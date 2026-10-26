package chat

import (
	"context"
	"errors"
	"log/slog"
	"unicode/utf8"

	"qq-bot/internal/config"
	"qq-bot/internal/platform"
)

// SendResult 汇总一次回复的发送结果。
//
// Sent 是「成功发出的段数」而不是段总数：R16 规定首段失败才算整轮失败，
// 只要有一段发出去，用户就已经看到了回复。
type SendResult struct {
	Segments       int
	Sent           int
	Err            error
	QuotaExhausted bool
}

// SendMeta 是写审计事件所需的上下文。它不是发送协议的一部分，
// 只是把 trace / 会话 / 轮次带到 outbound 事件里，方便 M4 按会话排障。
type SendMeta struct {
	TraceID string
	ConvID  int64
	TurnID  int64
}

// Outbound 负责把长文本切段并通过 Provider 发出。
// 它不修改轮次状态——状态由 Service 在拿到 SendResult 后统一决定。
type Outbound struct {
	provider platform.Provider
	sink     EventSink
	log      *slog.Logger
}

func NewOutbound(provider platform.Provider, sink EventSink, log *slog.Logger) *Outbound {
	if log == nil {
		log = slog.Default()
	}
	return &Outbound{provider: provider, sink: sink, log: log}
}

// SendText 切段并逐段发送。
//
// 失败策略（R16 / R1）：
//   - 第一段就失败 → 立即停止，Sent=0，由 Service 判 failed；
//   - 中间段失败 → 继续尝试后续段（前面的内容已经发出，不能回滚）；
//   - 配额耗尽 → 停止后续段，QuotaExhausted=true；
//   - 所有段共用同一个 ReplyRef，msg_seq 由 provider 的 NextSeq 递增。
//
// V1 不发「正在输入」、不加打字延迟：先把正确性做对，体验优化留到后面。
func (o *Outbound) SendText(
	ctx context.Context,
	conv platform.ConversationKey,
	reply *platform.ReplyRef,
	text string,
	cfg config.Config,
	meta SendMeta,
) SendResult {
	segments := Split(text, cfg.Chat.SegmentMaxChars, cfg.Chat.MaxReplySegments, segmentSplitOn)
	res := SendResult{Segments: len(segments)}
	if len(segments) == 0 {
		return res
	}

	for i, segment := range segments {
		sent, err := o.provider.Send(ctx, platform.OutgoingMessage{
			Conv:  conv,
			Reply: reply,
			Kind:  platform.OutText,
			Text:  segment,
		})
		if err != nil {
			quota := errors.Is(err, platform.ErrReplyQuotaExhausted)
			if quota {
				res.QuotaExhausted = true
			}
			if res.Err == nil {
				res.Err = err
			}
			o.emit(ctx, "outbound_error", "error", "发送消息失败", conv.AppID, meta, map[string]any{
				"error":    err.Error(),
				"segment":  i + 1,
				"segments": len(segments),
				"quota":    quota,
			})
			o.log.Warn("发送消息失败",
				"error", err,
				"segment", i+1,
				"segments", len(segments),
				"quota", quota,
				"trace_id", meta.TraceID,
			)
			if quota || res.Sent == 0 {
				break
			}
			continue
		}

		res.Sent++
		o.emit(ctx, "outbound", "", "发送消息", conv.AppID, meta, map[string]any{
			"msg_id":   sent.MsgID,
			"msg_seq":  sent.Seq,
			"chars":    utf8.RuneCountInString(segment),
			"segment":  i + 1,
			"segments": len(segments),
		})
	}
	return res
}

func (o *Outbound) emit(ctx context.Context, kind, level, message, appID string, meta SendMeta, payload map[string]any) {
	if o.sink == nil {
		return
	}
	e := Event{
		Kind:    kind,
		Level:   level,
		Message: message,
		TraceID: meta.TraceID,
		AppID:   appID,
		Payload: payload,
	}
	if meta.ConvID > 0 {
		id := meta.ConvID
		e.ConversationID = &id
	}
	if meta.TurnID > 0 {
		id := meta.TurnID
		e.TurnID = &id
	}
	if err := o.sink.AppendEvent(ctx, e); err != nil {
		o.log.Warn("写入出站审计事件失败", "kind", kind, "error", err)
	}
}
