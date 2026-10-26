package store_test

import (
	"context"
	"sync"
	"testing"

	"qq-bot/internal/platform"
	"qq-bot/internal/store"
)

func testKey() platform.ConversationKey {
	return platform.ConversationKey{AppID: "app1", Scene: platform.SceneC2C, SceneID: "openid-1"}
}

// mustConversation 在测试里取默认会话的 ID。
func mustConversation(t *testing.T, s *store.Store) int64 {
	t.Helper()
	conv, _, err := s.GetOrCreateConversation(context.Background(), testKey())
	if err != nil {
		t.Fatalf("GetOrCreateConversation 失败: %v", err)
	}
	return conv.ID
}

func TestGetOrCreateConversation_CreatesThenReuses(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()

	conv, isNew, err := s.GetOrCreateConversation(ctx, testKey())
	if err != nil {
		t.Fatalf("首次获取会话失败: %v", err)
	}
	if !isNew {
		t.Fatal("首次调用 isNew 应为 true")
	}
	if conv.ID == 0 || conv.AppID != "app1" || conv.Scene != platform.SceneC2C || conv.SceneID != "openid-1" {
		t.Fatalf("会话字段不正确: %+v", conv)
	}

	again, isNew, err := s.GetOrCreateConversation(ctx, testKey())
	if err != nil {
		t.Fatalf("再次获取会话失败: %v", err)
	}
	if isNew {
		t.Fatal("重复调用 isNew 应为 false")
	}
	if again.ID != conv.ID {
		t.Fatalf("两次返回的会话 ID 不同: %d vs %d", conv.ID, again.ID)
	}

	var n int
	if err := s.DB().QueryRowContext(ctx, `SELECT count(*) FROM conversations`).Scan(&n); err != nil {
		t.Fatalf("统计会话失败: %v", err)
	}
	if n != 1 {
		t.Fatalf("conversations 行数 = %d，期望 1", n)
	}
}

func TestGetOrCreateConversation_DistinguishesKeys(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()

	keys := []platform.ConversationKey{
		{AppID: "app1", Scene: platform.SceneC2C, SceneID: "openid-1"},
		{AppID: "app2", Scene: platform.SceneC2C, SceneID: "openid-1"}, // 换 AppID（R6）
		{AppID: "app1", Scene: platform.SceneGroup, SceneID: "openid-1"},
		{AppID: "app1", Scene: platform.SceneC2C, SceneID: "openid-2"},
	}

	seen := make(map[int64]bool, len(keys))
	for _, key := range keys {
		conv, isNew, err := s.GetOrCreateConversation(ctx, key)
		if err != nil {
			t.Fatalf("获取会话 %v 失败: %v", key, err)
		}
		if !isNew {
			t.Fatalf("会话 %v 本应新建", key)
		}
		if seen[conv.ID] {
			t.Fatalf("不同 key 复用了同一个会话 ID %d", conv.ID)
		}
		seen[conv.ID] = true
	}
}

func TestGetOrCreateConversation_Concurrent(t *testing.T) {
	s := newTestStoreN(t, 4)
	ctx := context.Background()

	const n = 8
	var (
		wg   sync.WaitGroup
		ids  = make([]int64, n)
		news = make([]bool, n)
		errs = make([]error, n)
	)
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			conv, isNew, err := s.GetOrCreateConversation(ctx, testKey())
			if err != nil {
				errs[i] = err
				return
			}
			ids[i] = conv.ID
			news[i] = isNew
		}(i)
	}
	wg.Wait()

	var created int
	for i := 0; i < n; i++ {
		if errs[i] != nil {
			t.Fatalf("第 %d 个 goroutine 失败: %v", i, errs[i])
		}
		if ids[i] != ids[0] {
			t.Fatalf("并发创建得到了不同会话 ID: %v", ids)
		}
		if news[i] {
			created++
		}
	}
	if created != 1 {
		t.Fatalf("isNew=true 的次数 = %d，期望恰好 1", created)
	}

	var count int
	if err := s.DB().QueryRowContext(ctx, `SELECT count(*) FROM conversations`).Scan(&count); err != nil {
		t.Fatalf("统计会话失败: %v", err)
	}
	if count != 1 {
		t.Fatalf("并发后 conversations 行数 = %d，期望 1", count)
	}
}
