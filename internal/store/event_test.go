package store_test

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"qq-bot/internal/chat"
)

func TestAppendEvent_RoundTrip(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	convID := mustConversation(t, s)

	e := chat.Event{
		Kind:           "inbound",
		Level:          "info",
		AppID:          "app1",
		TraceID:        "trace-1",
		ConversationID: &convID,
		PlatformMsgID:  "msg-1",
		Message:        "收到消息",
		Payload:        map[string]any{"chars": 2},
	}
	if err := s.AppendEvent(ctx, e); err != nil {
		t.Fatalf("AppendEvent 失败: %v", err)
	}

	events, next, err := s.QueryEvents(ctx, chat.EventQuery{Kind: "inbound"})
	if err != nil {
		t.Fatalf("QueryEvents 失败: %v", err)
	}
	if next != 0 {
		t.Fatalf("单页结果不应有下一页游标，实际 %d", next)
	}
	if len(events) != 1 {
		t.Fatalf("事件数 = %d，期望 1", len(events))
	}

	got := events[0]
	if got.Kind != "inbound" || got.Level != "info" || got.TraceID != "trace-1" ||
		got.PlatformMsgID != "msg-1" || got.Message != "收到消息" || got.AppID != "app1" {
		t.Fatalf("事件字段不正确: %+v", got)
	}
	if got.ConversationID == nil || *got.ConversationID != convID {
		t.Fatalf("conversation_id 未正确回读: %v", got.ConversationID)
	}

	payload, ok := got.Payload.(string)
	if !ok {
		t.Fatalf("payload 类型 = %T，期望 string", got.Payload)
	}
	if !json.Valid([]byte(payload)) || !strings.Contains(payload, `"chars":2`) {
		t.Fatalf("payload 不是预期的合法 JSON: %q", payload)
	}
}

func TestAppendEvent_TruncatesPayloadToValidJSON(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()

	e := chat.Event{
		Kind:    "llm_result",
		Message: "超大响应",
		Payload: map[string]any{"body": strings.Repeat("x", 100*1024)},
	}
	if err := s.AppendEvent(ctx, e); err != nil {
		t.Fatalf("AppendEvent 失败: %v", err)
	}

	events, _, err := s.QueryEvents(ctx, chat.EventQuery{Kind: "llm_result"})
	if err != nil {
		t.Fatalf("QueryEvents 失败: %v", err)
	}
	if len(events) != 1 {
		t.Fatalf("事件数 = %d，期望 1", len(events))
	}

	payload, _ := events[0].Payload.(string)
	if !json.Valid([]byte(payload)) {
		t.Fatalf("截断后的 payload 必须是合法 JSON，实际: %q", payload)
	}
	if !strings.Contains(payload, `"truncated":true`) {
		t.Fatalf("超限 payload 应替换为 truncated 标记，实际: %q", payload)
	}
	if len(payload) > 8<<10 {
		t.Fatalf("payload 长度 = %d，超过上限 8KB", len(payload))
	}
}

func TestQueryEvents_Filters(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	convID := mustConversation(t, s)

	base := time.Now().Add(-time.Hour)
	rows := []chat.Event{
		{Kind: "llm_call", Level: "debug", TraceID: "trace-a", ConversationID: &convID,
			Message: "调用模型", CreatedAt: base.UnixMilli()},
		{Kind: "inbound", Level: "info", TraceID: "trace-b",
			Message: "收到消息", CreatedAt: base.Add(10 * time.Minute).UnixMilli()},
		{Kind: "llm_error", Level: "error", TraceID: "trace-a",
			Message: "模型失败", CreatedAt: base.Add(20 * time.Minute).UnixMilli()},
	}
	for _, e := range rows {
		if err := s.AppendEvent(ctx, e); err != nil {
			t.Fatalf("AppendEvent 失败: %v", err)
		}
	}

	cases := []struct {
		name  string
		query chat.EventQuery
		want  int
	}{
		{"按 kind", chat.EventQuery{Kind: "llm_call"}, 1},
		{"按 level", chat.EventQuery{Level: "error"}, 1},
		{"按 trace_id", chat.EventQuery{TraceID: "trace-a"}, 2},
		{"按 conversation_id", chat.EventQuery{ConversationID: &convID}, 1},
		{"since", chat.EventQuery{Since: timePtr(base.Add(5 * time.Minute))}, 2},
		{"until", chat.EventQuery{Until: timePtr(base.Add(5 * time.Minute))}, 1},
		{"组合", chat.EventQuery{TraceID: "trace-a", Level: "error"}, 1},
		{"无匹配", chat.EventQuery{Kind: "not-exist"}, 0},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			events, _, err := s.QueryEvents(ctx, tc.query)
			if err != nil {
				t.Fatalf("QueryEvents 失败: %v", err)
			}
			if len(events) != tc.want {
				t.Fatalf("返回 %d 条，期望 %d 条", len(events), tc.want)
			}
		})
	}
}

