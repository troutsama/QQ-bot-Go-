package chat

import (
	"time"

	"qq-bot/internal/platform"
)

// User 用户维度数据结构（对应 users 表）
type User struct {
	ID        int64
	AppID     string
	OpenID    string
	Nickname  string
	Avatar    string
	CreatedAt int64
	UpdatedAt int64
}

// Group 群聊维度数据结构（对应 groups 表）
type Group struct {
	ID          int64
	AppID       string
	GroupOpenID string
	Name        string
	CreatedAt   int64
	UpdatedAt   int64
}

// Conversation 会话维度数据结构（对应 conversations 表）
type Conversation struct {
	ID        int64
	AppID     string
	Scene     platform.Scene
	SceneID   string // c2c: user_openid; group: group_openid
	CreatedAt int64
	UpdatedAt int64
}

// LLMRequestRecord 模型请求全量快照记录（对应 llm_requests 表）
type LLMRequestRecord struct {
	ID               int64
	TraceID          string
	ConversationID   int64
	Provider         string
	Model            string
	Temperature      float64
	PromptTokens     int
	CompletionTokens int
	TotalTokens      int
	DurationMS       int64
	Status           string // "success" | "failed"
	ErrorMessage     string
	RawRequest       string // 完整请求入参 JSON
	RawResponse      string // 完整模型返回 JSON
	CreatedAt        int64
}

// TurnStatus 轮次状态定义
type TurnStatus string

const (
	StatusProcessing TurnStatus = "processing" // 处理中（中断/崩溃排查依据）
	StatusOK         TurnStatus = "ok"         // 成功完成（进入上下文）
	StatusFailed     TurnStatus = "failed"     // 失败（不进上下文）
	StatusFiltered   TurnStatus = "filtered"   // 过滤（命令/非文本，不进上下文）
)

// TurnParams 进入对话时开启新轮次的入参
type TurnParams struct {
	ConvID    int64
	UserID    int64
	MsgID     string
	Sender    platform.Sender
	UserText  string
	TraceID   string
	StartedAt time.Time
}

// TurnRef 轮次引用句柄
// IsNew 为 true 表示新创建的轮次；为 false 表示命中重复消息（幂等处理）
type TurnRef struct {
	ID        int64
	IsNew     bool
	Status    TurnStatus
	StartedAt time.Time
}

// StoredMessage 存储在数据库中的消息片段（对应 messages 表）
type StoredMessage struct {
	ID        int64
	TurnID    int64
	SeqInTurn int
	Role      string // 'user' | 'assistant' | 'tool'
	Content   string
	CreatedAt int64
}

// Turn 一轮完整问答记录与聚合（对应 turns 表）
type Turn struct {
	ID             int64
	ConversationID int64
	UserID         int64
	InboundMsgID   string
	Status         TurnStatus
	LLMRequestID   *int64 // 关联的 llm_requests 表主键，可为 nil
	UserText       string
	ReplyText      string
	TraceID        string
	StartedAt      int64
	FinishedAt     *int64
	Messages       []StoredMessage
}

// Event 业务审计事件（对应 events 表）
type Event struct {
	ID             int64
	Kind           string
	Level          string
	Message        string
	TraceID        string
	AppID          string
	ConversationID *int64
	TurnID         *int64
	PlatformMsgID  string
	Payload        any // 业务载荷，落库前转为 JSON 字符串
	CreatedAt      int64
}

// EventQuery 事件查询条件（用于 Admin API 游标分页）
type EventQuery struct {
	Kind           string
	Level          string
	TraceID        string
	ConversationID *int64
	Since          *time.Time
	Until          *time.Time
	Limit          int
	Cursor         int64
}
