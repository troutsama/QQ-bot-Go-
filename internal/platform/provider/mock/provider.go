// Package mock 是 platform.Provider 的内存实现：不发真实消息，只打印。
package mock

import (
	"context"
	"fmt"
	"sync"
	"time"

	"qq-bot/internal/platform"
)

// 编译期断言：mock 必须满足 Provider 接口。
// 好处是「改了接口忘改实现」在 go build 时就报错，而不是运行时类型断言失败。
// 惯用法：断言写在**实现方**（这个包），不写在接口方。
var _ platform.Provider = (*Provider)(nil)

// Provider 假平台。
type Provider struct {
	mu     sync.Mutex
	sent   []platform.OutgoingMessage
	events chan platform.IncomingMessage
	status platform.Status

	// closed 与 events 的关闭配合，避免「向已关闭 channel 发送」。
	// 见 Stop 的注释。
	closed bool
}

// New 创建假平台。注意它不连接任何东西，也不启动 goroutine。
func New() *Provider {
	return &Provider{
		events: make(chan platform.IncomingMessage, 64),
		status: platform.StatusDisconnected,
	}
}

func (p *Provider) Name() string { return "mock" }

func (p *Provider) Start(ctx context.Context) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.status = platform.StatusConnected
	return nil
}

// Stop 幂等：重复调用必须安全，因为组合根的关闭路径可能被触发两次
// （信号一次、admin 重启接口一次）。
func (p *Provider) Stop(ctx context.Context) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.closed {
		return nil
	}
	p.closed = true
	p.status = platform.StatusDisconnected
	close(p.events) // 只有生产者关闭 channel
	return nil
}

func (p *Provider) Status() platform.Status {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.status
}

func (p *Provider) Send(ctx context.Context, msg platform.OutgoingMessage) (platform.SentMessage, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.closed {
		return platform.SentMessage{}, platform.ErrNotConnected
	}
	p.sent = append(p.sent, msg)
	fmt.Printf("[mock] → %s: %s\n", msg.Conv, msg.Text)
	return platform.SentMessage{MsgID: fmt.Sprintf("mock-%d", len(p.sent))}, nil
}

func (p *Provider) Events() <-chan platform.IncomingMessage { return p.events }

// ── 以下是 mock 独有的测试辅助方法（接口里没有）──

// Inject 注入一条入站事件，模拟「用户发了条消息」。
//
// 单测和未来的 /admin/debug/inject 都走它。
// ⚠️ select 里必须带 ctx.Done()：否则消费者退出后这里会永久阻塞（goroutine 泄漏）。
func (p *Provider) Inject(ctx context.Context, msg platform.IncomingMessage) error {
	p.mu.Lock()
	closed := p.closed
	p.mu.Unlock()
	if closed {
		return platform.ErrNotConnected
	}
	select {
	case p.events <- msg:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

// Sent 返回已发送消息的快照。
//
// ⚠️ 必须复制切片：直接 return p.sent 会让调用方和内部共享同一个底层数组，
// 之后内部 append 可能覆盖调用方正在读的内容（并发下的数据竞争）。
// append([]T(nil), src...) 是复制切片的惯用法。
func (p *Provider) Sent() []platform.OutgoingMessage {
	p.mu.Lock()
	defer p.mu.Unlock()
	return append([]platform.OutgoingMessage(nil), p.sent...)
}

// EnsureEvent 造一条最简入站事件，方便单测少写几行。
func EnsureEvent(sceneID, text string) platform.IncomingMessage {
	return platform.IncomingMessage{
		MsgID:     "mock-msg-1",
		Conv:      platform.ConversationKey{AppID: "app1", Scene: platform.SceneC2C, SceneID: sceneID},
		Sender:    platform.Sender{OpenID: sceneID},
		Text:      text,
		Timestamp: time.Now(),
	}
}
