package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"qq-bot/internal/chat"
)

const (
	defaultEventLimit = 50
	maxEventLimit     = 500

	// maxEventPayloadBytes 暂为常量；等 log.event_payload_max_kb 接入配置后再改为读取配置（03 §5.1）。
	maxEventPayloadBytes = 8 << 10
)

// AppendEvent 写入一条业务审计事件。
//
// Payload 先序列化为 JSON 再落库；超过上限时整条替换为
// {"truncated":true,"size":N}，而不是截断字符串 —— 保证存进去的永远是**合法 JSON**，
// 否则 admin 查出来再解析会炸。
func (s *Store) AppendEvent(ctx context.Context, e chat.Event) error {
	payload, err := marshalPayload(e.Payload, maxEventPayloadBytes)
	if err != nil {
		return err
	}

	created := e.CreatedAt
	if created == 0 {
		created = time.Now().UnixMilli()
	}
	level := e.Level
	if level == "" {
		level = "info"
	}

	var convID, turnID any
	if e.ConversationID != nil {
		convID = *e.ConversationID
	}
	if e.TurnID != nil {
		turnID = *e.TurnID
	}

	if _, err := s.db.ExecContext(ctx, `
		INSERT INTO events (
			created_at, level, kind, app_id, trace_id,
			conversation_id, turn_id, platform_msg_id, message, payload
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		created, level, e.Kind, e.AppID, e.TraceID,
		convID, turnID, e.PlatformMsgID, e.Message, payload,
	); err != nil {
		return fmt.Errorf("写入事件失败: %w", err)
	}
	return nil
}

// QueryEvents 按条件分页查询事件，按 id 倒序（新→旧）。
//
// 第二个返回值是下一页游标（取最后一条的 id），0 表示没有更多。
// 游标条件是 id < cursor：事件表是只追加的，id 顺序与 created_at 顺序一致，
// 用主键游标比用时间戳更稳（同一毫秒内的多条事件不会重复或漏页）。
func (s *Store) QueryEvents(ctx context.Context, q chat.EventQuery) ([]chat.Event, int64, error) {
	limit := q.Limit
	if limit <= 0 {
		limit = defaultEventLimit
	}
	if limit > maxEventLimit {
		limit = maxEventLimit
	}

	var (
		where []string
		args  []any
	)
	if q.Kind != "" {
		where = append(where, "kind = ?")
		args = append(args, q.Kind)
	}
	if q.Level != "" {
		where = append(where, "level = ?")
		args = append(args, q.Level)
	}
	if q.TraceID != "" {
		where = append(where, "trace_id = ?")
		args = append(args, q.TraceID)
	}
	if q.ConversationID != nil {
		where = append(where, "conversation_id = ?")
		args = append(args, *q.ConversationID)
	}
	if q.Since != nil {
		where = append(where, "created_at >= ?")
		args = append(args, q.Since.UnixMilli())
	}
	if q.Until != nil {
		where = append(where, "created_at <= ?")
		args = append(args, q.Until.UnixMilli())
	}
	if q.Cursor > 0 {
		where = append(where, "id < ?")
		args = append(args, q.Cursor)
	}

	query := `SELECT id, created_at, level, kind, app_id, trace_id,
	                 conversation_id, turn_id, platform_msg_id, message, payload
	          FROM events`
	if len(where) > 0 {
		query += " WHERE " + strings.Join(where, " AND ")
	}
	// 多取一条用于判断“还有没有下一页”。
	query += " ORDER BY id DESC LIMIT ?"
	args = append(args, limit+1)

	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, 0, fmt.Errorf("查询事件失败: %w", err)
	}
	defer rows.Close()

	events := make([]chat.Event, 0, limit)
	for rows.Next() {
		var (
			e    chat.Event
			conv sql.NullInt64
			turn sql.NullInt64
		)
		if err := rows.Scan(
			&e.ID, &e.CreatedAt, &e.Level, &e.Kind, &e.AppID, &e.TraceID,
			&conv, &turn, &e.PlatformMsgID, &e.Message, &e.Payload,
		); err != nil {
			return nil, 0, fmt.Errorf("扫描事件失败: %w", err)
		}
		if conv.Valid {
			id := conv.Int64
			e.ConversationID = &id
		}
		if turn.Valid {
			id := turn.Int64
			e.TurnID = &id
		}
		events = append(events, e)
	}
	if err := rows.Err(); err != nil {
		return nil, 0, fmt.Errorf("遍历事件失败: %w", err)
	}

	if len(events) > limit {
		events = events[:limit]
		return events, events[len(events)-1].ID, nil
	}
	return events, 0, nil
}

// CleanupEvents 删除 created_at 早于 retainDays 天前的事件，返回删除行数。
//
// retainDays <= 0 表示不清理 —— 防止误配成 0 时把审计日志整表清空（04 R19）。
func (s *Store) CleanupEvents(ctx context.Context, retainDays int) (int64, error) {
	if retainDays <= 0 {
		return 0, nil
	}
	cutoff := time.Now().Add(-time.Duration(retainDays) * 24 * time.Hour).UnixMilli()

	res, err := s.db.ExecContext(ctx, `DELETE FROM events WHERE created_at < ?`, cutoff)
	if err != nil {
		return 0, fmt.Errorf("清理过期事件失败: %w", err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return 0, fmt.Errorf("读取清理结果失败: %w", err)
	}
	return n, nil
}

// marshalPayload 把业务载荷转成可落库的 JSON 字符串；nil 存空串。
func marshalPayload(v any, limit int) (string, error) {
	if v == nil {
		return "", nil
	}
	raw, err := json.Marshal(v)
	if err != nil {
		return "", fmt.Errorf("序列化事件 payload 失败: %w", err)
	}
	if len(raw) <= limit {
		return string(raw), nil
	}
	// 整条替换，保证仍是合法 JSON。
	return fmt.Sprintf(`{"truncated":true,"size":%d}`, len(raw)), nil
}
