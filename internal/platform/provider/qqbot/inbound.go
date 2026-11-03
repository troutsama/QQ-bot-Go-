package qqbot

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"qq-bot/internal/platform"
	"qq-bot/internal/store"
	"time"

	"github.com/tencent-connect/botgo/dto"
)

// handleMessage 处理一条入站消息：空文本跳过 → 会话/轮次/用户消息入库 → 回复。
//
// 三件事的顺序不能换：先拿到轮次主键，回复才写得到 messages.turn_id 上。
func handleMessage(ctx context.Context, p platform.Provider, st *store.Store, log *slog.Logger, msg platform.IncomingMessage) {
	// "kind" 让这条记录同时进 events 表（业务事件）；其余日志只留文件。
	log.Info("收到消息",
		"kind", "inbound",
		"conv", msg.Conv.String(),
		"msg_id", msg.MsgID,
		"text", msg.Text,
	)
	if msg.Text == "" {
		return // 只发图片/语音，V1 不处理
	}
	turn, ok := persistInbound(ctx, st, log, msg)
	if !ok {
		return
	}
	// 重复事件（QQ 把同一条 msg_id 又推了一次）：这一轮已经入库并回复过，
	// 这里直接返回。否则用户会收到两条一样的回复，assistant 片段也会写两遍。
	if !turn.IsNew {
		log.Warn("重复入站事件，跳过回复",
			"msg_id", msg.MsgID,
			"turn_id", turn.ID,
		)
		return
	}
	sendReply(ctx, p, st, log, msg, turn, "你说的是："+msg.Text)
}

// persistInbound 入站落库：会话（找房）→ 轮次（开本，幂等）→ 用户消息。
//
// 返回 ok=false 表示这轮没法继续，调用方应该跳过（不入库也不回复）。
func persistInbound(ctx context.Context, st *store.Store, log *slog.Logger, msg platform.IncomingMessage) (store.TurnRef, bool) {
	convID, err := st.EnsureConversation(ctx, msg.Conv.AppID, string(msg.Conv.Scene), msg.Conv.SceneID)
	if err != nil {
		log.Error("会话入库失败", "error", err, "msg_id", msg.MsgID)
		return store.TurnRef{}, false
	}

	turn, err := st.BeginTurn(ctx, store.TurnParams{
		ConvID:    convID,
		MsgID:     msg.MsgID,
		UserText:  msg.Text,
		TraceID:   fmt.Sprintf("%x", time.Now().UnixNano()),
		StartedAt: msg.Timestamp,
	})
	if err != nil {
		log.Error("开轮失败", "error", err, "msg_id", msg.MsgID)
		return store.TurnRef{}, false
	}

	// 重复事件命中已有轮：用户那句已经写过，不重写。
	if turn.IsNew {
		if _, err := st.AppendMessage(ctx, turn.ID, "user", msg.Text); err != nil {
			log.Error("用户消息入库失败", "error", err, "turn_id", turn.ID)
		}
	}
	return turn, true
}

func translateC2C(appID string, d *dto.WSC2CMessageData) (platform.IncomingMessage, error) {
	if d == nil {
		return platform.IncomingMessage{}, errors.New("事件为 nil")
	}
	// dto.Message.Author 是 *dto.User —— 指针，必须判空：某天事件缺字段就直接 panic。
	if d.Author == nil || d.Author.ID == "" {
		return platform.IncomingMessage{}, errors.New("事件缺少 author.id（C2C 场景下它是 user_openid）")
	}
	// msg_id 是被动回复的**唯一**凭据：没有它，这条消息只能用「主动消息」回
	//（受频控，且用户可以在客户端关掉）。所以这里必须挡住，不能放行。
	if d.ID == "" {
		return platform.IncomingMessage{}, errors.New("事件缺少消息 id（被动回复凭据）")
	}
	// Timestamp 是 dto.Timestamp（底层 string，RFC3339），**不是** time.Time，
	// 要经过 .Time() 转换，而它会返回 error。解析失败就退化成 now，
	// 不要因为时间戳格式怪就丢掉整条消息。
	ts := time.Now()
	if t, err := d.Timestamp.Time(); err == nil {
		ts = t
	}
	//入库
	return platform.IncomingMessage{
		MsgID: d.ID,
		Conv: platform.ConversationKey{
			AppID:   appID,
			Scene:   platform.SceneC2C,
			SceneID: d.Author.ID,
		},
		Sender: platform.Sender{
			OpenID: d.Author.ID,
		},
		Text:          d.Content,
		HasAttachment: len(d.Attachments) > 0,
		MentionedBot:  false,
		Timestamp:     ts,
		Raw:           d,
	}, nil
}
