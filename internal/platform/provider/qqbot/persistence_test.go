package qqbot

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/tencent-connect/botgo/dto"
	"github.com/tencent-connect/botgo/openapi"
	"github.com/tencent-connect/botgo/openapi/options"

	"qq-bot/internal/config"
	"qq-bot/internal/platform"
	"qq-bot/internal/store"
)

// 仅替换外部 QQ HTTP 调用，转换、SQL 和 Events 都使用真实实现。
type channelAPI struct {
	openapi.OpenAPI
	post  func(context.Context, string, dto.APIMessage) (*dto.Message, error)
	calls int
}

func (a *channelAPI) PostC2CMessage(ctx context.Context, user string, msg dto.APIMessage, _ ...options.Option) (*dto.Message, error) {
	a.calls++
	if a.post != nil {
		return a.post(ctx, user, msg)
	}
	return &dto.Message{ID: fmt.Sprintf("out-%d", a.calls)}, nil
}

func channelProvider(t *testing.T) (*Provider, *store.Store, *channelAPI) {
	t.Helper()
	s, err := store.Open(config.DatabaseConfig{Driver: "sqlite", Path: filepath.Join(t.TempDir(), "qq.db"), MaxOpenConns: 1, MaxIdleConns: 1, InitSchema: true})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	p, err := New(config.BotConfig{Provider: "qqbot", QQBot: config.QQBotConfig{AppID: "app", AppSecret: "test-only"}}, slog.New(slog.NewTextHandler(io.Discard, nil)), s)
	if err != nil {
		t.Fatal(err)
	}
	a := &channelAPI{}
	p.api = a
	p.setStatus(platform.StatusConnected)
	return p, s, a
}

func inboundData() *dto.WSC2CMessageData {
	return &dto.WSC2CMessageData{ID: "in-1", Author: &dto.User{ID: "u1"}, Content: "你好", Timestamp: "2024-01-02T03:04:05Z"}
}

func outgoingData() platform.OutgoingMessage {
	return platform.OutgoingMessage{Conv: platform.ConversationKey{AppID: "app", Scene: platform.SceneC2C, SceneID: "u1"}, Kind: platform.OutText, Text: "你说的是：你好", Reply: platform.NewReplyRef("in-1", "event-1")}
}

func channelCount(t *testing.T, s *store.Store, query string) int {
	t.Helper()
	var n int
	if err := s.DB().QueryRow(query).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

func TestChannelReceivePersistsBeforeDelivery(t *testing.T) {
	p, s, _ := channelProvider(t)
	if err := p.onC2CMessage(nil, inboundData()); err != nil {
		t.Fatal(err)
	}
	select {
	case msg := <-p.Events():
		if msg.Text != "你好" || msg.MsgID != "in-1" {
			t.Fatalf("wrong event: %+v", msg)
		}
	default:
		t.Fatal("event not delivered to external consumer")
	}
	var direction, app, scene, sceneID, id, sender, content string
	var occurred int64
	if err := s.DB().QueryRow(`SELECT direction, app_id, scene, scene_id, msg_id, sender_openid, content, occurred_at FROM channel_messages`).Scan(&direction, &app, &scene, &sceneID, &id, &sender, &content, &occurred); err != nil {
		t.Fatal(err)
	}
	if direction != "incoming" || app != "app" || scene != "c2c" || sceneID != "u1" || id != "in-1" || sender != "u1" || content != "你好" || occurred != 1704164645000 {
		t.Fatalf("wrong inbound record: %q %q %q %q %q %q %q %d", direction, app, scene, sceneID, id, sender, content, occurred)
	}
	if n := channelCount(t, s, `SELECT COUNT(*) FROM turns`); n != 0 {
		t.Fatalf("channel created %d business turns", n)
	}
}

func TestChannelReceiveConcurrentDuplicatesAndDistinctMessages(t *testing.T) {
	p, s, _ := channelProvider(t)
	var wg sync.WaitGroup
	errs := make(chan error, 8)
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() { defer wg.Done(); errs <- p.onC2CMessage(nil, inboundData()) }()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}
	data := inboundData()
	data.ID = "in-2"
	if err := p.onC2CMessage(nil, data); err != nil {
		t.Fatal(err)
	}
	if n := channelCount(t, s, `SELECT COUNT(*) FROM channel_messages`); n != 2 {
		t.Fatalf("records=%d, want 2", n)
	}
	if n := len(p.events); n != 2 {
		t.Fatalf("delivered=%d, want 2", n)
	}
}

