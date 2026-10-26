package chat

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
	"unicode/utf8"

	"qq-bot/internal/config"
	"qq-bot/internal/platform"
)

// stubProvider 可以按调用序号注入失败；mock.Provider 做不到这一点。
type stubProvider struct {
	mu    sync.Mutex
	calls []platform.OutgoingMessage
	errs  map[int]error // key 是 1-based 的 Send 调用序号
}

func newStubProvider() *stubProvider {
	return &stubProvider{errs: make(map[int]error)}
}

func (p *stubProvider) Name() string                            { return "stub" }
func (p *stubProvider) Start(context.Context) error             { return nil }
func (p *stubProvider) Stop(context.Context) error              { return nil }
func (p *stubProvider) Status() platform.Status                 { return platform.StatusConnected }
func (p *stubProvider) Events() <-chan platform.IncomingMessage { return nil }
func (p *stubProvider) Send(_ context.Context, msg platform.OutgoingMessage) (platform.SentMessage, error) {
	p.mu.Lock()
	defer p.mu.Unlock()

	p.calls = append(p.calls, msg)
	n := len(p.calls)
	if err := p.errs[n]; err != nil {
		return platform.SentMessage{}, err
	}
	return platform.SentMessage{MsgID: fmt.Sprintf("sent-%d", n), Seq: uint32(n)}, nil
}

func (p *stubProvider) Calls() []platform.OutgoingMessage {
	p.mu.Lock()
	defer p.mu.Unlock()
	return append([]platform.OutgoingMessage(nil), p.calls...)
}

type memSink struct {
	mu     sync.Mutex
	events []Event
}

func (s *memSink) AppendEvent(_ context.Context, e Event) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.events = append(s.events, e)
	return nil
}

func (s *memSink) Events() []Event {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]Event(nil), s.events...)
}

func outboundConfig(maxChars, maxSegments int) config.Config {
	return config.Config{Chat: config.ChatConfig{
		SegmentMaxChars:  maxChars,
		MaxReplySegments: maxSegments,
	}}
}

func countKind(events []Event, kind string) int {
	n := 0
	for _, e := range events {
		if e.Kind == kind {
			n++
		}
	}
	return n
}

func TestOutbound_SplitsAndEmitsPerSegmentEvents(t *testing.T) {
	provider := newStubProvider()
	sink := &memSink{}
	o := NewOutbound(provider, sink, testLogger())
	reply := platform.NewReplyRef("msg-1", "evt-1")
	meta := SendMeta{TraceID: "trace-1", ConvID: 7, TurnID: 9}

	res := o.SendText(context.Background(), c2cKey("u1"), reply,
		strings.Repeat("长", 250), outboundConfig(100, 3), meta)

	if res.Segments != 3 || res.Sent != 3 || res.Err != nil {
		t.Fatalf("SendResult = %+v，期望 3 段全部发出", res)
	}
	calls := provider.Calls()
	if len(calls) != 3 {
		t.Fatalf("Send 调用次数 = %d，期望 3", len(calls))
	}
	for i, msg := range calls {
		if msg.Reply != reply {
			t.Fatalf("第 %d 段没有复用同一个 ReplyRef", i+1)
		}
		if msg.Kind != platform.OutText {
			t.Fatalf("第 %d 段类型 = %q", i+1, msg.Kind)
		}
		if n := utf8.RuneCountInString(msg.Text); n > 100 {
			t.Fatalf("第 %d 段长度 = %d rune", i+1, n)
		}
	}

	events := sink.Events()
	if got := countKind(events, "outbound"); got != 3 {
		t.Fatalf("outbound 事件数 = %d，期望 3", got)
	}
	for _, e := range events {
		if e.TraceID != "trace-1" || e.AppID != "app" {
			t.Fatalf("事件缺少 trace/app: %+v", e)
		}
		if e.ConversationID == nil || *e.ConversationID != 7 ||
			e.TurnID == nil || *e.TurnID != 9 {
			t.Fatalf("事件缺少会话/轮次: %+v", e)
		}
		payload, ok := e.Payload.(map[string]any)
		if !ok || payload["msg_seq"] == nil {
			t.Fatalf("outbound 事件 payload 缺少 msg_seq: %+v", e.Payload)
		}
	}
}

