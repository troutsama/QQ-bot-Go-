package platform

import "errors"

// 跨层错误：用哨兵 + errors.Is 判断，绝不靠字符串匹配（02 §3.6）。
var (
	// ErrSendUnrecorded 表示 QQ 已返回发送成功，但渠道记录未能保存。
	// 调用方必须保留 Send 返回的回执，不得因此自动重发。
	ErrSendUnrecorded = errors.New("platform: 消息已发送但未入库，禁止自动重发")

	// ErrReplyQuotaExhausted 被动回复次数已耗尽（单聊 4 次 / 60 分钟窗口）。
	// chat 收到它应当：记 outbound_error 事件、停止发送后续段、不再重试。
	ErrReplyQuotaExhausted = errors.New("platform: 被动回复次数已耗尽")

	// ErrNotConnected 尚未连接或已关闭。
	ErrNotConnected = errors.New("platform: 未连接")

	// ErrUnsupportedScene V1 只实现单聊。
	ErrUnsupportedScene = errors.New("platform: 不支持的会话场景")
)
