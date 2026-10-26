package chat

import (
	"context"
	"time"

	"qq-bot/internal/platform"
)

// EventSink 是审计事件的写入端口。
//
// 单独拆出来，是为了让 Outbound / aiStep 只依赖「写事件」这一件事；
// 它们的单测可以用内存 sink，不必实现整个 Store。
type EventSink interface {
	AppendEvent(ctx context.Context, e Event) error
}

// Store 是 chat 核心对数据层的**消费方接口**。
//
// 接口定义在消费方（本项目约定，见 AGENTS.md）：chat 不需要知道 SQLite、
// 表名或事务细节；internal/store 只负责提供这些方法。
// 生产代码里 chat 绝不 import internal/store，依赖方向始终是 store → chat。
//
// 签名必须与 internal/store 的方法逐一对应；store 包里有编译期断言，
// 将来改签名会在 go build 时立即暴露。
type Store interface {
	EventSink

	GetOrCreateConversation(ctx context.Context, key platform.ConversationKey) (*Conversation, bool, error)
	BeginTurn(ctx context.Context, p TurnParams) (TurnRef, error)
	FinishTurn(ctx context.Context, turnID int64, replyText string) error
	FailTurn(ctx context.Context, turnID int64) error
	FilterTurn(ctx context.Context, turnID int64) error
	RecentTurns(ctx context.Context, convID int64, limit int) ([]Turn, error)
	StaleTurns(ctx context.Context, convID int64, olderThan time.Duration) ([]TurnRef, error)
	RecordLLMRequest(ctx context.Context, rec *LLMRequestRecord, turnID int64) error
}
