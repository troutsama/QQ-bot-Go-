package store

import (
	"database/sql"
	"errors"
	"log/slog"
	"path/filepath"
	"strings"
	"testing"

	"qq-bot/internal/config"
)

// TestEventHandlerWritesKindEvents 验证 03 §5.7 的双轨规则：
// 带 kind 的记录进 events 表，不带 kind 的运行日志只留在文件里。
func TestEventHandlerWritesKindEvents(t *testing.T) {
	st, err := Open(config.DatabaseConfig{
		Driver:        "sqlite",
		Path:          filepath.Join(t.TempDir(), "event_test.db"),
		MaxOpenConns:  1,
		MaxIdleConns:  1,
		BusyTimeoutMS: 3000,
		InitSchema:    true,
	})
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer st.Close()

	log := slog.New(NewEventHandler(st)).With("service", "botd")

	log.Info("收到消息",
		"kind", "inbound",
		"msg_id", "m-1",
		"turn_id", int64(7),
		"text", "你好",
	)
	log.Info("普通运行日志，不该入库")
	log.Error("回显失败",
		"kind", "outbound_error",
		"error", errors.New("boom"),
		"msg_id", "m-2",
	)

	var count int
	if err := st.DB().QueryRow(`SELECT COUNT(*) FROM events`).Scan(&count); err != nil {
		t.Fatalf("统计事件: %v", err)
	}
	if count != 2 {
		t.Fatalf("events 应有 2 条（inbound + outbound_error），实际 %d", count)
	}

	var (
		kind, level, message, payload string
		msgID                         string
		turnID                        sql.NullInt64
	)
	if err := st.DB().QueryRow(
		`SELECT kind, level, message, payload, platform_msg_id, turn_id
		   FROM events WHERE kind = 'inbound'`).
		Scan(&kind, &level, &message, &payload, &msgID, &turnID); err != nil {
		t.Fatalf("查询 inbound 事件: %v", err)
	}
	if level != "info" || message != "收到消息" {
		t.Errorf("level/message 不对: %q / %q", level, message)
	}
	if msgID != "m-1" {
		t.Errorf("platform_msg_id 应为 m-1，实际 %q", msgID)
	}
	if !turnID.Valid || turnID.Int64 != 7 {
		t.Errorf("turn_id 应为 7，实际 %+v", turnID)
	}

	var errPayload string
	if err := st.DB().QueryRow(
		`SELECT payload FROM events WHERE kind = 'outbound_error'`).Scan(&errPayload); err != nil {
		t.Fatalf("查询 outbound_error 事件: %v", err)
	}
	// error 值必须落到 payload 的文本里，而不是变成 {}
	if want := `"error":"boom"`; !strings.Contains(errPayload, want) {
		t.Errorf("payload 应包含 %s，实际 %s", want, errPayload)
	}
}
