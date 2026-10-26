package store

import (
	"context"
	"database/sql"
	"fmt"
	"slices"
	"strings"
	"time"

	"qq-bot/internal/chat"
)

// BeginTurn 开启一轮问答，返回轮次引用。
//
// 幂等靠 turns 上的 UNIQUE (conversation_id, inbound_msg_id) 实现，而不是内存去重：
//   - RowsAffected() == 1：新建了 processing 轮，并在**同一事务**内写入第一条 user 消息，
//     返回 TurnRef{IsNew: true}；
//   - RowsAffected() == 0：同一条平台消息已经建过轮，读回既有轮并返回 IsNew: false。
//
// 调用方**必须**显式处理 IsNew == false 分支：既可能是重复推送（跳过），
// 也可能是崩溃遗留（接管重试），判定规则见 04 R15。
//
// 方案 A 的 turns 没有 sender_openid/sender_nickname 列，发送者记在 user_id 上；
// V1 只有 c2c，sender 的 openid 与 conversations.scene_id 相同，因此 p.Sender 暂不落库。
func (s *Store) BeginTurn(ctx context.Context, p chat.TurnParams) (chat.TurnRef, error) {
	started := p.StartedAt
	if started.IsZero() {
		started = time.Now()
	}
	now := started.UnixMilli()

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return chat.TurnRef{}, fmt.Errorf("开启事务失败: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	var userID any // NULL：V1 不写 users 表（M1 决策 T6）
	if p.UserID > 0 {
		userID = p.UserID
	}

	res, err := tx.ExecContext(ctx, `
		INSERT INTO turns (conversation_id, user_id, inbound_msg_id, status, user_text, trace_id, started_at)
		VALUES (?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT (conversation_id, inbound_msg_id) DO NOTHING`,
		p.ConvID, userID, p.MsgID, string(chat.StatusProcessing), p.UserText, p.TraceID, now)
	if err != nil {
		return chat.TurnRef{}, fmt.Errorf("插入轮次失败: %w", err)
	}

	n, err := res.RowsAffected()
	if err != nil {
		return chat.TurnRef{}, fmt.Errorf("读取轮次插入结果失败: %w", err)
	}

	if n == 1 {
		id, err := res.LastInsertId()
		if err != nil {
			return chat.TurnRef{}, fmt.Errorf("读取轮次 ID 失败: %w", err)
		}
		if err := insertMessage(ctx, tx, id, 0, "user", p.UserText, now); err != nil {
			return chat.TurnRef{}, err
		}
		if err := tx.Commit(); err != nil {
			return chat.TurnRef{}, fmt.Errorf("提交事务失败: %w", err)
		}
		return chat.TurnRef{
			ID:        id,
			IsNew:     true,
			Status:    chat.StatusProcessing,
			StartedAt: started,
		}, nil
	}

	// 冲突：读回既有轮。状态可能是 ok/failed/filtered/processing，
	// 由调用方据 R15 决定跳过还是接管。
	var (
		ref       chat.TurnRef
		status    string
		startedMS int64
	)
	if err := tx.QueryRowContext(ctx, `
		SELECT id, status, started_at
		FROM turns
		WHERE conversation_id = ? AND inbound_msg_id = ?`,
		p.ConvID, p.MsgID,
	).Scan(&ref.ID, &status, &startedMS); err != nil {
		return chat.TurnRef{}, fmt.Errorf("读回既有轮次失败: %w", err)
	}
	ref.IsNew = false
	ref.Status = chat.TurnStatus(status)
	ref.StartedAt = time.UnixMilli(startedMS)
	return ref, tx.Commit()
}

// FinishTurn 把一轮从 processing 改为 ok，写入 reply_text 与 assistant 消息。
//
// 条件更新 status = 'processing' 是为了防重：崩溃恢复后若重复结束同一轮，
// 第二次会命中 0 行并报错，而不是插入第二条 assistant 消息。
func (s *Store) FinishTurn(ctx context.Context, turnID int64, replyText string) error {
	now := time.Now().UnixMilli()

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("开启事务失败: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	res, err := tx.ExecContext(ctx, `
		UPDATE turns
		SET status = ?, reply_text = ?, finished_at = ?
		WHERE id = ? AND status = ?`,
		string(chat.StatusOK), replyText, now, turnID, string(chat.StatusProcessing))
	if err != nil {
		return fmt.Errorf("更新轮次状态失败: %w", err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return fmt.Errorf("读取更新结果失败: %w", err)
	}
	if n == 0 {
		return fmt.Errorf("结束轮次 %d 失败：该轮不存在或已不是 processing 状态", turnID)
	}

	// 轮内序号取 MAX+1：V2 一轮会有 tool 消息，不能写死成 1。
	var seq int
	if err := tx.QueryRowContext(ctx,
		`SELECT COALESCE(MAX(seq_in_turn), -1) + 1 FROM messages WHERE turn_id = ?`, turnID,
	).Scan(&seq); err != nil {
		return fmt.Errorf("计算消息序号失败: %w", err)
	}
	if err := insertMessage(ctx, tx, turnID, seq, "assistant", replyText, now); err != nil {
		return err
	}

	if err := tx.Commit(); err != nil {
		return fmt.Errorf("提交事务失败: %w", err)
	}
	return nil
}

// FailTurn 把一轮从 processing 改为 failed。失败的轮不进上下文（RecentTurns 只取 ok）。
//
// 失败原因不写 turns：方案 A 的 turns 没有 error_kind/error_message 列，
// 错误原文由调用方写入 events（kind=llm_error / outbound_error），便于按 trace_id 追溯。
func (s *Store) FailTurn(ctx context.Context, turnID int64) error {
	return s.finishWithoutReply(ctx, turnID, chat.StatusFailed)
}

// FilterTurn 把一轮标记为 filtered：非文本或命令轮不调用模型，
// 落库可审计，但不进上下文（R4）。
//
// 与 FailTurn 的区别是语义：filtered 不是出错，而是「按规则不需要回答」。
// 两者都不写 assistant 消息 —— 否则上下文里会出现空回答。
func (s *Store) FilterTurn(ctx context.Context, turnID int64) error {
	return s.finishWithoutReply(ctx, turnID, chat.StatusFiltered)
}

// finishWithoutReply 把 processing 轮改为不给回复的终态（failed / filtered）。
//
// 条件更新 status='processing' 是为了防重：同一轮被结束两次时第二次命中 0 行，
// 调用方能立刻发现状态机被写坏了。
func (s *Store) finishWithoutReply(ctx context.Context, turnID int64, status chat.TurnStatus) error {
	res, err := s.db.ExecContext(ctx, `
		UPDATE turns
		SET status = ?, finished_at = ?
		WHERE id = ? AND status = ?`,
		string(status), time.Now().UnixMilli(), turnID, string(chat.StatusProcessing))
	if err != nil {
		return fmt.Errorf("标记轮次为 %s 失败: %w", status, err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return fmt.Errorf("读取更新结果失败: %w", err)
	}
	if n == 0 {
		return fmt.Errorf("标记轮次 %d 为 %s 失败：该轮不存在或已不是 processing 状态", turnID, status)
	}
	return nil
}

// RecentTurns 取最近 limit 轮**有效**对话（status='ok'），按时间正序返回，每轮带全部消息。
//
// 分两步查：先按轮取（ORDER BY id DESC LIMIT n 再反转），再按 turn_id IN (...) 取消息。
// 不按消息行数取 —— V2 一轮会产生多行，按行取会切出半截轮（03 §4.3、风险 R5）。
func (s *Store) RecentTurns(ctx context.Context, convID int64, limit int) ([]chat.Turn, error) {
	if limit <= 0 {
		return []chat.Turn{}, nil
	}

	rows, err := s.db.QueryContext(ctx, `
		SELECT id, conversation_id, user_id, inbound_msg_id, status, llm_request_id,
		       user_text, reply_text, trace_id, started_at, finished_at
		FROM turns
		WHERE conversation_id = ? AND status = ?
		ORDER BY id DESC
		LIMIT ?`,
		convID, string(chat.StatusOK), limit)
	if err != nil {
		return nil, fmt.Errorf("查询最近有效轮次失败: %w", err)
	}
	defer rows.Close()

	turns := make([]chat.Turn, 0, limit)
	for rows.Next() {
		var (
			t        chat.Turn
			status   string
			userID   sql.NullInt64
			llmReqID sql.NullInt64
			finished sql.NullInt64
		)
		if err := rows.Scan(
			&t.ID, &t.ConversationID, &userID, &t.InboundMsgID, &status, &llmReqID,
			&t.UserText, &t.ReplyText, &t.TraceID, &t.StartedAt, &finished,
		); err != nil {
			return nil, fmt.Errorf("扫描轮次失败: %w", err)
		}
		t.Status = chat.TurnStatus(status)
		if userID.Valid {
			t.UserID = userID.Int64
		}
		if llmReqID.Valid {
			id := llmReqID.Int64
			t.LLMRequestID = &id
		}
		if finished.Valid {
			ms := finished.Int64
			t.FinishedAt = &ms
		}
		turns = append(turns, t)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("遍历轮次失败: %w", err)
	}
	if len(turns) == 0 {
		return turns, nil
	}

	slices.Reverse(turns) // 数据库按 id DESC 取，返回给上层时改为时间正序

	args := make([]any, len(turns))
	index := make(map[int64]int, len(turns))
	for i := range turns {
		args[i] = turns[i].ID
		index[turns[i].ID] = i
	}
	placeholders := strings.TrimSuffix(strings.Repeat("?,", len(turns)), ",")

	msgRows, err := s.db.QueryContext(ctx, `
		SELECT id, turn_id, seq_in_turn, role, content, created_at
		FROM messages
		WHERE turn_id IN (`+placeholders+`)
		ORDER BY turn_id ASC, seq_in_turn ASC`, args...)
	if err != nil {
		return nil, fmt.Errorf("查询轮内消息失败: %w", err)
	}
	defer msgRows.Close()

	for msgRows.Next() {
		var m chat.StoredMessage
		if err := msgRows.Scan(&m.ID, &m.TurnID, &m.SeqInTurn, &m.Role, &m.Content, &m.CreatedAt); err != nil {
			return nil, fmt.Errorf("扫描消息失败: %w", err)
		}
		i, ok := index[m.TurnID]
		if !ok {
			continue
		}
		turns[i].Messages = append(turns[i].Messages, m)
	}
	if err := msgRows.Err(); err != nil {
		return nil, fmt.Errorf("遍历消息失败: %w", err)
	}
	return turns, nil
}

// StaleTurns 返回该会话中已中断的轮次（status=processing、finished_at IS NULL、started_at 早于阈值）。
//
// 它只**报告**残轮，不改状态：是「接管重试」还是「跳过」必须由调用方按 R15 判定。
// 若在这里把残轮一律置为 failed，就再也无法区分「上次崩了」和「另一个 worker 正在处理」。
func (s *Store) StaleTurns(ctx context.Context, convID int64, olderThan time.Duration) ([]chat.TurnRef, error) {
	cutoff := time.Now().Add(-olderThan).UnixMilli()

	rows, err := s.db.QueryContext(ctx, `
		SELECT id, status, started_at
		FROM turns
		WHERE conversation_id = ? AND status = ? AND finished_at IS NULL AND started_at < ?
		ORDER BY id ASC`,
		convID, string(chat.StatusProcessing), cutoff)
	if err != nil {
		return nil, fmt.Errorf("查询残轮失败: %w", err)
	}
	defer rows.Close()

	var refs []chat.TurnRef
	for rows.Next() {
		var (
			ref       chat.TurnRef
			status    string
			startedMS int64
		)
		if err := rows.Scan(&ref.ID, &status, &startedMS); err != nil {
			return nil, fmt.Errorf("扫描残轮失败: %w", err)
		}
		ref.Status = chat.TurnStatus(status)
		ref.StartedAt = time.UnixMilli(startedMS)
		refs = append(refs, ref)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("遍历残轮失败: %w", err)
	}
	return refs, nil
}

// 编译期断言：chat.Store 是消费方定义的接口，Store 必须始终满足它。
// 将来任何一方改了方法签名，都会在这里直接编译失败，而不是等到 chat 的测试里才暴露。
var _ chat.Store = (*Store)(nil)

// insertMessage 在给定事务内写入一条轮内消息。
func insertMessage(ctx context.Context, tx *sql.Tx, turnID int64, seq int, role, content string, createdAt int64) error {
	_, err := tx.ExecContext(ctx, `
		INSERT INTO messages (turn_id, seq_in_turn, role, content, created_at)
		VALUES (?, ?, ?, ?, ?)`,
		turnID, seq, role, content, createdAt)
	if err != nil {
		return fmt.Errorf("写入 %s 消息失败: %w", role, err)
	}
	return nil
}
