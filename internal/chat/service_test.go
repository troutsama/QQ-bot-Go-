package chat

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"reflect"
	"testing"
	"time"

	"qq-bot/internal/config"
	"qq-bot/internal/llm"
	"qq-bot/internal/platform"
)

type testTransport struct {
	events chan platform.IncomingMessage
	sent   chan platform.OutgoingMessage
}

func (p *testTransport) Events() <-chan platform.IncomingMessage { return p.events }
func (p *testTransport) Send(ctx context.Context, msg platform.OutgoingMessage) (platform.SentMessage, error) {
	select {
	case p.sent <- msg:
		return platform.SentMessage{}, nil
	case <-ctx.Done():
		return platform.SentMessage{}, ctx.Err()
	}
}

type runnerFunc func(context.Context, llm.Request) (llm.Response, error)

func (f runnerFunc) Run(ctx context.Context, req llm.Request) (llm.Response, error) {
	return f(ctx, req)
}

func newTestService(t *testing.T, runner runnerFunc) (*Service, *testTransport) {
	t.Helper()
	p := &testTransport{events: make(chan platform.IncomingMessage, 20), sent: make(chan platform.OutgoingMessage, 20)}
	s, err := New(p, runner, config.LLMConfig{Model: "test", Timeout: "2s", SystemPrompt: "中文回答"}, config.ChatConfig{
		ContextTurns: 2, MaxReplySegments: 3, SegmentMaxChars: 100,
	}, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatal(err)
	}
	return s, p
}

func incoming(id, sceneID, text string) platform.IncomingMessage {
	return platform.IncomingMessage{MsgID: id, Text: text, Conv: platform.ConversationKey{AppID: "app", Scene: platform.SceneC2C, SceneID: sceneID}}
}

func receive[T any](t *testing.T, ch <-chan T) T {
	t.Helper()
	select {
	case value := <-ch:
		return value
	case <-time.After(3 * time.Second):
		t.Fatal("等待超时")
		var zero T
		return zero
	}
}

func TestServiceHistoryIsolationAndDeduplication(t *testing.T) {
	calls := make(chan llm.Request, 10)
	s, p := newTestService(t, func(_ context.Context, req llm.Request) (llm.Response, error) {
		calls <- req
		return llm.Response{Text: "答:" + req.Messages[len(req.Messages)-1].Text}, nil
	})
	p.events <- incoming("one", "a", "第一问")
	p.events <- incoming("one", "a", "第一问")
	p.events <- incoming("two", "a", "第二问")
	p.events <- incoming("one", "b", "另一个人")
	close(p.events)
	if err := s.Run(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(calls) != 3 || len(p.sent) != 3 {
		t.Fatalf("calls=%d sent=%d", len(calls), len(p.sent))
	}
	for len(calls) > 0 {
		req := <-calls
		last := req.Messages[len(req.Messages)-1].Text
		if last == "第二问" {
			want := []llm.Message{{Role: llm.RoleUser, Text: "第一问"}, {Role: llm.RoleAssistant, Text: "答:第一问"}, {Role: llm.RoleUser, Text: "第二问"}}
			if !reflect.DeepEqual(req.Messages, want) {
				t.Fatalf("历史=%+v", req.Messages)
			}
		} else if len(req.Messages) != 1 {
			t.Fatalf("会话串线: %+v", req.Messages)
		}
	}
	for len(p.sent) > 0 {
		msg := <-p.sent
		if msg.Reply == nil || msg.Reply.MsgID == "" || msg.Kind != platform.OutText {
			t.Fatalf("缺少 QQ 被动回复凭据: %+v", msg)
		}
	}
}

func TestServiceFailureDoesNotEnterHistory(t *testing.T) {
	s, p := newTestService(t, func(_ context.Context, req llm.Request) (llm.Response, error) {
		if req.Messages[len(req.Messages)-1].Text == "失败" {
			return llm.Response{}, errors.New("sensitive upstream detail")
		}
		if len(req.Messages) != 1 {
			t.Errorf("失败轮次不应进入历史: %+v", req.Messages)
		}
		return llm.Response{Text: "成功"}, nil
	})
	p.events <- incoming("1", "a", "失败")
	p.events <- incoming("2", "a", "重试")
	close(p.events)
	if err := s.Run(context.Background()); err != nil {
		t.Fatal(err)
	}
	if msg := <-p.sent; msg.Text != "暂时无法生成回复，请稍后再试。" {
		t.Fatalf("错误提示=%q", msg.Text)
	}
	if msg := <-p.sent; msg.Text != "成功" {
		t.Fatalf("后续消息=%q", msg.Text)
	}
}

func TestServiceCancelsInflightRequest(t *testing.T) {
	started := make(chan struct{})
	s, p := newTestService(t, func(ctx context.Context, _ llm.Request) (llm.Response, error) {
		close(started)
		<-ctx.Done()
		return llm.Response{}, ctx.Err()
	})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- s.Run(ctx) }()
	p.events <- incoming("1", "a", "等待")
	receive(t, started)
	cancel()
	if err := receive(t, done); err != nil {
		t.Fatal(err)
	}
	if len(p.sent) != 0 {
		t.Fatal("停机后不应回复")
	}
}

func TestServiceFullShardDoesNotBlockOtherConversations(t *testing.T) {
	started := make(chan struct{})
	s, p := newTestService(t, func(ctx context.Context, req llm.Request) (llm.Response, error) {
		text := req.Messages[len(req.Messages)-1].Text
		if text == "block" {
			close(started)
			<-ctx.Done()
			return llm.Response{}, ctx.Err()
		}
		return llm.Response{Text: text}, nil
	})
	p.events = make(chan platform.IncomingMessage, 100)
	s.timeout = 10 * time.Second
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- s.Run(ctx) }()
	p.events <- incoming("block", "a", "block")
	receive(t, started)
	for i := 0; i < 65; i++ {
		p.events <- incoming("duplicate", "a", "queued")
	}
	p.events <- incoming("fast", "b", "fast")
	for {
		msg := receive(t, p.sent)
		if msg.Text == "fast" {
			break
		}
	}
	cancel()
	if err := receive(t, done); err != nil {
		t.Fatal(err)
	}
}

func TestSplitReplyUsesRunesAndLimitsSegments(t *testing.T) {
	got := splitReply("你好世界长沙晴天啊", 4, 2)
	want := []string{"你好世界", "长沙晴…"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got=%q want=%q", got, want)
	}
}

func TestServiceSegmentsShareReplyRef(t *testing.T) {
	s, p := newTestService(t, func(context.Context, llm.Request) (llm.Response, error) {
		return llm.Response{Text: "你好世界长沙晴天啊"}, nil
	})
	s.chatCfg.SegmentMaxChars = 4
	s.chatCfg.MaxReplySegments = 2
	p.events <- incoming("1", "a", "问题")
	close(p.events)
	if err := s.Run(context.Background()); err != nil {
		t.Fatal(err)
	}
	first, second := <-p.sent, <-p.sent
	if first.Text != "你好世界" || second.Text != "长沙晴…" || first.Reply != second.Reply {
		t.Fatalf("分段结果: %+v %+v", first, second)
	}
}