func TestQueryEvents_CursorPagination(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()

	const total = 25
	const llmCalls = 10
	for i := 0; i < total; i++ {
		kind := "inbound"
		if i < llmCalls {
			kind = "llm_call"
		}
		if err := s.AppendEvent(ctx, chat.Event{
			Kind:      kind,
			Message:   "事件",
			CreatedAt: time.Now().Add(time.Duration(i) * time.Millisecond).UnixMilli(),
		}); err != nil {
			t.Fatalf("AppendEvent 失败: %v", err)
		}
	}

	var (
		got    []chat.Event
		seen   = make(map[int64]bool, llmCalls)
		cursor int64
	)
	for page := 0; ; page++ {
		if page > total {
			t.Fatal("分页没有收敛，疑似游标错误")
		}
		events, next, err := s.QueryEvents(ctx, chat.EventQuery{
			Kind:   "llm_call",
			Limit:  4,
			Cursor: cursor,
		})
		if err != nil {
			t.Fatalf("QueryEvents 失败: %v", err)
		}
		for _, e := range events {
			if e.Kind != "llm_call" {
				t.Fatalf("过滤失效，返回了 kind=%q", e.Kind)
			}
			if seen[e.ID] {
				t.Fatalf("游标分页出现重复行 id=%d", e.ID)
			}
			seen[e.ID] = true
		}
		got = append(got, events...)
		if next == 0 {
			break
		}
		cursor = next
	}

	if len(got) != llmCalls {
		t.Fatalf("分页共取到 %d 条，期望 %d 条", len(got), llmCalls)
	}
	// 按 id 倒序返回
	for i := 1; i < len(got); i++ {
		if got[i-1].ID <= got[i].ID {
			t.Fatalf("事件未按 id 倒序返回: %d 在 %d 之前", got[i-1].ID, got[i].ID)
		}
	}
}

func TestQueryEvents_LimitClamped(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()

	for i := 0; i < 3; i++ {
		if err := s.AppendEvent(ctx, chat.Event{Kind: "system", Message: "启动"}); err != nil {
			t.Fatalf("AppendEvent 失败: %v", err)
		}
	}

	for _, limit := range []int{0, -1, 10000} {
		events, _, err := s.QueryEvents(ctx, chat.EventQuery{Limit: limit})
		if err != nil {
			t.Fatalf("limit=%d 时 QueryEvents 失败: %v", limit, err)
		}
		if len(events) != 3 {
			t.Fatalf("limit=%d 时返回 %d 条，期望 3 条", limit, len(events))
		}
	}
}

func TestCleanupEvents(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	now := time.Now()

	rows := []chat.Event{
		{Kind: "system", Message: "今天", CreatedAt: now.UnixMilli()},
		{Kind: "system", Message: "8 天前", CreatedAt: now.AddDate(0, 0, -8).UnixMilli()},
		{Kind: "system", Message: "20 天前", CreatedAt: now.AddDate(0, 0, -20).UnixMilli()},
	}
	for _, e := range rows {
		if err := s.AppendEvent(ctx, e); err != nil {
			t.Fatalf("AppendEvent 失败: %v", err)
		}
	}

	deleted, err := s.CleanupEvents(ctx, 14)
	if err != nil {
		t.Fatalf("CleanupEvents 失败: %v", err)
	}
	if deleted != 1 {
		t.Fatalf("删除 %d 条，期望 1 条", deleted)
	}

	events, _, err := s.QueryEvents(ctx, chat.EventQuery{})
	if err != nil {
		t.Fatalf("QueryEvents 失败: %v", err)
	}
	if len(events) != 2 {
		t.Fatalf("剩余 %d 条，期望 2 条", len(events))
	}

	// retainDays=0 表示不清理，防止误配置清空审计日志
	if deleted, err := s.CleanupEvents(ctx, 0); err != nil || deleted != 0 {
		t.Fatalf("retainDays=0 时不应删除任何事件: deleted=%d err=%v", deleted, err)
	}
}

func timePtr(t time.Time) *time.Time {
	return &t
}
