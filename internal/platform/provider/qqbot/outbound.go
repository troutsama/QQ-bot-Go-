package qqbot

import (
	"context"
	"fmt"
	"qq-bot/internal/platform"
	"qq-bot/internal/store"
	"time"

	"github.com/tencent-connect/botgo/dto"
)

func (p *Provider) Send(ctx context.Context, msg platform.OutgoingMessage) (platform.SentMessage, error) {
	if msg.Conv.Scene != platform.SceneC2C {
		return platform.SentMessage{}, fmt.Errorf("qqbot：%w, %q", platform.ErrUnsupportedScene, msg.Conv.Scene)
	}
	p.sendMu.RLock()
	defer p.sendMu.RUnlock()
	if p.closed {
		return platform.SentMessage{}, platform.ErrNotConnected
	}
	if st := p.Status(); st != platform.StatusConnected {
		return platform.SentMessage{}, fmt.Errorf("qqbot：%w（当前状态 %s）", platform.ErrNotConnected, st)
	}

	mc := dto.MessageToCreate{Content: msg.Text}

	switch msg.Kind {
	case platform.OutText:
		// content 已填，无需额外动作
	case platform.OutTyping:
		mc.MsgType = dto.InputNotifyMsg
		mc.InputNotify = &dto.InputNotify{
			InputType:   1,
			InputSecond: 5,
		}
	default:
		return platform.SentMessage{}, fmt.Errorf("qqbot：未知出站类型 %q", msg.Kind)
	}
	if msg.Reply != nil {
		mc.MsgID = msg.Reply.MsgID
		mc.EventID = msg.Reply.EventID
		mc.MsgSeq = msg.Reply.NextSeq()
	} else {
		p.log.Warn("qqbot：发送主动消息", "scene", string(msg.Conv.Scene))
	}
	sent, err := p.api.PostC2CMessage(ctx, msg.Conv.SceneID, mc)
	if err != nil {
		if IsQuotaExhausted(err) {
			// 用 %w 包住哨兵 + %v 带上原始错误文本：
			// 上层能 errors.Is(err, platform.ErrReplyQuotaExhausted) 判断，
			// 同时日志里还留着平台返回的原文（排障必需）。
			return platform.SentMessage{}, fmt.Errorf("%w: %v", platform.ErrReplyQuotaExhausted, err)
		}
		return platform.SentMessage{}, fmt.Errorf("qqbot: 发送单聊消息: %w", err)
	}
	return platform.SentMessage{MsgID: sent.ID, Seq: mc.MsgSeq}, nil

}
func (p *Provider) runOutbound(ctx context.Context) {
	for {
		select {
		case <-ctx.Done():
			return
		case msg, ok := <-p.events:
			if !ok {
				return // Stop 已 close(p.events)
			}
			p.handleMessage(ctx, msg)
		}
	}
}
func (p *Provider) handleMessage(ctx context.Context, msg platform.IncomingMessage) {
	if msg.Text == "" {
		return // 只发图片/语音，V1 不处理
	}

	// ① 找房
	convID, err := p.persist.EnsureConversation(ctx, msg.Conv.AppID, string(msg.Conv.Scene), msg.Conv.SceneID)
	if err != nil {
		p.log.Error("会话入库失败", "error", err, "msg_id", msg.MsgID)
		return
	}

	// ② 开本（幂等）
	turn, err := p.persist.BeginTurn(ctx, store.TurnParams{
		ConvID:    convID,
		MsgID:     msg.MsgID,
		UserText:  msg.Text,
		TraceID:   fmt.Sprintf("%x", time.Now().UnixNano()),
		StartedAt: msg.Timestamp,
	})
	if err != nil {
		p.log.Error("开轮失败", "error", err, "msg_id", msg.MsgID)
		return
	}

	// ③ 用户那句入库（重复事件不重写）
	if turn.IsNew {
		if _, err := p.persist.AppendMessage(ctx, turn.ID, "user", msg.Text); err != nil {
			p.log.Error("用户消息入库失败", "error", err, "turn_id", turn.ID)
		}
	}

	replyText := "你说的是：" + msg.Text

	// ④ 回复先落库再发送
	if _, err := p.persist.AppendMessage(ctx, turn.ID, "assistant", replyText); err != nil {
		p.log.Error("回复入库失败", "error", err, "turn_id", turn.ID)
	}

	// ⑤ 发送 + 收尾
	reply := platform.NewReplyRef(msg.MsgID, msg.EventID)
	if _, err := p.Send(ctx, platform.OutgoingMessage{
		Conv:  msg.Conv,
		Reply: reply,
		Kind:  platform.OutText,
		Text:  replyText,
	}); err != nil {
		p.log.Error("回显失败", "error", err, "msg_id", msg.MsgID)
		if e := p.persist.FinishTurn(ctx, turn.ID, store.StatusFailed, ""); e != nil {
			p.log.Error("轮次标记失败", "error", e, "turn_id", turn.ID)
		}
		return
	}
	if e := p.persist.FinishTurn(ctx, turn.ID, store.StatusOK, replyText); e != nil {
		p.log.Error("轮次收尾失败", "error", e, "turn_id", turn.ID)
	}
}
