package qqbot

import (
	"errors"
	"qq-bot/internal/platform"
	"time"

	"github.com/tencent-connect/botgo/dto"
)

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
