package chat

import (
	"context"
	"hash/fnv"
	"sync"

	"qq-bot/internal/platform"
)

const (
	defaultShardCount     = 8
	defaultShardQueueSize = 16
)

// Dispatcher 是按会话分片的串行队列（R3 的对策）。
//
// 同一个 ConversationKey 一定落到同一个 shard，所以同会话消息串行处理；
// 不同会话尽量落到不同 shard，因此仍然并行。
//
// channel **永远不关闭**：worker 退出靠 ctx.Done()，Submit 靠 ctx 解除阻塞。
// 关闭 channel 会让「消费者已退出但生产者还在 Submit」变成 panic，而 ctx 取消
// 是单向的，所有参与者都能安全观察到。
type Dispatcher struct {
	shards []chan platform.IncomingMessage
	handle func(context.Context, platform.IncomingMessage)
	wg     sync.WaitGroup
}

// NewDispatcher 创建分片队列。shards / queueSize <= 0 时使用默认值。
func NewDispatcher(shards, queueSize int, handle func(context.Context, platform.IncomingMessage)) *Dispatcher {
	if shards <= 0 {
		shards = defaultShardCount
	}
	if queueSize <= 0 {
		queueSize = defaultShardQueueSize
	}
	d := &Dispatcher{
		shards: make([]chan platform.IncomingMessage, shards),
		handle: handle,
	}
	for i := range d.shards {
		d.shards[i] = make(chan platform.IncomingMessage, queueSize)
	}
	return d
}

// Run 启动全部 worker，并阻塞到 ctx 取消、所有 worker 退出为止。
// 同一个 Dispatcher 只应 Run 一次；重启由上层构造新实例。
func (d *Dispatcher) Run(ctx context.Context) {
	for i := range d.shards {
		d.wg.Add(1)
		go func(ch <-chan platform.IncomingMessage) {
			defer d.wg.Done()
			for {
				select {
				case <-ctx.Done():
					return
				case msg, ok := <-ch:
					if !ok {
						return // 防御性分支：本实现不会关闭 channel
					}
					// ctx 可能恰好在 select 期间被取消；此时不再拿新消息，
					// 避免用已取消的 ctx 去写库、调模型。
					if ctx.Err() != nil {
						return
					}
					d.handle(ctx, msg)
				}
			}
		}(d.shards[i])
	}
	d.wg.Wait()
}

// Submit 把消息放进对应分片。队列满时阻塞（天然背压），但响应 ctx 取消。
func (d *Dispatcher) Submit(ctx context.Context, msg platform.IncomingMessage) error {
	ch := d.shards[shardOf(msg.Conv, len(d.shards))]
	select {
	case ch <- msg:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

// shardOf 用 FNV-1a 把会话键映射到分片。
//
// 不直接用 map 分配 shard：FNV 是稳定哈希，进程重启后同一会话仍落同一分片；
// 这也让分片函数可以被单测直接验证，不依赖运行期状态。
func shardOf(key platform.ConversationKey, shards int) int {
	h := fnv.New32a()
	_, _ = h.Write([]byte(key.AppID + "|" + string(key.Scene) + "|" + key.SceneID))
	return int(h.Sum32() % uint32(shards))
}
