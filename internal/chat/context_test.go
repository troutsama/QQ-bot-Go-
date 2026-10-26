package chat

import (
	"strings"
	"testing"
	"time"

	"qq-bot/internal/llm"
)

// 2025-09-26 06:00:00 UTC。用 FixedZone 而不是 LoadLocation，
// 避免测试依赖 Windows 的 tzdata；生产时区由 main 加载后注入。
const sampleMS = int64(1758866400000)

func TestBuildMessages_MapsRolesAndSkipsTool(t *testing.T) {
	turns := []Turn{
		{
			Messages: []StoredMessage{
				{Role: "user", Content: "问题", CreatedAt: sampleMS},
				{Role: "tool", Content: `{"r":1}`, CreatedAt: sampleMS},
				{Role: "assistant", Content: "回答", CreatedAt: sampleMS},
			},
		},
	}

	msgs := BuildMessages(turns, time.UTC, false)
	if len(msgs) != 2 {
		t.Fatalf("消息条数 = %d，期望 2（tool 应被跳过）", len(msgs))
	}
	if msgs[0].Role != llm.RoleUser || msgs[0].Text != "问题" {
		t.Fatalf("第 1 条 = %+v", msgs[0])
	}
	if msgs[1].Role != llm.RoleAssistant || msgs[1].Text != "回答" {
		t.Fatalf("第 2 条 = %+v", msgs[1])
	}
}

func TestBuildMessages_TimePrefixUsesInjectedLocation(t *testing.T) {
	east8 := time.FixedZone("UTC+8", 8*3600)
	west5 := time.FixedZone("UTC-5", -5*3600)
	turns := []Turn{{Messages: []StoredMessage{
		{Role: "user", Content: "你好", CreatedAt: sampleMS},
	}}}

	east := BuildMessages(turns, east8, true)
	west := BuildMessages(turns, west5, true)

	if east[0].Text != "[09-26 14:00] 你好" {
		t.Fatalf("UTC+8 前缀 = %q，期望 [09-26 14:00] 你好", east[0].Text)
	}
	if west[0].Text != "[09-26 01:00] 你好" {
		t.Fatalf("UTC-5 前缀 = %q，期望 [09-26 01:00] 你好", west[0].Text)
	}
}

func TestBuildMessages_TimePrefixDisabled(t *testing.T) {
	turns := []Turn{{Messages: []StoredMessage{
		{Role: "user", Content: "你好", CreatedAt: sampleMS},
	}}}
	msgs := BuildMessages(turns, time.FixedZone("UTC+8", 8*3600), false)
	if strings.Contains(msgs[0].Text, "[") {
		t.Fatalf("关闭时间前缀后仍出现方括号: %q", msgs[0].Text)
	}
}

func TestBuildMessages_ZeroTimestampHasNoPrefix(t *testing.T) {
	turns := []Turn{{Messages: []StoredMessage{
		{Role: "user", Content: "你好", CreatedAt: 0},
	}}}
	msgs := BuildMessages(turns, time.UTC, true)
	if msgs[0].Text != "你好" {
		t.Fatalf("CreatedAt=0 不应加前缀，实际 %q", msgs[0].Text)
	}
}

func TestBuildMessages_EmptyReturnsNonNilSlice(t *testing.T) {
	for _, turns := range [][]Turn{nil, {}} {
		msgs := BuildMessages(turns, nil, true)
		if msgs == nil {
			t.Fatal("空输入应返回空切片而不是 nil（便于 JSON 序列化为 []）")
		}
		if len(msgs) != 0 {
			t.Fatalf("空输入返回了 %d 条消息", len(msgs))
		}
	}
}

func TestBuildMessages_NilLocationFallsBackToUTC(t *testing.T) {
	turns := []Turn{{Messages: []StoredMessage{
		{Role: "user", Content: "你好", CreatedAt: sampleMS},
	}}}
	msgs := BuildMessages(turns, nil, true)
	if msgs[0].Text != "[09-26 06:00] 你好" {
		t.Fatalf("nil loc 应退化为 UTC，实际 %q", msgs[0].Text)
	}
}
