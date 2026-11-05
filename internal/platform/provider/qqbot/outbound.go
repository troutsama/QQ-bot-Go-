package qqbot

import (
	"context"
	"fmt"
	"qq-bot/internal/platform"
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
	result := platform.SentMessage{Seq: mc.MsgSeq}
	if sent == nil || sent.ID == "" {
		// API 没有报错，但回执不足以建档；不能把它当作未发送来重试。
		return result, fmt.Errorf("qqbot: QQ 返回成功但缺少消息 ID: %w", platform.ErrSendUnrecorded)
	}
	result.MsgID = sent.ID
	conv := msg.Conv
	conv.AppID = p.appID // 记录实际调用 QQ API 的机器人身份。
	// 网络已经成功，调用方取消不应阻止保存结果；独立设置写库上限。
	persistCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), channelPersistenceTimeout)
	defer cancel()
	if _, err := p.store.RecordChannelMessage(persistCtx, platform.ChannelMessage{
		Provider: p.Name(), Conv: conv, Direction: platform.ChannelOutgoing,
		MsgID: sent.ID, ReplyToMsgID: mc.MsgID, ReplyToEventID: mc.EventID,
		Seq: mc.MsgSeq, Kind: string(msg.Kind), Text: mc.Content,
		OccurredAt: time.Now(),
	}); err != nil {
		p.log.Error("qqbot：消息已发送但未入库，不重发", "error", err, "sent_msg_id", sent.ID, "conv", conv.String())
		return result, fmt.Errorf("qqbot: 保存已发送消息 %s: %w: %w", sent.ID, platform.ErrSendUnrecorded, err)
	}
	return result, nil

}
