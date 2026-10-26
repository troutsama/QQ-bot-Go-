package chat

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"
	"unicode/utf8"

	"qq-bot/internal/config"
	"qq-bot/internal/llm"
	"qq-bot/internal/platform"
)

const (
	// dedupTTL 是进程内去重的窗口。进程重启后表会清空，DB 的 UNIQUE 仍是最终真相。
	dedupTTL = 10 * time.Minute

	// staleTurnThreshold 是「处理中的轮多久没结束才算崩溃遗留」。
	// 必须大于一次正常 LLM 调用的最坏耗时（含超时/重试），否则会把正在处理的轮抢走。
	staleTurnThreshold = 5 * time.Minute

	segmentSplitOn = "\n\n"
	nonTextHint    = "目前只支持文字消息哦～"
)

// Deps 是 Service 的全部外部依赖，由组合根注入（禁止包级单例）。
type Deps struct {
	Provider platform.Provider
	LLM      llm.Client
	Store    Store

	// Cfg 返回当前配置的完整快照。每次收到消息只调用一次，
	// 避免热重载时 Chat.* 与 LLM.* 来自两份不同配置（撕裂读）。
	Cfg func() config.Config

	// Now 供 Dedup 与残轮判定使用；生产传 time.Now。
	Now func() time.Time
	// Loc 是上下文时间前缀使用的时区；main 按 Chat.Timezone 加载一次。
	Loc *time.Location
	Log *slog.Logger
}

// Service 编排「入站 → 去重 → 分片串行 → 上下文 → 模型 → 出站 → 落库」。
type Service struct {
	provider platform.Provider
	llm      llm.Client
	store    Store
	cfg      func() config.Config
	now      func() time.Time
	loc      *time.Location
	log      *slog.Logger

	dedup      *Dedup
	outbound   *Outbound
	pipeline   *Pipeline
	dispatcher *Dispatcher
}

// New 构造服务。依赖的合法性在 Run 里检查（New 不返回 error，便于组合根写起来干净）。
func New(deps Deps) *Service {
	if deps.Cfg == nil {
		deps.Cfg = func() config.Config { return config.Config{} }
	}
	if deps.Now == nil {
		deps.Now = time.Now
	}
	if deps.Loc == nil {
		deps.Loc = time.UTC
	}
	if deps.Log == nil {
		deps.Log = slog.Default()
	}

	s := &Service{
		provider: deps.Provider,
		llm:      deps.LLM,
		store:    deps.Store,
		cfg:      deps.Cfg,
		now:      deps.Now,
		loc:      deps.Loc,
		log:      deps.Log,
	}
	s.dedup = NewDedup(dedupTTL, deps.Now)
	s.outbound = NewOutbound(deps.Provider, deps.Store, deps.Log)
	s.pipeline = NewPipeline(NewAIStep(deps.LLM, deps.Store, deps.Loc))
	s.dispatcher = NewDispatcher(defaultShardCount, defaultShardQueueSize, s.handleOne)
	return s
}

// Run 启动消费者与分片 worker，阻塞到 ctx 取消或 Provider 关闭事件流。
//
// 内部再包一层 runCtx：Provider 关闭 Events 时，消费者必须能主动停掉 dispatcher，
// 否则 Run 会等在 dispatcher.Run 上永远不返回。
func (s *Service) Run(ctx context.Context) error {
	if s.provider == nil {
		return errors.New("chat: Provider 不能为空")
	}
	if s.store == nil {
		return errors.New("chat: Store 不能为空")
	}
	if s.llm == nil {
		return errors.New("chat: LLM 不能为空")
	}

	runCtx, cancel := context.WithCancel(ctx)
	defer cancel()

	consumerDone := make(chan struct{})
	go func() {
		defer close(consumerDone)
		defer cancel() // Provider 关闭事件流时，同步停掉 dispatcher

		for {
			select {
			case <-runCtx.Done():
				return
			case msg, ok := <-s.provider.Events():
				if !ok {
					return
				}
				if err := s.dispatcher.Submit(runCtx, msg); err != nil {
					// 正常关闭时 Submit 会因 ctx 取消而失败，不必刷 Warn。
					if runCtx.Err() == nil {
						s.log.Warn("入队失败", "error", err, "msg_id", msg.MsgID)
					}
				}
			}
		}
	}()

	s.dispatcher.Run(runCtx)
	<-consumerDone
	return nil
}

