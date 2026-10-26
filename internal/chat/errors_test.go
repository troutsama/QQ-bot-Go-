package chat

import (
	"context"
	"errors"
	"strings"
	"testing"

	"qq-bot/internal/llm"
	"qq-bot/internal/platform"
)

func TestErrorCopy(t *testing.T) {
	tests := []struct {
		name string
		err  error
		want string
	}{
		{"auth", llm.ErrAuth, "❌ 模型服务鉴权失败，请联系管理员检查 API Key。"},
		{"rate", llm.ErrRateLimited, "⚠️ 模型服务限流中，请稍后再试。"},
		{"context", llm.ErrContextTooLong, "📏 本次对话太长了，请重新开始一段对话。"},
		{"quota", platform.ErrReplyQuotaExhausted, "⏳ 短时间内消息太多了，请过一会儿再试。"},
		{"timeout", context.DeadlineExceeded, "🐢 模型思考太久了，请再试一次。"},
		{"unknown", errors.New("boom 429 sk-secret"), "❌ 处理出错了，请稍后再试。"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := errorCopy(tt.err)
			if got != tt.want {
				t.Fatalf("errorCopy(%v) = %q，期望 %q", tt.err, got, tt.want)
			}
			for _, leak := range []string{"401", "429", "boom", "sk-", "base_url"} {
				if strings.Contains(got, leak) {
					t.Fatalf("用户文案泄漏内部信息 %q: %q", leak, got)
				}
			}
			if strings.Contains(got, "/clear") {
				t.Fatalf("V1 文案不应提 /clear: %q", got)
			}
		})
	}
}

func TestErrorCopy_WrappedSentinel(t *testing.T) {
	err := errors.Join(errors.New("transport"), llm.ErrRateLimited)
	if got := errorCopy(err); got != "⚠️ 模型服务限流中，请稍后再试。" {
		t.Fatalf("包裹后的哨兵未被识别: %q", got)
	}
}
