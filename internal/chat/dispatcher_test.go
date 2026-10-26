package chat

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"qq-bot/internal/platform"
)

func c2cKey(sceneID string) platform.ConversationKey {
	return platform.ConversationKey{AppID: "app", Scene: platform.SceneC2C, SceneID: sceneID}
}

// keyForShard 找一个确实落在指定分片的会话键。
// 用真实哈希而不是构造假的 shardOf，测的才是生产路径。
func keyForShard(t *testing.T, target, shards int) platform.ConversationKey {
	t.Helper()
	for i := 0; i < 100000; i++ {
		k := c2cKey(fmt.Sprintf("user-%d", i))
		if shardOf(k, shards) == target {
			return k
		}
	}
	t.Fatalf("找不到落在分片 %d 的会话键", target)
	return platform.ConversationKey{}
}

func TestDispatcher_SameConversationSerialOrder(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	var (
		mu   sync.Mutex
		got  []string
		done = make(chan struct{}, 3)
	)
	d := NewDispatcher(8, 16, func(_ context.Context, msg platform.IncomingMessage) {
		time.Sleep(10 * time.Millisecond) // 放大并发窗口，顺序错了更容易暴露
		mu.Lock()
		got = append(got, msg.MsgID)
		mu.Unlock()
		done <- struct{}{}
	})

	runDone := make(chan struct{})
	go func() {
		d.Run(ctx)
		close(runDone)
	}()

	key := c2cKey("u1")
	for _, id := range []string{"m1", "m2", "m3"} {
		if err := d.Submit(ctx, platform.IncomingMessage{MsgID: id, Conv: key}); err != nil {
			t.Fatalf("Submit(%s) 失败: %v", id, err)
		}
	}
	for i := 0; i < 3; i++ {
		select {
		case <-done:
		case <-time.After(time.Second):
			t.Fatal("等待消息处理超时")
		}
	}

	cancel()
	select {
	case <-runDone:
	case <-time.After(time.Second):
		t.Fatal("ctx 取消后 Run 没有返回")
	}

	mu.Lock()
	defer mu.Unlock()
	if strings.Join(got, ",") != "m1,m2,m3" {
		t.Fatalf("处理顺序 = %v，期望 [m1 m2 m3]", got)
	}
}

func TestDispatcher_DifferentShardsRunConcurrently(t *testing.T) {
	const shards = 8
	k1 := keyForShard(t, 0, shards)
	k2 := keyForShard(t, 1, shards)
	if shardOf(k1, shards) == shardOf(k2, shards) {
		t.Fatal("测试前提不成立：两个 key 落到了同一分片")
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	started := make(chan string, 2)
	release := make(chan struct{})
	d := NewDispatcher(shards, 2, func(_ context.Context, msg platform.IncomingMessage) {
		started <- msg.Conv.SceneID
		<-release
	})

	runDone := make(chan struct{})
	go func() {
		d.Run(ctx)
		close(runDone)
	}()

	if err := d.Submit(ctx, platform.IncomingMessage{MsgID: "a", Conv: k1}); err != nil {
		t.Fatalf("Submit(k1) 失败: %v", err)
	}
	if err := d.Submit(ctx, platform.IncomingMessage{MsgID: "b", Conv: k2}); err != nil {
		t.Fatalf("Submit(k2) 失败: %v", err)
	}

	// 如果两个会话被串行处理，第二个 handler 永远进不来，这里会超时。
	for i := 0; i < 2; i++ {
		select {
		case <-started:
		case <-time.After(time.Second):
			t.Fatal("不同分片没有并行执行（第二个 handler 未启动）")
		}
	}
	close(release)

	cancel()
	select {
	case <-runDone:
	case <-time.After(time.Second):
		t.Fatal("ctx 取消后 Run 没有返回")
	}
}

func TestDispatcher_SubmitRespectsCancel(t *testing.T) {
	d := NewDispatcher(1, 1, func(context.Context, platform.IncomingMessage) {})
	key := c2cKey("u1")

	// 没有 Run，队列容量仅为 1：第一条占满，第二条只能等 ctx。
	if err := d.Submit(context.Background(), platform.IncomingMessage{MsgID: "m1", Conv: key}); err != nil {
		t.Fatalf("第一次 Submit 失败: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	err := d.Submit(ctx, platform.IncomingMessage{MsgID: "m2", Conv: key})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("队列满且 ctx 已取消时应返回 context.Canceled，实际 %v", err)
	}
}

func TestDispatcher_RunReturnsOnCancel(t *testing.T) {
	d := NewDispatcher(2, 1, func(context.Context, platform.IncomingMessage) {})
	ctx, cancel := context.WithCancel(context.Background())

	done := make(chan struct{})
	go func() {
		d.Run(ctx)
		close(done)
	}()

	cancel()
	select {
	case <-done:
	case <-time.After(500 * time.Millisecond):
		t.Fatal("ctx 取消后 Run 没有及时返回")
	}
}