// handleOne 处理一条入站消息。它由同一个 shard 的 worker 串行调用，
// 因此同一会话的「建轮 → 调模型 → 发送 → 定状态」不会被并发穿插。
func (s *Service) handleOne(ctx context.Context, msg platform.IncomingMessage) {
	cfg := s.cfg()
	trace := traceIDOf(msg)
	log := s.log.With("trace_id", trace)

	s.appendEvent(ctx, Event{
		Kind:          "inbound",
		Message:       "收到消息",
		TraceID:       trace,
		AppID:         msg.Conv.AppID,
		PlatformMsgID: msg.MsgID,
		Payload: map[string]any{
			"msg_id":         msg.MsgID,
			"scene":          string(msg.Conv.Scene),
			"openid":         msg.Sender.OpenID,
			"chars":          utf8.RuneCountInString(msg.Text),
			"has_attachment": msg.HasAttachment,
		},
	})

	// ① 快路径去重：平台重推大概率落在同一个 10 分钟窗口内。
	if s.dedup.Seen(dedupKey(msg)) {
		s.appendEvent(ctx, Event{
			Kind:          "inbound_duplicate",
			Message:       "重复推送，跳过",
			TraceID:       trace,
			AppID:         msg.Conv.AppID,
			PlatformMsgID: msg.MsgID,
			Payload:       map[string]any{"msg_id": msg.MsgID, "reason": "dedup_cache"},
		})
		log.Info("重复推送，跳过", "msg_id", msg.MsgID)
		return
	}

	// ② 会话
	conv, _, err := s.store.GetOrCreateConversation(ctx, msg.Conv)
	if err != nil {
		log.Error("会话读写失败", "error", err)
		return
	}
	if conv == nil {
		log.Error("会话为空")
		return
	}
	convID := conv.ID

	// ③ 非文本：不调模型，记 filtered 轮，回固定提示（R13）
	if strings.TrimSpace(msg.Text) == "" {
		s.handleNonText(ctx, msg, convID, trace, cfg, log)
		return
	}

	// ④ 建轮（DB UNIQUE 是幂等的最终保证）
	tn, err := s.store.BeginTurn(ctx, TurnParams{
		ConvID:    convID,
		MsgID:     msg.MsgID,
		Sender:    msg.Sender,
		UserText:  msg.Text,
		TraceID:   trace,
		StartedAt: msg.Timestamp,
	})
	if err != nil {
		log.Error("建轮失败", "error", err)
		return
	}

	if !tn.IsNew {
		// R15：只有「processing + 超过阈值 + StaleTurns 确认」三条同时成立才接管。
		if !s.shouldReclaim(ctx, convID, tn) {
			log.Info("重复推送，跳过", "turn", tn.ID, "status", tn.Status)
			s.appendEvent(ctx, Event{
				Kind:           "inbound_duplicate",
				Message:        "已有轮次，跳过",
				TraceID:        trace,
				AppID:          msg.Conv.AppID,
				ConversationID: &convID,
				TurnID:         ptrID(tn.ID),
				PlatformMsgID:  msg.MsgID,
				Payload: map[string]any{
					"msg_id": msg.MsgID,
					"turn":   tn.ID,
					"status": string(tn.Status),
					"reason": "turn_exists",
				},
			})
			return
		}
		log.Warn("接管未完成的轮，重试", "turn", tn.ID)
	}

	// ⑤ 一条入站消息只建一个 ReplyRef；所有分段共用它，msg_seq 才会连续递增。
	reply := platform.NewReplyRef(msg.MsgID, msg.EventID)
	req := &Request{
		In:     msg,
		Reply:  reply,
		Trace:  trace,
		ConvID: convID,
		Turn:   tn,
		Cfg:    cfg,
		Log:    log,
	}
	meta := SendMeta{TraceID: trace, ConvID: convID, TurnID: tn.ID}

	handled, err := s.pipeline.Run(ctx, req)
	if err == nil && !handled {
		err = errors.New("没有任何步骤处理该消息")
	}
	if err != nil {
		// 关闭过程中不做失败落库、也不发兜底文案：
		// 轮保持 processing，下次启动由 R15 的接管路径继续。
		if ctx.Err() != nil {
			log.Info("服务关闭中，轮次保持 processing", "turn", tn.ID)
			return
		}
		log.Error("处理消息失败", "error", err, "turn", tn.ID)
		if ferr := s.store.FailTurn(ctx, tn.ID); ferr != nil {
			log.Warn("标记失败轮出错", "error", ferr, "turn", tn.ID)
		}
		s.outbound.SendText(ctx, msg.Conv, reply, errorCopy(err), cfg, meta)
		return
	}

	// ⑥ 出站
	res := s.outbound.SendText(ctx, msg.Conv, reply, req.ReplyText, cfg, meta)
	if res.Sent == 0 {
		log.Warn("回复未发出", "error", res.Err, "turn", tn.ID, "segments", res.Segments)
		if ferr := s.store.FailTurn(ctx, tn.ID); ferr != nil {
			log.Warn("标记失败轮出错", "error", ferr, "turn", tn.ID)
		}
		return
	}

	// ⑦ 定轮状态（R16）：只要发出去至少一段，就算成功。
	if err := s.store.FinishTurn(ctx, tn.ID, req.ReplyText); err != nil {
		log.Warn("结束轮次失败", "error", err, "turn", tn.ID)
	}
}

