package chat

import (
	"context"
	"errors"

	"qq-bot/internal/llm"
	"qq-bot/internal/platform"
)

// errorCopy 把内部错误映射成用户可读文案。
//
// 规则（02 §3.6）：不出现 HTTP 状态码、API 原始错误体、base_url、key 片段。
// 这里是用户可见层的唯一出口，调用方绝不要把 err.Error() 直接发出去。
//
// V1 没有 /clear 命令，所以「上下文过长」的文案要说「重新开始一段对话」，
// 不能照抄设计文档里的命令提示。
func errorCopy(err error) string {
	switch {
	case errors.Is(err, llm.ErrAuth):
		return "❌ 模型服务鉴权失败，请联系管理员检查 API Key。"
	case errors.Is(err, llm.ErrRateLimited):
		return "⚠️ 模型服务限流中，请稍后再试。"
	case errors.Is(err, llm.ErrContextTooLong):
		return "📏 本次对话太长了，请重新开始一段对话。"
	case errors.Is(err, platform.ErrReplyQuotaExhausted):
		return "⏳ 短时间内消息太多了，请过一会儿再试。"
	case errors.Is(err, context.DeadlineExceeded):
		return "🐢 模型思考太久了，请再试一次。"
	default:
		return "❌ 处理出错了，请稍后再试。"
	}
}
