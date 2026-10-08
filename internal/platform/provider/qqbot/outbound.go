package qqbot

import (
	"context"
	"fmt"
	"qq-bot/internal/platform"

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
