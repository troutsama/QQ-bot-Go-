package chat

import (
	"context"
	"encoding/json"
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

// errEmptyReply 表示模型「成功返回」了空白文本。
// 它不是网络/鉴权错误，但同样没法回复用户，必须走失败兜底。
var errEmptyReply = errors.New("模型返回空文本")

// Request 是一条消息在步骤链里的上下文。
//
// 特意不叫 Turn：chat.Turn 已经是数据库轮次的聚合类型，同名无法编译。
type Request struct {
	In        platform.IncomingMessage
	Reply     *platform.ReplyRef
	Trace     string
	ConvID    int64
	Turn      TurnRef
	Cfg       config.Config
	Log       *slog.Logger
	ReplyText string // 由步骤链填充，出站层消费
}

// Step 是消息处理链的一个步骤。
// handled=true 表示后续步骤不再执行（例如 V2 的命令步骤直接回完）。
type Step interface {
	Name() string
	Process(ctx context.Context, r *Request) (handled bool, err error)
}

// Pipeline 顺序执行步骤，任一错误立即中止，handled=true 提前结束。
type Pipeline struct {
	steps []Step
}

func NewPipeline(steps ...Step) *Pipeline {
	return &Pipeline{steps: append([]Step(nil), steps...)}
}

func (p *Pipeline) Run(ctx context.Context, r *Request) (bool, error) {
	for _, step := range p.steps {
		handled, err := step.Process(ctx, r)
		if err != nil {
			// 用 %w 保留哨兵，Service 的 errorCopy 才能 errors.Is 到 llm.ErrAuth 等。
			return handled, fmt.Errorf("步骤 %s: %w", step.Name(), err)
		}
		if handled {
			return true, nil
		}
	}
	return false, nil
}

// aiStep 是 V1 唯一的步骤：拉上下文、调模型、把回复文本放进 Request。
type aiStep struct {
	client llm.Client
	store  Store
	loc    *time.Location
}

// NewAIStep 构造模型步骤。loc 为 nil 时按 UTC 处理（生产由 main 注入配置时区）。
func NewAIStep(client llm.Client, store Store, loc *time.Location) Step {
	if loc == nil {
		loc = time.UTC
	}
	return &aiStep{client: client, store: store, loc: loc}
}

func (s *aiStep) Name() string { return "ai" }

func (s *aiStep) Process(ctx context.Context, r *Request) (bool, error) {
	if r == nil {
		return false, errors.New("chat: 步骤请求不能为空")
	}

	turns, err := s.store.RecentTurns(ctx, r.ConvID, r.Cfg.Chat.ContextTurns)
	if err != nil {
		return false, fmt.Errorf("读取上下文: %w", err)
	}
	msgs := BuildMessages(turns, s.loc, r.Cfg.Chat.ContextTimePrefix)
	// 当前这轮还是 processing，RecentTurns 取不到它；必须显式补上，
	// 否则模型只看到历史、看不到用户刚说的话。
	msgs = append(msgs, llm.Message{Role: llm.RoleUser, Text: r.In.Text})

	req := llm.Request{
		System:      r.Cfg.LLM.SystemPrompt,
		Message:     msgs,
		Model:       r.Cfg.LLM.Model,
		MaxTokens:   r.Cfg.LLM.MaxTokens,
		Temperature: r.Cfg.LLM.Temperature,
		// Tools 恒空：V1 不做工具调用。
	}

	start := time.Now()
	s.emit(ctx, r, "llm_call", "", "调用模型", map[string]any{
		"model":    req.Model,
		"messages": len(msgs),
		"chars":    messagesChars(msgs),
	})

	resp, callErr := s.client.Complete(ctx, req)
	elapsed := time.Since(start).Milliseconds()
	if callErr != nil {
		s.emit(ctx, r, "llm_error", "error", "模型调用失败", map[string]any{
			"error":      callErr.Error(),
			"elapsed_ms": elapsed,
		})
		s.record(ctx, r, req, llm.Response{}, callErr, "failed", elapsed)
		return false, callErr
	}

	if strings.TrimSpace(resp.Text) == "" {
		emptyErr := fmt.Errorf("%w（输入 %d tokens，输出 %d tokens）",
			errEmptyReply, resp.Usage.InputTokens, resp.Usage.OutputTokens)
		s.emit(ctx, r, "llm_error", "error", "模型返回空文本", map[string]any{
			"error":      emptyErr.Error(),
			"elapsed_ms": elapsed,
		})
		s.record(ctx, r, req, resp, emptyErr, "failed", elapsed)
		return false, emptyErr
	}

	s.emit(ctx, r, "llm_result", "", "模型返回", map[string]any{
		"elapsed_ms": elapsed,
		"in_tokens":  resp.Usage.InputTokens,
		"out_tokens": resp.Usage.OutputTokens,
		"stop":       string(resp.StopReason),
	})

	// 快照写失败不影响用户拿回复：回复优先，审计记录只降级为日志。
	s.record(ctx, r, req, resp, nil, "success", elapsed)
	r.ReplyText = resp.Text
	return true, nil
}

// record 写一次模型请求的全量快照。turnID 交给 store 反写 turns.llm_request_id。
func (s *aiStep) record(ctx context.Context, r *Request, req llm.Request, resp llm.Response,
	callErr error, status string, elapsedMS int64) {

	rawReq, err := json.Marshal(req)
	if err != nil {
		rawReq = nil // 理论上不会失败（字段都可 JSON 序列化），仍做防御
	}
	rawResp, err := json.Marshal(resp)
	if err != nil {
		rawResp = nil
	}

	rec := &LLMRequestRecord{
		TraceID:          r.Trace,
		ConversationID:   r.ConvID,
		Provider:         s.client.Name(),
		Model:            req.Model,
		Temperature:      req.Temperature,
		PromptTokens:     resp.Usage.InputTokens,
		CompletionTokens: resp.Usage.OutputTokens,
		TotalTokens:      resp.Usage.InputTokens + resp.Usage.OutputTokens,
		DurationMS:       elapsedMS,
		Status:           status,
		RawRequest:       string(rawReq),
		RawResponse:      string(rawResp),
	}
	if callErr != nil {
		rec.ErrorMessage = callErr.Error()
	}
	if err := s.store.RecordLLMRequest(ctx, rec, r.Turn.ID); err != nil {
		logOf(r).Warn("记录模型请求快照失败", "error", err, "turn_id", r.Turn.ID)
	}
}

func (s *aiStep) emit(ctx context.Context, r *Request, kind, level, message string, payload map[string]any) {
	e := Event{
		Kind:    kind,
		Level:   level,
		Message: message,
		TraceID: r.Trace,
		AppID:   r.In.Conv.AppID,
		Payload: payload,
	}
	if r.ConvID > 0 {
		id := r.ConvID
		e.ConversationID = &id
	}
	if r.Turn.ID > 0 {
		id := r.Turn.ID
		e.TurnID = &id
	}
	if err := s.store.AppendEvent(ctx, e); err != nil {
		logOf(r).Warn("写入模型审计事件失败", "kind", kind, "error", err)
	}
}

func messagesChars(msgs []llm.Message) int {
	n := 0
	for _, m := range msgs {
		n += utf8.RuneCountInString(m.Text)
	}
	return n
}

func logOf(r *Request) *slog.Logger {
	if r.Log != nil {
		return r.Log
	}
	return slog.Default()
}
