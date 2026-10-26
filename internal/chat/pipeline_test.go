package chat

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"strings"
	"testing"
	"time"

	"qq-bot/internal/config"
	"qq-bot/internal/llm"
	"qq-bot/internal/llm/provider/fake"
	"qq-bot/internal/platform"
)

func testLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

// stubStore 只实现 aiStep 用到的方法，其余方法返回零值。
// 它是 package chat 内部的测试替身；端到端测试用真实 store。
type stubStore struct {
	turns     []Turn
	recentErr error

	events        []Event
	records       []*LLMRequestRecord
	recordTurnIDs []int64
	recordErr     error
}

func (s *stubStore) GetOrCreateConversation(context.Context, platform.ConversationKey) (*Conversation, bool, error) {
	return &Conversation{ID: 1}, true, nil
}
func (s *stubStore) BeginTurn(context.Context, TurnParams) (TurnRef, error) {
	return TurnRef{}, nil
}
func (s *stubStore) FinishTurn(context.Context, int64, string) error { return nil }
func (s *stubStore) FailTurn(context.Context, int64) error           { return nil }
func (s *stubStore) FilterTurn(context.Context, int64) error         { return nil }
func (s *stubStore) RecentTurns(context.Context, int64, int) ([]Turn, error) {
	return s.turns, s.recentErr
}
func (s *stubStore) StaleTurns(context.Context, int64, time.Duration) ([]TurnRef, error) {
	return nil, nil
}
func (s *stubStore) AppendEvent(_ context.Context, e Event) error {
	s.events = append(s.events, e)
	return nil
}
func (s *stubStore) RecordLLMRequest(_ context.Context, rec *LLMRequestRecord, turnID int64) error {
	if s.recordErr != nil {
		return s.recordErr
	}
	cp := *rec
	s.records = append(s.records, &cp)
	s.recordTurnIDs = append(s.recordTurnIDs, turnID)
	return nil
}

type stubStep struct {
	name    string
	handled bool
	err     error
	calls   *[]string
}

func (s *stubStep) Name() string { return s.name }
func (s *stubStep) Process(context.Context, *Request) (bool, error) {
	*s.calls = append(*s.calls, s.name)
	return s.handled, s.err
}

func TestPipeline_StopsOnHandled(t *testing.T) {
	var calls []string
	p := NewPipeline(
		&stubStep{name: "first", handled: true, calls: &calls},
		&stubStep{name: "second", calls: &calls},
	)

	handled, err := p.Run(context.Background(), &Request{})
	if err != nil || !handled {
		t.Fatalf("Run = (%v, %v)，期望 (true, nil)", handled, err)
	}
	if strings.Join(calls, ",") != "first" {
		t.Fatalf("调用链 = %v，期望只执行 first", calls)
	}
}

func TestPipeline_StopsOnErrorAndKeepsSentinel(t *testing.T) {
	sentinel := errors.New("boom")
	var calls []string
	p := NewPipeline(
		&stubStep{name: "first", calls: &calls},
		&stubStep{name: "second", err: sentinel, calls: &calls},
		&stubStep{name: "third", calls: &calls},
	)

	_, err := p.Run(context.Background(), &Request{})
	if !errors.Is(err, sentinel) {
		t.Fatalf("错误 %v 应可用 errors.Is 命中哨兵", err)
	}
	if strings.Join(calls, ",") != "first,second" {
		t.Fatalf("调用链 = %v，期望在 second 中止", calls)
	}
}

func aiTestRequest(cfg config.Config) *Request {
	return &Request{
		In: platform.IncomingMessage{
			Text: "当前问题",
			Conv: platform.ConversationKey{AppID: "app", Scene: platform.SceneC2C, SceneID: "u1"},
		},
		Trace:  "trace-1",
		ConvID: 5,
		Turn:   TurnRef{ID: 9},
		Cfg:    cfg,
		Log:    testLogger(),
	}
}

func aiTestConfig() config.Config {
	return config.Config{
		Chat: config.ChatConfig{ContextTurns: 10},
		LLM: config.LLMConfig{
			Model:        "test-model",
			SystemPrompt: "你是测试助手",
			MaxTokens:    256,
			Temperature:  0.7,
		},
	}
}

