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

// ChannelDirection 表示消息相对于机器人的收发方向，对应数据库 direction 列。
type ChannelDirection string

const (
	ChannelIncoming ChannelDirection = "incoming" // 入站：机器人收到的消息。
	ChannelOutgoing ChannelDirection = "outgoing" // 出站：机器人成功发出的消息或通知。
)

// ChannelMessage 汇集写入 channel_messages 表的一条渠道通信记录。
// 它只承载待保存的数据，实际 SQL 由 Store.RecordChannelMessage 执行。
// 每条收发消息独立保存，不要求一收一发配对，也不依赖业务轮次 turn_id。
type ChannelMessage struct {
	Provider string // 渠道名称，当前为 "qqbot"；用于区分不同平台实现。
	// Conv 标识消息所属会话：AppID 是机器人应用 ID，Scene 是 c2c/group，
	// SceneID 在单聊中是对方 openid，在群聊中是群 openid；当前 QQ 仅实现单聊。
	Conv      ConversationKey
	Direction ChannelDirection // incoming 表示收到，outgoing 表示发出；以机器人为参照。
	MsgID     string           // 本条消息的平台 ID：入站取 IncomingMessage.MsgID，出站取发送成功回执的 MsgID；不是数据库主键。
	// ReplyToMsgID 是发送时使用的被回复消息 ID，来自 OutgoingMessage.Reply.MsgID。
	// 入站或主动发送时为空；它记录平台回复关联，不表示两条消息必须组成一轮问答。
	ReplyToMsgID string
	// ReplyToEventID 是发送时携带的事件回复凭据，来自 OutgoingMessage.Reply.EventID。
	// 没有事件凭据时为空，普通 QQ 单聊消息通常只使用 ReplyToMsgID。
	ReplyToEventID string
	// Seq 是同一个 ReplyRef 下递增的 QQ 发送序号，成功回执中也会返回该值。
	// 入站和无 ReplyRef 的主动消息为 0；它不是会话内的消息排序号，也不是数据库主键。
	Seq           uint32
	SenderOpenID  string // 入站发送者的 openid，来自 IncomingMessage.Sender.OpenID；当前出站记录留空，机器人身份由 Conv.AppID 标识。
	Kind          string // 记录类型：当前入站统一为 "message"，出站为 "text" 或 "typing"；不表示 user/assistant 业务角色。
	Text          string // 标准化正文，对应数据库 content 列；允许空文本，不包含 Raw 载荷或附件二进制。
	HasAttachment bool   // 是否带附件：入站来自 IncomingMessage.HasAttachment；只保存标记，不保存附件 URL 或文件内容，当前出站为 false。
	// OccurredAt 是消息发生时间：入站使用平台事件解析出的 Timestamp，
	// 当前出站使用 QQ 返回成功时的本地时间；零值由 Store 补为当前时间，入库统一转为 Unix 毫秒。
	OccurredAt time.Time
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
	// Send 发送一条消息。QQ 渠道在成功后保存通信记录。
	// ErrSendUnrecorded 表示已经发送，返回值仍包含可用回执，不得自动重发。
	// 为保存已完成的发送结果，QQ 渠道可在 ctx 取消后继续做有超时的写库。
	Send(ctx context.Context, msg OutgoingMessage) (SentMessage, error)
	// Events 入站事件流。Provider 关闭时通道被 close
	Events() <-chan IncomingMessage
}