// handleNonText 处理纯附件/空白消息：记 filtered，不调模型。
func (s *Service) handleNonText(
	ctx context.Context,
	msg platform.IncomingMessage,
	convID int64,
	trace string,
	cfg config.Config,
	log *slog.Logger,
) {
	tn, err := s.store.BeginTurn(ctx, TurnParams{
		ConvID:    convID,
		MsgID:     msg.MsgID,
		Sender:    msg.Sender,
		UserText:  "",
		TraceID:   trace,
		StartedAt: msg.Timestamp,
	})
	if err != nil {
		log.Error("建轮失败", "error", err)
		return
	}
	if !tn.IsNew {
		s.appendEvent(ctx, Event{
			Kind:           "inbound_duplicate",
			Message:        "非文本消息已有轮次，跳过",
			TraceID:        trace,
			AppID:          msg.Conv.AppID,
			ConversationID: &convID,
			TurnID:         ptrID(tn.ID),
			PlatformMsgID:  msg.MsgID,
			Payload:        map[string]any{"msg_id": msg.MsgID, "turn": tn.ID, "reason": "turn_exists"},
		})
		return
	}

	if err := s.store.FilterTurn(ctx, tn.ID); err != nil {
		log.Warn("标记 filtered 轮出错", "error", err, "turn", tn.ID)
	}
	s.appendEvent(ctx, Event{
		Kind:           "inbound_filtered",
		Message:        "非文本消息，未调用模型",
		TraceID:        trace,
		AppID:          msg.Conv.AppID,
		ConversationID: &convID,
		TurnID:         ptrID(tn.ID),
		PlatformMsgID:  msg.MsgID,
		Payload: map[string]any{
			"msg_id":         msg.MsgID,
			"turn":           tn.ID,
			"has_attachment": msg.HasAttachment,
		},
	})

	reply := platform.NewReplyRef(msg.MsgID, msg.EventID)
	s.outbound.SendText(ctx, msg.Conv, reply, nonTextHint, cfg,
		SendMeta{TraceID: trace, ConvID: convID, TurnID: tn.ID})
}

// shouldReclaim 实现 R15 的双重判定。
//
// 只比较时间戳不够：另一台 worker 可能刚抢到同一条消息并在处理；
// 以 StaleTurns（数据库里的 processing 残轮清单）为准，才能把「崩溃遗留」
// 与「正在进行」区分开。StaleTurns 只读，不在这里改任何状态。
func (s *Service) shouldReclaim(ctx context.Context, convID int64, tn TurnRef) bool {
	if tn.Status != StatusProcessing {
		return false
	}
	if s.now().Sub(tn.StartedAt) <= staleTurnThreshold {
		return false
	}
	refs, err := s.store.StaleTurns(ctx, convID, staleTurnThreshold)
	if err != nil {
		s.log.Warn("查询残轮失败", "error", err, "turn", tn.ID)
		return false
	}
	for _, ref := range refs {
		if ref.ID == tn.ID {
			return true
		}
	}
	return false
}

func (s *Service) appendEvent(ctx context.Context, e Event) {
	if s.store == nil {
		return
	}
	if err := s.store.AppendEvent(ctx, e); err != nil {
		s.log.Warn("写入审计事件失败", "kind", e.Kind, "error", err)
	}
}

func dedupKey(msg platform.IncomingMessage) string {
	id := msg.MsgID
	if id == "" {
		id = msg.EventID
	}
	return msg.Conv.AppID + "|" + id
}

// traceIDOf 优先复用平台消息 ID：它天然唯一，排障时能直接和平台日志对上。
func traceIDOf(msg platform.IncomingMessage) string {
	if msg.MsgID != "" {
		return msg.MsgID
	}
	var b [8]byte
	if _, err := rand.Read(b[:]); err != nil {
		return fmt.Sprintf("trace-%d", time.Now().UnixNano())
	}
	return hex.EncodeToString(b[:])
}

func ptrID(id int64) *int64 {
	if id <= 0 {
		return nil
	}
	return &id
}
