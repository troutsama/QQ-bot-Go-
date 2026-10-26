package chat

import (
	"sync"
	"time"
)

// Dedup 是进程内的短期去重表，挡住平台的重推（R2）与同一消息的并发处理。
//
// 它只是**快路径**：真相仍然是 turns 上的 UNIQUE (conversation_id, inbound_msg_id)。
// 进程重启后 map 会清空，重复消息由 BeginTurn 的 IsNew=false 兜底。
//
// 之所以需要它：同一条消息重推时，如果每次都先打 DB 再判断，
// 在 LLM 尚未返回的窗口里会产生两条 processing 轮；内存表把绝大多数重复挡在 DB 之前。
type Dedup struct {
	mu   sync.Mutex
	seen map[string]time.Time
	ttl  time.Duration
	now  func() time.Time
}

// NewDedup 创建去重表。ttl <= 0 时用 10 分钟，now 为 nil 时用 time.Now。
func NewDedup(ttl time.Duration, now func() time.Time) *Dedup {
	if ttl <= 0 {
		ttl = 10 * time.Minute
	}
	if now == nil {
		now = time.Now
	}
	return &Dedup{
		seen: make(map[string]time.Time),
		ttl:  ttl,
		now:  now,
	}
}

// Seen 记录 key 并返回它此前是否已经出现过。
//
// 先清理过期项再查询，所以过期的 key 会重新返回 false。
// V1 的消息量很小，每次 O(n) 清理可以接受；量级上来后应改成定时清理，
// 否则单次 Seen 的耗时随表大小线性增长。
func (d *Dedup) Seen(key string) bool {
	d.mu.Lock()
	defer d.mu.Unlock()

	// 在锁内取时间：注入的 now 可能不是并发安全的（测试里就常是闭包变量），
	// 而 Seen 本身会被多个 shard worker 同时调用。
	now := d.now()
	for k, at := range d.seen {
		if now.Sub(at) >= d.ttl {
			delete(d.seen, k)
		}
	}
	if _, ok := d.seen[key]; ok {
		return true
	}
	d.seen[key] = now
	return false
}
