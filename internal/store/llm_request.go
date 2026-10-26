package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"qq-bot/internal/chat"
)

// RecordLLMRequest 写入一次模型调用的全量快照，并把 turns.llm_request_id 反写为该行 ID。
//
// 插入与反写在同一事务内：否则可能出现「快照写了、轮次没挂上」的半截记录。
// raw_request / raw_response 按原样保存，不做截断 —— 这张表的价值就是无损溯源。
func (s *Store) RecordLLMRequest(ctx context.Context, rec *chat.LLMRequestRecord, turnID int64) error {
	if rec == nil {
		return errors.New("记录模型请求失败：record 不能为 nil")
	}

	created := rec.CreatedAt
	if created == 0 {
		created = time.Now().UnixMilli()
	}

	var convID any // llm_requests.conversation_id 可空
	if rec.ConversationID > 0 {
		convID = rec.ConversationID
	}

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("开启事务失败: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	res, err := tx.ExecContext(ctx, `
		INSERT INTO llm_requests (
			trace_id, conversation_id, provider, model, temperature,
			prompt_tokens, completion_tokens, total_tokens, duration_ms,
			status, error_message, raw_request, raw_response, created_at
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		rec.TraceID, convID, rec.Provider, rec.Model, rec.Temperature,
		rec.PromptTokens, rec.CompletionTokens, rec.TotalTokens, rec.DurationMS,
		rec.Status, rec.ErrorMessage, rec.RawRequest, rec.RawResponse, created)
	if err != nil {
		return fmt.Errorf("写入模型请求快照失败: %w", err)
	}

	id, err := res.LastInsertId()
	if err != nil {
		return fmt.Errorf("读取模型请求 ID 失败: %w", err)
	}

	if turnID > 0 {
		if _, err := tx.ExecContext(ctx,
			`UPDATE turns SET llm_request_id = ? WHERE id = ?`, id, turnID,
		); err != nil {
			return fmt.Errorf("反写轮次 llm_request_id 失败: %w", err)
		}
	}

	if err := tx.Commit(); err != nil {
		return fmt.Errorf("提交事务失败: %w", err)
	}

	rec.ID = id
	rec.CreatedAt = created
	return nil
}

// GetLLMRequest 按 ID 读取一条模型请求快照（admin 查询详情用）。
// 记录不存在时错误可用 errors.Is(err, sql.ErrNoRows) 判定。
func (s *Store) GetLLMRequest(ctx context.Context, id int64) (*chat.LLMRequestRecord, error) {
	var (
		rec  chat.LLMRequestRecord
		conv sql.NullInt64
		temp sql.NullFloat64
	)
	err := s.db.QueryRowContext(ctx, `
		SELECT id, trace_id, conversation_id, provider, model, temperature,
		       prompt_tokens, completion_tokens, total_tokens, duration_ms,
		       status, error_message, raw_request, raw_response, created_at
		FROM llm_requests
		WHERE id = ?`, id,
	).Scan(
		&rec.ID, &rec.TraceID, &conv, &rec.Provider, &rec.Model, &temp,
		&rec.PromptTokens, &rec.CompletionTokens, &rec.TotalTokens, &rec.DurationMS,
		&rec.Status, &rec.ErrorMessage, &rec.RawRequest, &rec.RawResponse, &rec.CreatedAt,
	)
	if err != nil {
		return nil, err
	}
	if conv.Valid {
		rec.ConversationID = conv.Int64
	}
	if temp.Valid {
		rec.Temperature = temp.Float64
	}
	return &rec, nil
}
