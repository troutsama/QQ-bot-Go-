package store_test

import (
	"context"
	"database/sql"
	"errors"
	"strings"
	"testing"

	"qq-bot/internal/chat"
)

func TestRecordLLMRequest_WritesSnapshotAndLinksTurn(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	convID := mustConversation(t, s)
	ref := beginTurn(t, s, convID, "m1", "你好")

	rec := &chat.LLMRequestRecord{
		TraceID:          "trace-m1",
		ConversationID:   convID,
		Provider:         "openai-chat",
		Model:            "deepseek-chat",
		Temperature:      0.7,
		PromptTokens:     12,
		CompletionTokens: 34,
		TotalTokens:      46,
		DurationMS:       880,
		Status:           "success",
		RawRequest:       `{"messages":[{"role":"user","content":"你好"}]}`,
		RawResponse:      `{"choices":[{"message":{"content":"你好呀"}}]}`,
	}
	if err := s.RecordLLMRequest(ctx, rec, ref.ID); err != nil {
		t.Fatalf("RecordLLMRequest 失败: %v", err)
	}
	if rec.ID == 0 {
		t.Fatal("RecordLLMRequest 应回填记录 ID")
	}

	var llmID sql.NullInt64
	if err := s.DB().QueryRowContext(ctx,
		`SELECT llm_request_id FROM turns WHERE id = ?`, ref.ID,
	).Scan(&llmID); err != nil {
		t.Fatalf("读取轮次失败: %v", err)
	}
	if !llmID.Valid || llmID.Int64 != rec.ID {
		t.Fatalf("turns.llm_request_id = %v，期望 %d", llmID, rec.ID)
	}

	got, err := s.GetLLMRequest(ctx, rec.ID)
	if err != nil {
		t.Fatalf("GetLLMRequest 失败: %v", err)
	}
	if got.TraceID != rec.TraceID || got.ConversationID != rec.ConversationID ||
		got.Provider != rec.Provider || got.Model != rec.Model ||
		got.Temperature != rec.Temperature ||
		got.PromptTokens != rec.PromptTokens ||
		got.CompletionTokens != rec.CompletionTokens ||
		got.TotalTokens != rec.TotalTokens ||
		got.DurationMS != rec.DurationMS ||
		got.Status != rec.Status ||
		got.RawRequest != rec.RawRequest ||
		got.RawResponse != rec.RawResponse {
		t.Fatalf("读回的记录与写入不一致:\n got=%+v\nwant=%+v", got, rec)
	}
}

func TestRecordLLMRequest_PreservesLongRawJSON(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	convID := mustConversation(t, s)
	ref := beginTurn(t, s, convID, "m1", "你好")

	rawRequest := `{"messages":[{"role":"user","content":"` + strings.Repeat("长", 50000) + `"}]}`
	rawResponse := `{"choices":[{"message":{"content":"` + strings.Repeat("答", 50000) + `"}}]}`

	rec := &chat.LLMRequestRecord{
		TraceID:     "trace-m1",
		Provider:    "fake",
		Model:       "fake",
		Status:      "success",
		RawRequest:  rawRequest,
		RawResponse: rawResponse,
	}
	if err := s.RecordLLMRequest(ctx, rec, ref.ID); err != nil {
		t.Fatalf("RecordLLMRequest 失败: %v", err)
	}

	got, err := s.GetLLMRequest(ctx, rec.ID)
	if err != nil {
		t.Fatalf("GetLLMRequest 失败: %v", err)
	}
	if got.RawRequest != rawRequest {
		t.Fatalf("raw_request 被改动：len=%d，期望 %d", len(got.RawRequest), len(rawRequest))
	}
	if got.RawResponse != rawResponse {
		t.Fatalf("raw_response 被改动：len=%d，期望 %d", len(got.RawResponse), len(rawResponse))
	}
}

func TestRecordLLMRequest_NilRecord(t *testing.T) {
	s := newTestStore(t)
	if err := s.RecordLLMRequest(context.Background(), nil, 1); err == nil {
		t.Fatal("record 为 nil 时应报错")
	}
}

func TestGetLLMRequest_NotFound(t *testing.T) {
	s := newTestStore(t)
	_, err := s.GetLLMRequest(context.Background(), 99999)
	if !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("不存在的记录应返回 sql.ErrNoRows，实际: %v", err)
	}
}
