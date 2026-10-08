package platform

import (
	"context"
	"sync/atomic"
	"time"
)

type IncomingMessage struct {
	MsgID         string // 平台消息 ID
	EventID       string // 事件类回调的凭据（消息类事件为空）
	Conv          ConversationKey
	Sender        Sender
	Text          string // 已提取的文本，可能为空
	HasAttachment bool   // 是否含非文本段（区分「空文本因为发了图」与「用户发了空白」）
	MentionedBot  bool   // 群消息是否 @ 了机器人（暂时不做）
	Timestamp     time.Time
	Raw           any // 平台原始载荷，排障用；不进 DB 正文
}

type OutKind string

const (
	OutText   OutKind = "text"   // 普通文本
	OutTyping OutKind = "typing" // 输入中状态
)

type ReplyRef struct {
	MsgID   string
	EventID string

	seq atomic.Uint32
}

func NewReplyRef(msgID, eventID string) *ReplyRef {
	return &ReplyRef{MsgID: msgID, EventID: eventID}
}

func (r *ReplyRef) NextSeq() uint32 {
	return r.seq.Add(1)
}

type OutgoingMessage struct {
	Conv  ConversationKey
	Reply *ReplyRef // nil = 主动消息（受频控，且用户可关闭）
	Kind  OutKind
	Text  string
}

type SentMessage struct {
	MsgID string
	Seq   uint32
}

type Status string

const (
	StatusDisconnected Status = "disconnected"
	StatusConnecting   Status = "connecting"
	StatusConnected    Status = "connected"
)

type Provider interface {
	// Name 适配器名
	Name() string
	// Start 建立连接并开始投递事件。应尽快返回；事件循环在内部 goroutine
	Start(ctx context.Context) error
	// Stop 优雅断开：停止接收新事件，等待内部 goroutine 退出、关闭 Events 通道
	// 需要可以幂等
	Stop(ctx context.Context) error
	// Status 当前连接状态
	Status() Status
	// Send 发送一条消息。阻塞直到平台返回或 ctx 超时
	Send(ctx context.Context, msg OutgoingMessage) (SentMessage, error)
	// Events 入站事件流。Provider 关闭时通道被 close
	Events() <-chan IncomingMessage
}