func TestAIStep_BuildsRequestFromTurns(t *testing.T) {
	client := fake.New()
	client.SetReply("回答")
	st := &stubStore{turns: []Turn{{Messages: []StoredMessage{
		{Role: "user", Content: "历史问题", CreatedAt: 1},
		{Role: "assistant", Content: "历史回答", CreatedAt: 2},
	}}}}

	cfg := aiTestConfig()
	cfg.Chat.ContextTimePrefix = false
	r := aiTestRequest(cfg)
	step := NewAIStep(client, st, time.UTC)

	handled, err := step.Process(context.Background(), r)
	if err != nil || !handled {
		t.Fatalf("Process = (%v, %v)，期望 (true, nil)", handled, err)
	}
	if r.ReplyText != "回答" {
		t.Fatalf("ReplyText = %q，期望 回答", r.ReplyText)
	}

	calls := client.Calls()
	if len(calls) != 1 {
		t.Fatalf("模型调用次数 = %d，期望 1", len(calls))
	}
	req := calls[0]
	if req.System != "你是测试助手" || req.Model != "test-model" ||
		req.MaxTokens != 256 || req.Temperature != 0.7 {
		t.Fatalf("请求参数不正确: %+v", req)
	}
	if len(req.Tools) != 0 {
		t.Fatalf("V1 不应携带工具，实际 %d 个", len(req.Tools))
	}
	if len(req.Message) != 3 {
		t.Fatalf("消息条数 = %d，期望 3（历史 user + 历史 assistant + 当前 user）", len(req.Message))
	}
	wantRoles := []llm.Role{llm.RoleUser, llm.RoleAssistant, llm.RoleUser}
	for i, m := range req.Message {
		if m.Role != wantRoles[i] {
			t.Fatalf("第 %d 条角色 = %q，期望 %q", i, m.Role, wantRoles[i])
		}
	}
	if req.Message[2].Text != "当前问题" {
		t.Fatalf("最后一条应是当前用户消息，实际 %q", req.Message[2].Text)
	}

	if len(st.records) != 1 {
		t.Fatalf("模型快照条数 = %d，期望 1", len(st.records))
	}
	rec := st.records[0]
	if rec.Status != "success" || rec.Provider != "fake" || rec.Model != "test-model" ||
		rec.ConversationID != 5 || rec.TraceID != "trace-1" ||
		rec.PromptTokens != 1 || rec.CompletionTokens != 1 || rec.TotalTokens != 2 {
		t.Fatalf("模型快照字段不正确: %+v", rec)
	}
	if rec.RawRequest == "" || rec.RawResponse == "" {
		t.Fatal("raw_request / raw_response 应保存完整快照")
	}
	if st.recordTurnIDs[0] != 9 {
		t.Fatalf("快照关联的 turnID = %d，期望 9", st.recordTurnIDs[0])
	}

	if got := eventKinds(st.events); !containsAll(got, "llm_call", "llm_result") {
		t.Fatalf("事件 = %v，期望包含 llm_call/llm_result", got)
	}
}

func TestAIStep_ErrorWritesLlmError(t *testing.T) {
	client := fake.New()
	client.SetError(llm.ErrAuth)
	st := &stubStore{turns: []Turn{}}
	r := aiTestRequest(aiTestConfig())

	handled, err := NewAIStep(client, st, time.UTC).Process(context.Background(), r)
	if handled {
		t.Fatal("模型出错时 handled 应为 false")
	}
	if !errors.Is(err, llm.ErrAuth) {
		t.Fatalf("错误 = %v，期望包裹 llm.ErrAuth", err)
	}
	if got := eventKinds(st.events); !containsAll(got, "llm_call", "llm_error") {
		t.Fatalf("事件 = %v，期望包含 llm_call/llm_error", got)
	}
	if len(st.records) != 1 || st.records[0].Status != "failed" || st.records[0].ErrorMessage == "" {
		t.Fatalf("失败快照不正确: %+v", st.records)
	}
}

func TestAIStep_EmptyReplyIsError(t *testing.T) {
	client := fake.New()
	client.SetReply("  \n\t")
	st := &stubStore{turns: []Turn{}}
	r := aiTestRequest(aiTestConfig())

	handled, err := NewAIStep(client, st, time.UTC).Process(context.Background(), r)
	if handled || !errors.Is(err, errEmptyReply) {
		t.Fatalf("Process = (%v, %v)，期望 false + errEmptyReply", handled, err)
	}
	if r.ReplyText != "" {
		t.Fatalf("空回复不应写入 ReplyText，实际 %q", r.ReplyText)
	}
	if len(st.records) != 1 || st.records[0].Status != "failed" {
		t.Fatalf("空回复快照应为 failed: %+v", st.records)
	}
}

func eventKinds(events []Event) []string {
	out := make([]string, 0, len(events))
	for _, e := range events {
		out = append(out, e.Kind)
	}
	return out
}

func containsAll(got []string, want ...string) bool {
	set := make(map[string]bool, len(got))
	for _, k := range got {
		set[k] = true
	}
	for _, k := range want {
		if !set[k] {
			return false
		}
	}
	return true
}
