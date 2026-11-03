package llm

import "errors"

// 跨层错误：chat 层据此选择**面向用户的文案**，绝不把 provider 原文抛给用户（02 §3.6）。
var (
	ErrRetryable      = errors.New("llm: 可重试错误") // 网络抖动、5xx、空 choices
	ErrAuth           = errors.New("llm: 鉴权失败")  // 401/403 → 配置错，重试无意义还会烧配额
	ErrRateLimited    = errors.New("llm: 触发限流")  // 429
	ErrContextTooLong = errors.New("llm: 上下文超长") // 需要裁剪历史再试
)
