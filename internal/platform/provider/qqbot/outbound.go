package qqbot

import (
	"context"
	"fmt"
	"log/slog"
	"qq-bot/internal/platform"
	"qq-bot/internal/store"

	"github.com/tencent-connect/botgo/dto"
)

// sendReply 回一条消息：回复先落库 → 发送 → 按发送结果给轮次收尾。
//
// 网络发送刻意放在事务之外：「生成回复」不等于「发送成功」，两者要分开记录。
// QQ 成功而更新数据库失败时只记错误、不重发 —— 重发会让用户看到重复内容。
func sendReply(ctx context.Context, p platform.Provider, st *store.Store, log *slog.Logger,
	msg platform.IncomingMessage, turn store.TurnRef, text string) {

	if _, err := st.AppendMessage(ctx, turn.ID, "assistant", text); err != nil {
		log.Error("回复入库失败", "error", err, "turn_id", turn.ID)
	}

	reply := platform.NewReplyRef(msg.MsgID, msg.EventID)
	if _, err := p.Send(ctx, platform.OutgoingMessage{
		Conv:  msg.Conv,
		Reply: reply,
		Kind:  platform.OutText,
		Text:  text,
	}); err != nil {
		log.Error("回显失败",
			"kind", "outbound_error",
			"error", err,
			"turn_id", turn.ID,
			"msg_id", msg.MsgID,
		)
		if e := st.FinishTurn(ctx, turn.ID, store.StatusFailed, ""); e != nil {
			log.Error("轮次标记失败", "error", e, "turn_id", turn.ID)
		}
		return
	}

	// 发送成功才记 outbound 事件：这条是「用户真的看到了」的证据。
	log.Info("回复已发送",
		"kind", "outbound",
		"turn_id", turn.ID,
		"msg_id", msg.MsgID,
	)
	if e := st.FinishTurn(ctx, turn.ID, store.StatusOK, text); e != nil {
		log.Error("轮次收尾失败", "error", e, "turn_id", turn.ID)
	}
}

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
