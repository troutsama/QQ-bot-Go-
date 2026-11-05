package store_test

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"qq-bot/internal/config"
	"qq-bot/internal/platform"
	"qq-bot/internal/store"
)

func channelStore(t *testing.T, init bool) (*store.Store, config.DatabaseConfig) {
	t.Helper()
	cfg := config.DatabaseConfig{Driver: "sqlite", Path: filepath.Join(t.TempDir(), "channel.db"), MaxOpenConns: 1, MaxIdleConns: 1, InitSchema: init}
	s, err := store.Open(cfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	return s, cfg
}

func channelRecord() platform.ChannelMessage {
	return platform.ChannelMessage{Provider: "qqbot", Conv: platform.ConversationKey{AppID: "app", Scene: platform.SceneC2C, SceneID: "u1"}, Direction: platform.ChannelIncoming, MsgID: "in-1", SenderOpenID: "u1", Kind: "message", Text: "你好，'SQL' ?", HasAttachment: true, OccurredAt: time.UnixMilli(1700000000123)}
}

func TestChannelMessageFieldsAndDeduplication(t *testing.T) {
	s, _ := channelStore(t, false)
	msg := channelRecord()
	for i := 0; i < 2; i++ {
		inserted, err := s.RecordChannelMessage(context.Background(), msg)
		if err != nil || inserted != (i == 0) {
			t.Fatalf("insert %d: inserted=%v err=%v", i, inserted, err)
		}
	}
	var text, sender, kind string
	var attachment bool
	var occurred, recorded int64
	if err := s.DB().QueryRow(`SELECT content, sender_openid, kind, has_attachment, occurred_at, recorded_at FROM channel_messages`).Scan(&text, &sender, &kind, &attachment, &occurred, &recorded); err != nil {
		t.Fatal(err)
	}
	if text != "你好，'SQL' ?" || sender != "u1" || kind != "message" || !attachment || occurred != 1700000000123 || recorded <= 0 {
		t.Fatalf("wrong record: %q %q %q %v %d %d", text, sender, kind, attachment, occurred, recorded)
	}
	// 同一平台 ID 在不同方向、会话或 App 下不能互相去重。
	for _, change := range []func(*platform.ChannelMessage){
		func(m *platform.ChannelMessage) { m.Direction = platform.ChannelOutgoing },
		func(m *platform.ChannelMessage) { m.Conv.SceneID = "u2" },
		func(m *platform.ChannelMessage) { m.Conv.AppID = "app2" },
		func(m *platform.ChannelMessage) { m.Provider = "other" },
	} {
		m := msg
		change(&m)
		if inserted, err := s.RecordChannelMessage(context.Background(), m); err != nil || !inserted {
			t.Fatalf("distinct record: inserted=%v err=%v", inserted, err)
		}
	}
	var count int
	if err := s.DB().QueryRow(`SELECT COUNT(*) FROM channel_messages`).Scan(&count); err != nil || count != 5 {
		t.Fatalf("count=%d err=%v", count, err)
	}
}

func TestChannelMessageReplyMetadata(t *testing.T) {
	s, _ := channelStore(t, false)
	m := channelRecord()
	m.Direction = platform.ChannelOutgoing
	m.MsgID = "out-1"
	m.ReplyToMsgID = "in-1"
	m.ReplyToEventID = "event-1"
	m.Seq = 2
	m.Kind = "text"
	if _, err := s.RecordChannelMessage(context.Background(), m); err != nil {
		t.Fatal(err)
	}
	var reply, event string
	var seq int
	if err := s.DB().QueryRow(`SELECT reply_to_msg_id, reply_to_event_id, reply_seq FROM channel_messages`).Scan(&reply, &event, &seq); err != nil {
		t.Fatal(err)
	}
	if reply != "in-1" || event != "event-1" || seq != 2 {
		t.Fatalf("reply=%q event=%q seq=%d", reply, event, seq)
	}
}

func TestChannelMessageErrorsAreNotDuplicates(t *testing.T) {
	s, _ := channelStore(t, false)
	if _, err := s.DB().Exec(`DROP TABLE channel_messages`); err != nil {
		t.Fatal(err)
	}
	if inserted, err := s.RecordChannelMessage(context.Background(), channelRecord()); err == nil || inserted {
		t.Fatalf("missing table treated as duplicate: inserted=%v err=%v", inserted, err)
	}
}

func TestChannelMessageReopenPreservesBusinessTables(t *testing.T) {
	s, cfg := channelStore(t, true)
	if _, err := s.DB().Exec(`INSERT INTO users(app_id, openid, created_at, updated_at) VALUES ('app', 'existing', 1, 1)`); err != nil {
		t.Fatal(err)
	}
	if _, err := s.DB().Exec(`DROP TABLE channel_messages`); err != nil {
		t.Fatal(err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	cfg.InitSchema = false
	reopened, err := store.Open(cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	if _, err := reopened.RecordChannelMessage(context.Background(), channelRecord()); err != nil {
		t.Fatal(err)
	}
	var users, turns int
	if err := reopened.DB().QueryRow(`SELECT COUNT(*) FROM users`).Scan(&users); err != nil {
		t.Fatal(err)
	}
	if err := reopened.DB().QueryRow(`SELECT COUNT(*) FROM turns`).Scan(&turns); err != nil {
		t.Fatal(err)
	}
	if users != 1 || turns != 0 {
		t.Fatalf("business tables modified: users=%d turns=%d", users, turns)
	}
}

func TestChannelMessageRejectsMissingIdentity(t *testing.T) {
	s, _ := channelStore(t, false)
	for _, change := range []func(*platform.ChannelMessage){
		func(m *platform.ChannelMessage) { m.MsgID = "" },
		func(m *platform.ChannelMessage) { m.Conv.AppID = "" },
		func(m *platform.ChannelMessage) { m.Direction = "invalid" },
	} {
		m := channelRecord()
		change(&m)
		if _, err := s.RecordChannelMessage(context.Background(), m); err == nil {
			t.Fatal("accepted invalid identity")
		}
	}
}
