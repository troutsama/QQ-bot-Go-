package main

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"qq-bot/internal/config"
	"qq-bot/internal/platform/provider/mock"
	"qq-bot/internal/platform/provider/qqbot"
	"qq-bot/internal/store"
)

// TestEchoLoopPersist 端到端：mock 注入一条消息 → echoLoop 跑完收发入库 → 断言落库。
func TestEchoLoopPersist(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	// 1. 临时库
	st, err := store.Open(config.DatabaseConfig{
		Driver:        "sqlite",
		Path:          filepath.Join(t.TempDir(), "bot_test.db"),
		MaxOpenConns:  1,
		MaxIdleConns:  1,
		BusyTimeoutMS: 3000,
		InitSchema:    true,
	})
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer st.Close()

	// 2. mock 平台
	p := mock.New()
	if err := p.Start(ctx); err != nil {
		t.Fatalf("Start: %v", err)
	}

	// 3. goroutine 跑业务循环（日志喂 nil，函数内有兜底）
	//    qqbot.Run 只依赖 platform.Provider 接口和 store，所以能直接拿 mock 假数据跑同一条链路。
	done := make(chan error, 1)
	go func() { done <- qqbot.Run(ctx, p, st, nil) }()

	// 4. 注入一条入站消息
	if err := p.Inject(ctx, mock.EnsureEvent("u_test_1", "你好")); err != nil {
		t.Fatalf("Inject: %v", err)
	}

	// 5. 轮询等 turns 落库（3 秒超时）
	var turns int
	for deadline := time.Now().Add(3 * time.Second); time.Now().Before(deadline); {
		if err := st.DB().QueryRow(`SELECT COUNT(*) FROM turns`).Scan(&turns); err != nil {
			t.Fatalf("查询 turns: %v", err)
		}
		if turns >= 1 {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if turns != 1 {
		t.Fatalf("超时：turns 未落库")
	}

	// 6. 断言 messages 两行
	var rows int
	if err := st.DB().QueryRow(`SELECT COUNT(*) FROM messages`).Scan(&rows); err != nil {
		t.Fatalf("查询 messages: %v", err)
	}
	if rows != 2 {
		t.Fatalf("messages 应为 2（user+assistant），实际 %d", rows)
	}

	// 7. 断言行序：user 行 seq=1、assistant 行 seq=2
	var seq1, seq2 int
	var role1, role2 string
	if err := st.DB().QueryRow(`SELECT seq_in_turn, role FROM messages WHERE turn_id = 1 AND seq_in_turn = 1`).Scan(&seq1, &role1); err != nil {
		t.Fatalf("第 1 行: %v", err)
	}
	if err := st.DB().QueryRow(`SELECT seq_in_turn, role FROM messages WHERE turn_id = 1 AND seq_in_turn = 2`).Scan(&seq2, &role2); err != nil {
		t.Fatalf("第 2 行: %v", err)
	}
	if seq1 != 1 || role1 != "user" || seq2 != 2 || role2 != "assistant" {
		t.Errorf("行序/角色不对: (seq=%d,role=%q) (seq=%d,role=%q)", seq1, role1, seq2, role2)
	}

	// 8. 断言轮次状态 ok
	var status string
	if err := st.DB().QueryRow(`SELECT status FROM turns WHERE id = 1`).Scan(&status); err != nil {
		t.Fatalf("查询状态: %v", err)
	}
	if status != "ok" {
		t.Errorf("turns.status 应为 ok，实际 %q", status)
	}

	// 9. 收尾
	p.Stop(ctx)
	<-done
}
