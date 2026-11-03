package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"
)

// ── 出入库 DTO（store 自足，不引用任何业务/端口包，杜绝循环依赖）──

// TurnParams 开启一轮的入参：一条入站消息开一轮。
type TurnParams struct {
	ConvID    int64     // turns.conversation_id
	MsgID     string    // turns.inbound_msg_id（幂等键）
	UserText  string    // turns.user_text
	TraceID   string    // turns.trace_id
	StartedAt time.Time // turns.started_at（毫秒）
}

// TurnRef 轮次句柄：IsNew=true 表示新开的轮；false = 重复事件命中已有轮。
type TurnRef struct {
	ID    int64
	IsNew bool
}

// TurnStatus 轮次状态，对应 turns.status 列。
type TurnStatus string

const (
	StatusProcessing TurnStatus = "processing"
	StatusOK         TurnStatus = "ok"
	StatusFailed     TurnStatus = "failed"
)

// EnsureConversation 找到或创建会话，返回 conversation.id。
// ON CONFLICT DO NOTHING：已存在则静默跳过 → 免掉「先查后插」的竞态。
func (s *Store) EnsureConversation(ctx context.Context, appID, scene, sceneID string) (int64, error) {
	now := time.Now().UnixMilli()
	if _, err := s.db.ExecContext(ctx,
		`INSERT INTO conversations (app_id, scene, scene_id, created_at, updated_at)
                 VALUES (?, ?, ?, ?, ?)
                 ON CONFLICT(app_id, scene, scene_id) DO NOTHING`,
		appID, scene, sceneID, now, now); err != nil {
		return 0, fmt.Errorf("store: 插入会话: %w", err)
	}

	var id int64
	if err := s.db.QueryRowContext(ctx,
		`SELECT id FROM conversations WHERE app_id = ? AND scene = ? AND scene_id = ?`,
		appID, scene, sceneID).Scan(&id); err != nil {
		return 0, fmt.Errorf("store: 查询会话: %w", err)
	}
	return id, nil
}

// BeginTurn 开启新一轮（幂等）：命中 UNIQUE(conversation_id, inbound_msg_id) 返回已有轮。
func (s *Store) BeginTurn(ctx context.Context, p TurnParams) (TurnRef, error) {
	// 先查：重复事件重推时，不重复建轮
	var existing int64
	err := s.db.QueryRowContext(ctx,
		`SELECT id FROM turns WHERE conversation_id = ? AND inbound_msg_id = ?`,
		p.ConvID, p.MsgID).Scan(&existing)
	switch {
	case err == nil:
		return TurnRef{ID: existing, IsNew: false}, nil
	case !errors.Is(err, sql.ErrNoRows):
		return TurnRef{}, fmt.Errorf("store: 查询轮次: %w", err)
	}

	now := time.Now().UnixMilli()
	res, err := s.db.ExecContext(ctx,
		`INSERT INTO turns (conversation_id, inbound_msg_id, status, user_text, trace_id, started_at)
                 VALUES (?, ?, ?, ?, ?, ?)`,
		p.ConvID, p.MsgID, string(StatusProcessing), p.UserText, p.TraceID, now)
	if err != nil {
		return TurnRef{}, fmt.Errorf("store: 开启轮次: %w", err)
	}
	id, err := res.LastInsertId()
	if err != nil {
		return TurnRef{}, fmt.Errorf("store: 取轮次主键: %w", err)
	}
	return TurnRef{ID: id, IsNew: true}, nil
}

// AppendMessage 往某轮插入一条消息；seq_in_turn = 当前最大序号 + 1。
func (s *Store) AppendMessage(ctx context.Context, turnID int64, role, content string) (int64, error) {
	var seq int
	if err := s.db.QueryRowContext(ctx,
		`SELECT COALESCE(MAX(seq_in_turn), 0) + 1 FROM messages WHERE turn_id = ?`,
		turnID).Scan(&seq); err != nil {
		return 0, fmt.Errorf("store: 取消息序号: %w", err)
	}

	res, err := s.db.ExecContext(ctx,
		`INSERT INTO messages (turn_id, seq_in_turn, role, content, created_at)
                 VALUES (?, ?, ?, ?, ?)`,
		turnID, seq, role, content, time.Now().UnixMilli())
	if err != nil {
		return 0, fmt.Errorf("store: 插入消息: %w", err)
	}
	return res.LastInsertId()
}

// FinishTurn 收尾本轮：标记状态、写回复、记完成时间。
func (s *Store) FinishTurn(ctx context.Context, turnID int64, status TurnStatus, replyText string) error {
	if _, err := s.db.ExecContext(ctx,
		`UPDATE turns SET status = ?, reply_text = ?, finished_at = ?
                 WHERE id = ?`,
		string(status), replyText, time.Now().UnixMilli(), turnID); err != nil {
		return fmt.Errorf("store: 收尾轮次: %w", err)
	}
	return nil
}