func TestOutbound_FirstSegmentFailureStops(t *testing.T) {
	provider := newStubProvider()
	provider.errs[1] = errors.New("boom")
	sink := &memSink{}
	o := NewOutbound(provider, sink, testLogger())

	res := o.SendText(context.Background(), c2cKey("u1"), platform.NewReplyRef("m", ""),
		strings.Repeat("长", 250), outboundConfig(100, 3), SendMeta{TraceID: "t"})

	if res.Sent != 0 || res.Err == nil || res.QuotaExhausted {
		t.Fatalf("SendResult = %+v，期望首段失败后停止", res)
	}
	if calls := provider.Calls(); len(calls) != 1 {
		t.Fatalf("首段失败后仍调用了 %d 次 Send", len(calls))
	}
	if got := countKind(sink.Events(), "outbound_error"); got != 1 {
		t.Fatalf("outbound_error 事件数 = %d，期望 1", got)
	}
}

func TestOutbound_MiddleFailureContinues(t *testing.T) {
	provider := newStubProvider()
	provider.errs[2] = errors.New("boom")
	sink := &memSink{}
	o := NewOutbound(provider, sink, testLogger())

	// 150 个 rune / 100 上限 = 2 段；第 2 段失败，第 1 段已发出。
	res := o.SendText(context.Background(), c2cKey("u1"), platform.NewReplyRef("m", ""),
		strings.Repeat("长", 150), outboundConfig(100, 3), SendMeta{TraceID: "t"})

	if res.Sent != 1 || res.Err == nil {
		t.Fatalf("SendResult = %+v，期望 1 段成功、错误保留", res)
	}
	if calls := provider.Calls(); len(calls) != 2 {
		t.Fatalf("Send 调用次数 = %d，期望 2", len(calls))
	}
	events := sink.Events()
	if got := countKind(events, "outbound"); got != 1 {
		t.Fatalf("outbound 事件数 = %d，期望 1", got)
	}
	if got := countKind(events, "outbound_error"); got != 1 {
		t.Fatalf("outbound_error 事件数 = %d，期望 1", got)
	}
}

func TestOutbound_QuotaExhaustedStopsRemaining(t *testing.T) {
	provider := newStubProvider()
	provider.errs[2] = fmt.Errorf("qqbot: %w", platform.ErrReplyQuotaExhausted)
	sink := &memSink{}
	o := NewOutbound(provider, sink, testLogger())

	res := o.SendText(context.Background(), c2cKey("u1"), platform.NewReplyRef("m", ""),
		strings.Repeat("长", 250), outboundConfig(100, 3), SendMeta{TraceID: "t"})

	if res.Sent != 1 || !res.QuotaExhausted {
		t.Fatalf("SendResult = %+v，期望 1 段成功 + 配额耗尽", res)
	}
	if calls := provider.Calls(); len(calls) != 2 {
		t.Fatalf("配额耗尽后仍尝试了第 3 段：Send 调用 %d 次", len(calls))
	}
	if !errors.Is(res.Err, platform.ErrReplyQuotaExhausted) {
		t.Fatalf("res.Err = %v，期望命中配额哨兵", res.Err)
	}
}

func TestOutbound_EmptyTextSendsNothing(t *testing.T) {
	provider := newStubProvider()
	sink := &memSink{}
	o := NewOutbound(provider, sink, testLogger())

	res := o.SendText(context.Background(), c2cKey("u1"), platform.NewReplyRef("m", ""),
		"   ", outboundConfig(100, 3), SendMeta{TraceID: "t"})

	if res.Segments != 0 || res.Sent != 0 || res.Err != nil {
		t.Fatalf("SendResult = %+v，期望什么都不发", res)
	}
	if len(provider.Calls()) != 0 || len(sink.Events()) != 0 {
		t.Fatal("空文本不应产生发送或事件")
	}
}

func TestOutbound_NilReplyStillPassedThrough(t *testing.T) {
	provider := newStubProvider()
	o := NewOutbound(provider, &memSink{}, testLogger())

	res := o.SendText(context.Background(), c2cKey("u1"), nil, "hi",
		outboundConfig(100, 3), SendMeta{TraceID: "t"})
	if res.Sent != 1 {
		t.Fatalf("SendResult = %+v", res)
	}
	calls := provider.Calls()
	if len(calls) != 1 || calls[0].Reply != nil {
		t.Fatalf("主动消息的 Reply 应为 nil: %+v", calls)
	}
}
