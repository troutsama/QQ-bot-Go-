package chat

import (
	"fmt"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestDedup_SeenAndTTL(t *testing.T) {
	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	d := NewDedup(time.Minute, func() time.Time { return now })

	if d.Seen("k") {
		t.Fatal("第一次出现不应判为重复")
	}
	if !d.Seen("k") {
		t.Fatal("TTL 内第二次出现应判为重复")
	}

	now = now.Add(2 * time.Minute)
	if d.Seen("k") {
		t.Fatal("超过 TTL 后应重新视为首次出现")
	}
}

func TestDedup_CleansExpiredEntries(t *testing.T) {
	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	d := NewDedup(time.Minute, func() time.Time { return now })

	d.Seen("old")
	now = now.Add(2 * time.Minute)
	d.Seen("new")

	d.mu.Lock()
	_, oldStillThere := d.seen["old"]
	d.mu.Unlock()
	if oldStillThere {
		t.Fatal("过期 key 应被清理")
	}
}

func TestDedup_ConcurrentOnlyOneFirst(t *testing.T) {
	const (
		goroutines = 100
		keys       = 10
	)
	d := NewDedup(time.Hour, time.Now)

	var firsts atomic.Int32
	var wg sync.WaitGroup
	for i := 0; i < goroutines; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			if !d.Seen(fmt.Sprintf("key-%d", i%keys)) {
				firsts.Add(1)
			}
		}(i)
	}
	wg.Wait()

	if got := firsts.Load(); got != keys {
		t.Fatalf("首次出现次数 = %d，期望 %d（每个 key 恰好一次）", got, keys)
	}
}
