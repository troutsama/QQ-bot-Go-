package store

import (
	"context"
	"fmt"
	"time"

	"qq-bot/internal/platform"
)

// RecordChannelMessage 将 msg 的字段写入独立的 channel_messages 表，不创建业务轮次。
// ctx 控制本次数据库操作的取消和超时，msg 是渠道接收或发送成功后整理出的记录。
// 返回 (true, nil) 表示新插入一条，(false, nil) 表示该消息已存在，(false, err) 表示保存失败。
// 去重键是 provider/app_id/scene/scene_id/direction/msg_id；只忽略这个键的重复，
// 不会更新已有记录，也不会把缺表、取消、其他约束等错误当成重复消息。
func (s *Store) RecordChannelMessage(ctx context.Context, msg platform.ChannelMessage) (bool, error) {
	// 平台、会话、方向和消息 ID 用于定位唯一消息；Kind 用于解释记录内容。
	// 正文、回复关联和发送者允许为空，以支持附件事件、主动发送等情况。
	if msg.Provider == "" || msg.Conv.AppID == "" || msg.Conv.Scene == "" || msg.Conv.SceneID == "" || msg.MsgID == "" || msg.Kind == "" {
		return false, fmt.Errorf("store: 渠道消息缺少平台、会话、消息 ID 或类型")
	}
	if msg.Direction != platform.ChannelIncoming && msg.Direction != platform.ChannelOutgoing {
		return false, fmt.Errorf("store: 无效渠道消息方向 %q", msg.Direction)
	}
	// occurred_at 表示消息发生时间，recorded_at 表示本次执行保存的时间。
	// 调用方未提供发生时间时才补当前时间，不覆盖已有的平台时间。
	now := time.Now()
	if msg.OccurredAt.IsZero() {
		msg.OccurredAt = now
	}
	// 字段对应关系：
	// Provider → provider；Conv.AppID/Scene/SceneID → app_id/scene/scene_id；
	// Direction → direction；MsgID → msg_id（平台 ID，表主键 id 由 SQLite 生成）；
	// ReplyToMsgID/ReplyToEventID/Seq → reply_to_msg_id/reply_to_event_id/reply_seq；
	// SenderOpenID → sender_openid；Kind → kind；Text → content；
	// HasAttachment → has_attachment（SQLite 以 0/1 保存布尔值）；
	// OccurredAt → occurred_at；本次取得的 now → recorded_at，两个时间均为 Unix 毫秒。
	// VALUES 中的 ? 与下方参数一一对应，用户正文作为参数传入，不拼接进 SQL。
	res, err := s.db.ExecContext(ctx, `
		INSERT INTO channel_messages (
			provider, app_id, scene, scene_id, direction, msg_id,
			reply_to_msg_id, reply_to_event_id, reply_seq, sender_openid,
			kind, content, has_attachment, occurred_at, recorded_at
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(provider, app_id, scene, scene_id, direction, msg_id) DO NOTHING`,
		msg.Provider, msg.Conv.AppID, string(msg.Conv.Scene), msg.Conv.SceneID,
		string(msg.Direction), msg.MsgID, msg.ReplyToMsgID, msg.ReplyToEventID,
		msg.Seq, msg.SenderOpenID, msg.Kind, msg.Text, msg.HasAttachment,
		msg.OccurredAt.UnixMilli(), now.UnixMilli())
	if err != nil {
		return false, fmt.Errorf("store: 保存渠道消息: %w", err)
	}
	// 新消息 INSERT 影响 1 行；命中指定唯一键的 DO NOTHING 影响 0 行。
	// 入站调用方根据此结果决定是否向 Events 投递，避免重复交给业务处理。
	n, err := res.RowsAffected()
	if err != nil {
		return false, fmt.Errorf("store: 获取渠道消息写入结果: %w", err)
	}
	return n == 1, nil
}
