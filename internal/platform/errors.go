package platform

import "errors"

// 跨层错误：用哨兵 + errors.Is 判断，绝不靠字符串匹配（02 §3.6）。
var (
	// ErrReplyQuotaExhausted 被动回复次数已耗尽（单聊 4 次 / 60 分钟窗口）。
	// chat 收到它应当：记 outbound_error 事件、停止发送后续段、不再重试。
	ErrReplyQuotaExhausted = errors.New("platform: 被动回复次数已耗尽")

	// ErrNotConnected 尚未连接或已关闭。
	ErrNotConnected = errors.New("platform: 未连接")

	// ErrUnsupportedScene V1 只实现单聊。
	ErrUnsupportedScene = errors.New("platform: 不支持的会话场景")
)
