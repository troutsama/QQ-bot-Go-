package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"qq-bot/internal/chat"
	"qq-bot/internal/platform"
)

// GetOrCreateConversation 按 (app_id, scene, scene_id) 取会话，不存在则创建。
//
// 返回值 isNew 为 true 表示本次调用创建了会话，false 表示命中已有会话。
//
// 并发安全完全依赖 conversations 的 UNIQUE 约束：
// INSERT ... ON CONFLICT DO NOTHING + RowsAffected() 判定，冲突时再 SELECT 读回。
// 这样同一条消息被两个 goroutine / 两个进程同时处理时也只会有一行。
func (s *Store) GetOrCreateConversation(
	ctx context.Context,
	key platform.ConversationKey,
) (*chat.Conversation, bool, error) {
	conv, err := s.findConversation(ctx, key)
	if err == nil {
		return conv, false, nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return nil, false, fmt.Errorf("查询会话失败: %w", err)
	}

	now := time.Now().UnixMilli()
	res, err := s.db.ExecContext(ctx, `
		INSERT INTO conversations (app_id, scene, scene_id, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?)
		ON CONFLICT (app_id, scene, scene_id) DO NOTHING`,
		key.AppID, string(key.Scene), key.SceneID, now, now)
	if err != nil {
		return nil, false, fmt.Errorf("创建会话失败: %w", err)
	}

	n, err := res.RowsAffected()
	if err != nil {
		return nil, false, fmt.Errorf("读取会话插入结果失败: %w", err)
	}
	if n == 1 {
		id, err := res.LastInsertId()
		if err != nil {
			return nil, false, fmt.Errorf("读取会话 ID 失败: %w", err)
		}
		return &chat.Conversation{
			ID:        id,
			AppID:     key.AppID,
			Scene:     key.Scene,
			SceneID:   key.SceneID,
			CreatedAt: now,
			UpdatedAt: now,
		}, true, nil
	}

	// 竞态：另一个连接/进程抢先插入了同一会话，读回它。
	conv, err = s.findConversation(ctx, key)
	if err != nil {
		return nil, false, fmt.Errorf("读回已存在的会话失败: %w", err)
	}
	return conv, false, nil
}

// findConversation 返回 (app_id, scene, scene_id) 对应的会话；不存在时返回 sql.ErrNoRows。
func (s *Store) findConversation(ctx context.Context, key platform.ConversationKey) (*chat.Conversation, error) {
	var (
		conv  chat.Conversation
		scene string
	)
	err := s.db.QueryRowContext(ctx, `
		SELECT id, app_id, scene, scene_id, created_at, updated_at
		FROM conversations
		WHERE app_id = ? AND scene = ? AND scene_id = ?`,
		key.AppID, string(key.Scene), key.SceneID,
	).Scan(&conv.ID, &conv.AppID, &scene, &conv.SceneID, &conv.CreatedAt, &conv.UpdatedAt)
	if err != nil {
		return nil, err
	}
	conv.Scene = platform.Scene(scene)
	return &conv, nil
}
