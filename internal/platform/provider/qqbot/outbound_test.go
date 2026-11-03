package qqbot

import (
	"context"
	"log/slog"
	"path/filepath"
	"testing"
	"time"

	"github.com/tencent-connect/botgo/dto"
	"github.com/tencent-connect/botgo/openapi"
	"github.com/tencent-connect/botgo/openapi/options"

	"qq-bot/internal/config"
	"qq-bot/internal/platform"
	"qq-bot/internal/store"
)

// fakeAPI 只实现 PostC2CMessage，靠「内嵌 openapi.OpenAPI 接口」满足类型约束。
// 被覆盖之外的方法若被调用会 panic（nil 接口）——本测试只走 Send → PostC2CMessage，安全。
type fakeAPI struct {
	openapi.OpenAPI
}

func (fakeAPI) PostC2CMessage(ctx context.Context, userID string, msg dto.APIMessage, opt ...options.Option) (*dto.Message, error) {
	return &dto.Message{ID: "test_sent_id"}, nil
}

// TestHandleMessagePersist 验证 Plan A 业务循环的单条处理：
// handleMessage 跑完 ①~⑤ → 断言 turns/messages 落库、行序、状态 ok。
// 直接同步调 handleMessage，不起 worker goroutine，断言确定、无需轮询。
func TestHandleMessagePersist(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	// 1. 临时真库（*store.Store 隐式实现 MessageStore）
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

	// 2. 手搓 Provider：绕开 New（避免真实 token/openapi/WS）。
	//    只填 handleMessage → Send 这条路径碰到的字段。
	p := &Provider{
		log:     slog.Default(),
		api:     fakeAPI{},
		persist: st,
		status:  platform.StatusConnected, // Send 会校验 Status==Connected
		closed:  false,
	}

	// 3. 构造一条 C2C 入站消息，直接喂 handleMessage
	msg := platform.IncomingMessage{
		MsgID: "m_test_1",
		Conv: platform.ConversationKey{
			AppID:   "test_app",
			Scene:   platform.SceneC2C,
			SceneID: "u_test_1",
		},
		Text:      "你好",
		Timestamp: time.Now(),
	}
	p.handleMessage(ctx, msg)

	// 4. 断言 turns 一行
	var turns int
	if err := st.DB().QueryRow(`SELECT COUNT(*) FROM turns`).Scan(&turns); err != nil {
		t.Fatalf("查询 turns: %v", err)
	}
	if turns != 1 {
		t.Fatalf("turns 应为 1，实际 %d", turns)
	}

	// 5. 断言 messages 两行
	var rows int
	if err := st.DB().QueryRow(`SELECT COUNT(*) FROM messages`).Scan(&rows); err != nil {
		t.Fatalf("查询 messages: %v", err)
	}
	if rows != 2 {
		t.Fatalf("messages 应为 2（user+assistant），实际 %d", rows)
	}

	// 6. 断言行序：user seq=1、assistant seq=2
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

	// 7. 断言轮次状态 ok
	var status string
	if err := st.DB().QueryRow(`SELECT status FROM turns WHERE id = 1`).Scan(&status); err != nil {
		t.Fatalf("查询状态: %v", err)
	}
	if status != "ok" {
		t.Errorf("turns.status 应为 ok，实际 %q", status)
	}
}