func TestChannelReceiveAttachmentWithoutText(t *testing.T) {
	p, s, _ := channelProvider(t)
	data := inboundData()
	data.Content = ""
	data.Attachments = []*dto.MessageAttachment{{URL: "https://example.invalid/pic.png", ContentType: "image/png"}}
	if err := p.onC2CMessage(nil, data); err != nil {
		t.Fatal(err)
	}
	if n := channelCount(t, s, `SELECT COUNT(*) FROM channel_messages WHERE has_attachment = 1 AND content = ''`); n != 1 {
		t.Fatalf("attachment records=%d", n)
	}
	if msg := <-p.Events(); !msg.HasAttachment || msg.Text != "" {
		t.Fatalf("wrong attachment event: %+v", msg)
	}
}

func TestChannelReceiveSQLFailureDoesNotDeliver(t *testing.T) {
	p, s, _ := channelProvider(t)
	if _, err := s.DB().Exec(`DROP TABLE channel_messages`); err != nil {
		t.Fatal(err)
	}
	if err := p.onC2CMessage(nil, inboundData()); err == nil {
		t.Fatal("SQL error swallowed")
	}
	if len(p.events) != 0 {
		t.Fatal("delivered unrecorded event")
	}
}

func TestChannelReceiveInvalidAndStoppedEvents(t *testing.T) {
	p, s, _ := channelProvider(t)
	if err := p.onC2CMessage(nil, nil); err != nil {
		t.Fatal(err)
	}
	if err := p.Stop(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := p.onC2CMessage(nil, inboundData()); err != nil {
		t.Fatal(err)
	}
	if n := channelCount(t, s, `SELECT COUNT(*) FROM channel_messages`); n != 0 {
		t.Fatalf("recorded %d invalid/stopped events", n)
	}
}

func TestChannelSendPersistsReplyAndSequence(t *testing.T) {
	p, s, api := channelProvider(t)
	msg := outgoingData()
	api.post = func(_ context.Context, user string, payload dto.APIMessage) (*dto.Message, error) {
		m, ok := payload.(dto.MessageToCreate)
		if !ok || user != "u1" || m.Content != "你说的是：你好" || m.MsgID != "in-1" || m.EventID != "event-1" || m.MsgSeq != uint32(api.calls) {
			t.Fatalf("wrong QQ payload: user=%s payload=%+v", user, payload)
		}
		return &dto.Message{ID: fmt.Sprintf("out-%d", api.calls)}, nil
	}
	for i := 1; i <= 2; i++ {
		sent, err := p.Send(context.Background(), msg)
		if err != nil || sent.MsgID != fmt.Sprintf("out-%d", i) || sent.Seq != uint32(i) {
			t.Fatalf("send %d: %+v %v", i, sent, err)
		}
	}
	if n := channelCount(t, s, `SELECT COUNT(*) FROM channel_messages WHERE direction='outgoing' AND reply_to_msg_id='in-1' AND reply_to_event_id='event-1' AND reply_seq IN (1, 2) AND content='你说的是：你好' AND kind='text'`); n != 2 {
		t.Fatalf("reply records=%d", n)
	}
	if n := channelCount(t, s, `SELECT COUNT(*) FROM turns`); n != 0 {
		t.Fatalf("Send created %d turns", n)
	}
}

func TestChannelSendActiveAndTyping(t *testing.T) {
	p, s, _ := channelProvider(t)
	for _, kind := range []platform.OutKind{platform.OutText, platform.OutTyping} {
		msg := outgoingData()
		msg.Reply = nil
		msg.Kind = kind
		if _, err := p.Send(context.Background(), msg); err != nil {
			t.Fatal(err)
		}
	}
	if n := channelCount(t, s, `SELECT COUNT(*) FROM channel_messages WHERE direction='outgoing' AND reply_to_msg_id='' AND reply_seq=0`); n != 2 {
		t.Fatalf("active records=%d", n)
	}
	if n := channelCount(t, s, `SELECT COUNT(*) FROM channel_messages WHERE kind='typing'`); n != 1 {
		t.Fatalf("typing records=%d", n)
	}
}

func TestChannelSendAPIFailureDoesNotRecord(t *testing.T) {
	p, s, api := channelProvider(t)
	apiErr := errors.New("QQ unavailable")
	api.post = func(context.Context, string, dto.APIMessage) (*dto.Message, error) { return nil, apiErr }
	sent, err := p.Send(context.Background(), outgoingData())
	if !errors.Is(err, apiErr) || errors.Is(err, platform.ErrSendUnrecorded) || sent.MsgID != "" {
		t.Fatalf("wrong send failure: %+v %v", sent, err)
	}
	if n := channelCount(t, s, `SELECT COUNT(*) FROM channel_messages`); n != 0 {
		t.Fatalf("failed sends recorded=%d", n)
	}
}

func TestChannelSendSQLFailurePreservesReceipt(t *testing.T) {
	p, s, api := channelProvider(t)
	if _, err := s.DB().Exec(`CREATE TRIGGER reject_outbound BEFORE INSERT ON channel_messages WHEN NEW.direction='outgoing' BEGIN SELECT RAISE(ABORT, 'test SQL failure'); END`); err != nil {
		t.Fatal(err)
	}
	sent, err := p.Send(context.Background(), outgoingData())
	if !errors.Is(err, platform.ErrSendUnrecorded) || sent.MsgID != "out-1" || sent.Seq != 1 {
		t.Fatalf("lost sent result: %+v %v", sent, err)
	}
	if api.calls != 1 {
		t.Fatalf("unexpected resend: %d calls", api.calls)
	}
	if n := channelCount(t, s, `SELECT COUNT(*) FROM channel_messages`); n != 0 {
		t.Fatalf("SQL failure recorded=%d", n)
	}
}

func TestChannelSendCompletedResultSurvivesCancellation(t *testing.T) {
	p, s, api := channelProvider(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	api.post = func(context.Context, string, dto.APIMessage) (*dto.Message, error) {
		cancel()
		return &dto.Message{ID: "completed"}, nil
	}
	sent, err := p.Send(ctx, outgoingData())
	if err != nil || sent.MsgID != "completed" {
		t.Fatalf("completed send lost after cancellation: %+v %v", sent, err)
	}
	if n := channelCount(t, s, `SELECT COUNT(*) FROM channel_messages WHERE msg_id='completed'`); n != 1 {
		t.Fatalf("completed records=%d", n)
	}
}

func TestChannelSendMalformedSuccessIsExplicit(t *testing.T) {
	for _, response := range []*dto.Message{nil, {}} {
		t.Run(fmt.Sprintf("%v", response), func(t *testing.T) {
			p, _, api := channelProvider(t)
			api.post = func(context.Context, string, dto.APIMessage) (*dto.Message, error) { return response, nil }
			if _, err := p.Send(context.Background(), outgoingData()); !errors.Is(err, platform.ErrSendUnrecorded) {
				t.Fatalf("ambiguous API success: %v", err)
			}
		})
	}
}

func TestChannelSendRejectsInvalidOrStoppedCalls(t *testing.T) {
	p, s, api := channelProvider(t)
	msg := outgoingData()
	msg.Conv.Scene = platform.SceneGroup
	if _, err := p.Send(context.Background(), msg); !errors.Is(err, platform.ErrUnsupportedScene) {
		t.Fatalf("unsupported scene: %v", err)
	}
	msg = outgoingData()
	msg.Kind = "invalid"
	if _, err := p.Send(context.Background(), msg); err == nil {
		t.Fatal("invalid kind accepted")
	}
	if err := p.Stop(context.Background()); err != nil {
		t.Fatal(err)
	}
	if _, err := p.Send(context.Background(), outgoingData()); !errors.Is(err, platform.ErrNotConnected) {
		t.Fatalf("stopped send: %v", err)
	}
	if api.calls != 0 || channelCount(t, s, `SELECT COUNT(*) FROM channel_messages`) != 0 {
		t.Fatal("invalid sends reached API or storage")
	}
}

func TestChannelNewRequiresStore(t *testing.T) {
	_, err := New(config.BotConfig{QQBot: config.QQBotConfig{AppID: "app", AppSecret: "test-only"}}, nil, nil)
	if err == nil {
		t.Fatal("nil Store accepted")
	}
}

// 使用超时 context 证明失败路径不会依赖真实网络或无限等待。
func TestChannelReceiveCanceledContextDoesNotDeliver(t *testing.T) {
	p, s, _ := channelProvider(t)
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	cancel()
	p.ctx = ctx
	if err := p.onC2CMessage(nil, inboundData()); err == nil {
		t.Fatal("canceled receive context ignored")
	}
	if len(p.events) != 0 || channelCount(t, s, `SELECT COUNT(*) FROM channel_messages`) != 0 {
		t.Fatal("canceled event processed")
	}
}
