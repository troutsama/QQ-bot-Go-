package store

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"qq-bot/internal/chat"
)

// AppendEvent 把一条业务事件写进 events 表（admin API 的数据源，03 §4.2）。
func (s *Store) AppendEvent(ctx context.Context, e chat.Event) error {
	createdAt := e.CreatedAt
	if createdAt == 0 {
		createdAt = time.Now().UnixMilli()
	}

	// payload 在 chat.Event 里是 any，落库前统一转成 JSON 字符串。
	payload := ""
	if e.Payload != nil {
		b, err := json.Marshal(e.Payload)
		if err != nil {
			return fmt.Errorf("store: 序列化事件载荷: %w", err)
		}
		payload = string(b)
	}

	// ConversationID / TurnID 是 *int64：nil 会由 database/sql 写成 NULL，
	// 正好对上 events 表这两列可空的定义。
	if _, err := s.db.ExecContext(ctx,
		`INSERT INTO events
		    (created_at, level, kind, app_id, trace_id, conversation_id, turn_id, platform_msg_id, message, payload)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		createdAt, e.Level, e.Kind, e.AppID, e.TraceID,
		e.ConversationID, e.TurnID, e.PlatformMsgID, e.Message, payload); err != nil {
		return fmt.Errorf("store: 插入事件: %w", err)
	}
	return nil
}

// EventHandler 是一个 slog.Handler：把**带 kind 属性的日志记录**写成 events 行。
//
// 为什么只落带 kind 的记录（03 §5.7「日志双轨」）：
// 运行日志（启动、重连、botgo 内部日志）数量大、只用于排障，留在文件里；
// 能进 events 表的必须是可被 admin API 按 kind 查询的业务事件。
// 所以调用方想让一条日志进库，就在参数里加 "kind", "inbound" 这样的属性；
// 不加的记录只走文件，不会污染事件表。
//
// 用法见 cmd/botd/main.go：logx.Setup(..., store.NewEventHandler(st))。
type EventHandler struct {
	st *Store

	// With / WithGroup 累积的上下文，Handle 时一并参与映射。
	attrs  []slog.Attr
	groups []string
}

var _ slog.Handler = (*EventHandler)(nil)

func NewEventHandler(st *Store) *EventHandler { return &EventHandler{st: st} }

// Enabled 放行所有级别：文件侧的级别过滤由 logx 里的 TextHandler 负责，
// 这里再拦一次会让 cfg.log.level=info 时丢掉 warn 以下的业务事件。
func (h *EventHandler) Enabled(context.Context, slog.Level) bool { return true }

func (h *EventHandler) WithAttrs(attrs []slog.Attr) slog.Handler {
	if len(attrs) == 0 {
		return h
	}
	c := *h
	c.attrs = append(append([]slog.Attr(nil), h.attrs...), attrs...)
	return &c
}

func (h *EventHandler) WithGroup(name string) slog.Handler {
	if name == "" {
		return h
	}
	c := *h
	c.groups = append(append([]string(nil), h.groups...), name)
	return &c
}

func (h *EventHandler) Handle(ctx context.Context, r slog.Record) error {
	e := &chat.Event{
		Level:     levelName(r.Level),
		Message:   r.Message,
		CreatedAt: r.Time.UnixMilli(),
	}
	payload := map[string]any{}

	apply := func(a slog.Attr) bool {
		applyAttr(e, payload, a, h.groups)
		return true
	}
	for _, a := range h.attrs {
		apply(a)
	}
	r.Attrs(apply)

	// 没有 kind 说明不是业务事件，只留在文件里。
	if e.Kind == "" {
		return nil
	}
	if len(payload) > 0 {
		e.Payload = payload
	}
	return h.st.AppendEvent(ctx, *e)
}

// applyAttr 把一条日志属性拆到事件的固定列或 payload 里。
// 固定列的键名与 events 表一致，其余属性原样进 payload（JSON）。
func applyAttr(e *chat.Event, payload map[string]any, a slog.Attr, groups []string) {
	v := a.Value.Resolve()
	key := a.Key
	if key == "" && v.Kind() == slog.KindAny && v.Any() == nil {
		return // 空属性（slog.Attr{}），跳过
	}
	if len(groups) > 0 {
		key = strings.Join(groups, ".") + "." + key
	}

	switch key {
	case "kind":
		e.Kind = v.String()
	case "app_id":
		e.AppID = v.String()
	case "trace_id":
		e.TraceID = v.String()
	case "msg_id":
		e.PlatformMsgID = v.String()
	case "conversation_id":
		if n, ok := asInt64(v); ok {
			e.ConversationID = &n
		}
	case "turn_id":
		if n, ok := asInt64(v); ok {
			e.TurnID = &n
		}
	default:
		payload[key] = anyValue(v)
	}
}

func asInt64(v slog.Value) (int64, bool) {
	if v.Kind() == slog.KindInt64 {
		return v.Int64(), true
	}
	n, ok := v.Any().(int64)
	return n, ok
}

// anyValue 把 slog.Value 转成能交给 encoding/json 的普通 Go 值。
func anyValue(v slog.Value) any {
	switch v.Kind() {
	case slog.KindString:
		return v.String()
	case slog.KindInt64:
		return v.Int64()
	case slog.KindUint64:
		return v.Uint64()
	case slog.KindFloat64:
		return v.Float64()
	case slog.KindBool:
		return v.Bool()
	case slog.KindDuration:
		return v.Duration().String()
	case slog.KindTime:
		return v.Time().Format(time.RFC3339Nano)
	case slog.KindGroup:
		m := make(map[string]any, len(v.Group()))
		for _, ga := range v.Group() {
			m[ga.Key] = anyValue(ga.Value.Resolve())
		}
		return m
	default:
		// error 直接 json.Marshal 会得到 {}（没有导出字段），所以先取文本。
		if err, ok := v.Any().(error); ok {
			return err.Error()
		}
		return v.Any()
	}
}

func levelName(l slog.Level) string {
	switch {
	case l < slog.LevelInfo:
		return "debug"
	case l < slog.LevelWarn:
		return "info"
	case l < slog.LevelError:
		return "warn"
	default:
		return "error"
	}
}
