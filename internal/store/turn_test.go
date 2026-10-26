package store_test

import (
	"context"
	"sync"
	"testing"
	"time"

	"qq-bot/internal/chat"
	"qq-bot/internal/store"
)

func beginTurnAt(t *testing.T, s *store.Store, convID int64, msgID, text string, started time.Time) chat.TurnRef {
	t.Helper()
	ref, err := s.BeginTurn(context.Background(), chat.TurnParams{
		ConvID:    convID,
		MsgID:     msgID,
		UserText:  text,
		TraceID:   "trace-" + msgID,
		StartedAt: started,
	})
	if err != nil {
		t.Fatalf("BeginTurn(%s) 失败: %v", msgID, err)
	}
	return ref
}

func beginTurn(t *testing.T, s *store.Store, convID int64, msgID, text string) chat.TurnRef {
	t.Helper()
	return beginTurnAt(t, s, convID, msgID, text, time.Now())
}

func TestBeginTurn_WritesTurnAndUserMessage(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	convID := mustConversation(t, s)

	ref := beginTurn(t, s, convID, "msg-1", "你好")
	if !ref.IsNew {
		t.Fatal("首次 BeginTurn 的 IsNew 应为 true")
	}
	if ref.ID == 0 {
		t.Fatal("首次 BeginTurn 应返回非零 ID")
	}
	if ref.Status != chat.StatusProcessing {
		t.Fatalf("状态 = %q，期望 %q", ref.Status, chat.StatusProcessing)
	}

	var (
		status   string
		userText string
		traceID  string
		finished *int64
	)
	if err := s.DB().QueryRowContext(ctx, `
		SELECT status, user_text, trace_id, finished_at FROM turns WHERE id = ?`, ref.ID,
	).Scan(&status, &userText, &traceID, &finished); err != nil {
		t.Fatalf("读取轮次失败: %v", err)
	}
	if status != string(chat.StatusProcessing) || userText != "你好" || traceID != "trace-msg-1" {
		t.Fatalf("轮次字段不正确: status=%q user_text=%q trace=%q", status, userText, traceID)
	}
	if finished != nil {
		t.Fatal("processing 轮的 finished_at 应为 NULL")
	}

	var (
		role    string
		seq     int
		content string
	)
	if err := s.DB().QueryRowContext(ctx, `
		SELECT role, seq_in_turn, content FROM messages WHERE turn_id = ?`, ref.ID,
	).Scan(&role, &seq, &content); err != nil {
		t.Fatalf("读取 user 消息失败: %v", err)
	}
	if role != "user" || seq != 0 || content != "你好" {
		t.Fatalf("user 消息不正确: role=%q seq=%d content=%q", role, seq, content)
	}
}

func TestBeginTurn_DuplicateReturnsExisting(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	convID := mustConversation(t, s)

	first := beginTurn(t, s, convID, "msg-1", "你好")
	second := beginTurn(t, s, convID, "msg-1", "你好")

	if second.IsNew {
		t.Fatal("重复 msg_id 的 IsNew 应为 false")
	}
	if second.ID != first.ID {
		t.Fatalf("重复 msg_id 返回了不同轮次: %d vs %d", first.ID, second.ID)
	}

	var turnCount, msgCount int
	if err := s.DB().QueryRowContext(ctx, `SELECT count(*) FROM turns`).Scan(&turnCount); err != nil {
		t.Fatalf("统计轮次失败: %v", err)
	}
	if err := s.DB().QueryRowContext(ctx, `SELECT count(*) FROM messages`).Scan(&msgCount); err != nil {
		t.Fatalf("统计消息失败: %v", err)
	}
	if turnCount != 1 || msgCount != 1 {
		t.Fatalf("重复推送后 turns=%d messages=%d，期望各 1", turnCount, msgCount)
	}
}

func TestBeginTurn_ConcurrentSameMsgID(t *testing.T) {
	s := newTestStoreN(t, 4)
	ctx := context.Background()
	convID := mustConversation(t, s)

	const n = 8
	var (
		wg   sync.WaitGroup
		refs = make([]chat.TurnRef, n)
		errs = make([]error, n)
	)
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			refs[i], errs[i] = s.BeginTurn(ctx, chat.TurnParams{
				ConvID:   convID,
				MsgID:    "msg-dup",
				UserText: "你好",
				TraceID:  "trace-dup",
			})
		}(i)
	}
	wg.Wait()

	created := 0
	for i := 0; i < n; i++ {
		if errs[i] != nil {
			t.Fatalf("第 %d 个 goroutine 失败: %v", i, errs[i])
		}
		if refs[i].ID != refs[0].ID {
			t.Fatalf("并发 BeginTurn 得到了不同轮次 ID: %v", refs)
		}
		if refs[i].IsNew {
			created++
		}
	}
	if created != 1 {
		t.Fatalf("IsNew=true 的次数 = %d，期望恰好 1", created)
	}

	var turnCount, msgCount int
	if err := s.DB().QueryRowContext(ctx, `SELECT count(*) FROM turns`).Scan(&turnCount); err != nil {
		t.Fatalf("统计轮次失败: %v", err)
	}
	if err := s.DB().QueryRowContext(ctx, `SELECT count(*) FROM messages`).Scan(&msgCount); err != nil {
		t.Fatalf("统计消息失败: %v", err)
	}
	if turnCount != 1 || msgCount != 1 {
		t.Fatalf("并发后 turns=%d messages=%d，期望各 1", turnCount, msgCount)
	}
}

func TestFinishTurn_MarksOKAndAppendsAssistant(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	convID := mustConversation(t, s)

	ref := beginTurn(t, s, convID, "msg-1", "你好")
	if err := s.FinishTurn(ctx, ref.ID, "你好呀"); err != nil {
		t.Fatalf("FinishTurn 失败: %v", err)
	}

	var (
		status   string
		reply    string
		finished *int64
	)
	if err := s.DB().QueryRowContext(ctx,
		`SELECT status, reply_text, finished_at FROM turns WHERE id = ?`, ref.ID,
	).Scan(&status, &reply, &finished); err != nil {
		t.Fatalf("读取轮次失败: %v", err)
	}
	if status != string(chat.StatusOK) || reply != "你好呀" || finished == nil {
		t.Fatalf("结束后的轮次不正确: status=%q reply=%q finished=%v", status, reply, finished)
	}

	rows, err := s.DB().QueryContext(ctx,
		`SELECT role, seq_in_turn, content FROM messages WHERE turn_id = ? ORDER BY seq_in_turn`, ref.ID)
	if err != nil {
		t.Fatalf("读取消息失败: %v", err)
	}
	defer rows.Close()

	var want = []struct {
		role    string
		seq     int
		content string
	}{
		{"user", 0, "你好"},
		{"assistant", 1, "你好呀"},
	}
	i := 0
	for rows.Next() {
		var role, content string
		var seq int
		if err := rows.Scan(&role, &seq, &content); err != nil {
			t.Fatalf("扫描消息失败: %v", err)
		}
		if i >= len(want) || role != want[i].role || seq != want[i].seq || content != want[i].content {
			t.Fatalf("第 %d 条消息不正确: %s/%d/%q", i, role, seq, content)
		}
		i++
	}
	if i != len(want) {
		t.Fatalf("消息条数 = %d，期望 %d", i, len(want))
	}

	turns, err := s.RecentTurns(ctx, convID, 10)
	if err != nil {
		t.Fatalf("RecentTurns 失败: %v", err)
	}
	if len(turns) != 1 || turns[0].ID != ref.ID {
		t.Fatalf("完成后的轮本应进入上下文窗口，实际: %+v", turns)
	}
}

func TestFinishTurn_SecondCallFails(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	convID := mustConversation(t, s)

	ref := beginTurn(t, s, convID, "msg-1", "你好")
	if err := s.FinishTurn(ctx, ref.ID, "第一次"); err != nil {
		t.Fatalf("首次 FinishTurn 失败: %v", err)
	}
	if err := s.FinishTurn(ctx, ref.ID, "第二次"); err == nil {
		t.Fatal("重复结束同一轮应当报错，否则会写入第二条 assistant 消息")
	}

	var n int
	if err := s.DB().QueryRowContext(ctx,
		`SELECT count(*) FROM messages WHERE turn_id = ? AND role = 'assistant'`, ref.ID,
	).Scan(&n); err != nil {
		t.Fatalf("统计 assistant 消息失败: %v", err)
	}
	if n != 1 {
		t.Fatalf("assistant 消息条数 = %d，期望 1", n)
	}
}

func TestFailTurn_ExcludedFromContext(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	convID := mustConversation(t, s)

	ref := beginTurn(t, s, convID, "msg-1", "你好")
	if err := s.FailTurn(ctx, ref.ID); err != nil {
		t.Fatalf("FailTurn 失败: %v", err)
	}

	var (
		status   string
		finished *int64
	)
	if err := s.DB().QueryRowContext(ctx,
		`SELECT status, finished_at FROM turns WHERE id = ?`, ref.ID,
	).Scan(&status, &finished); err != nil {
		t.Fatalf("读取轮次失败: %v", err)
	}
	if status != string(chat.StatusFailed) || finished == nil {
		t.Fatalf("失败轮状态不正确: status=%q finished=%v", status, finished)
	}

	turns, err := s.RecentTurns(ctx, convID, 10)
	if err != nil {
		t.Fatalf("RecentTurns 失败: %v", err)
	}
	if len(turns) != 0 {
		t.Fatalf("失败轮不应进入上下文，实际返回 %d 轮", len(turns))
	}
}

func TestFilterTurn_ExcludedFromContext(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	convID := mustConversation(t, s)

	ref := beginTurn(t, s, convID, "m-cmd", "/help")
	if err := s.FilterTurn(ctx, ref.ID); err != nil {
		t.Fatalf("FilterTurn 失败: %v", err)
	}

	var (
		status   string
		finished *int64
	)
	if err := s.DB().QueryRowContext(ctx,
		`SELECT status, finished_at FROM turns WHERE id = ?`, ref.ID,
	).Scan(&status, &finished); err != nil {
		t.Fatalf("读取轮次失败: %v", err)
	}
	if status != string(chat.StatusFiltered) || finished == nil {
		t.Fatalf("过滤轮状态不正确: status=%q finished=%v", status, finished)
	}

	// filtered 轮不写 assistant 消息，也不进上下文。
	var replies int
	if err := s.DB().QueryRowContext(ctx,
		`SELECT count(*) FROM messages WHERE turn_id = ? AND role = 'assistant'`, ref.ID,
	).Scan(&replies); err != nil {
		t.Fatalf("统计 assistant 消息失败: %v", err)
	}
	if replies != 0 {
		t.Fatalf("filtered 轮不应有 assistant 消息，实际 %d 条", replies)
	}

	turns, err := s.RecentTurns(ctx, convID, 10)
	if err != nil {
		t.Fatalf("RecentTurns 失败: %v", err)
	}
	if len(turns) != 0 {
		t.Fatalf("filtered 轮不应进入上下文，实际返回 %d 轮", len(turns))
	}

	if err := s.FilterTurn(ctx, ref.ID); err == nil {
		t.Fatal("重复标记同一轮应当报错")
	}
}

func TestRecentTurns_OnlyOKWholeTurnInOrder(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	convID := mustConversation(t, s)

	// 轮 1：ok
	t1 := beginTurn(t, s, convID, "m1", "问题一")
	if err := s.FinishTurn(ctx, t1.ID, "回答一"); err != nil {
		t.Fatalf("FinishTurn(t1) 失败: %v", err)
	}

	// 轮 2：failed
	t2 := beginTurn(t, s, convID, "m2", "问题二")
	if err := s.FailTurn(ctx, t2.ID); err != nil {
		t.Fatalf("FailTurn(t2) 失败: %v", err)
	}

	// 轮 3：filtered（V2 命令轮走这条路径）
	t3 := beginTurn(t, s, convID, "m3", "/help")
	if err := s.FilterTurn(ctx, t3.ID); err != nil {
		t.Fatalf("FilterTurn 失败: %v", err)
	}

	// 轮 4：ok，且模拟 V2 的工具调用（user + assistant + tool + assistant 共 4 条）
	t4 := beginTurn(t, s, convID, "m4", "带工具的问题")
	if err := s.FinishTurn(ctx, t4.ID, "最终回答"); err != nil {
		t.Fatalf("FinishTurn(t4) 失败: %v", err)
	}
	now := time.Now().UnixMilli()
	for _, m := range []struct {
		seq     int
		role    string
		content string
	}{
		{2, "tool", `{"result":"天气晴"}`},
		{3, "assistant", "补充回答"},
	} {
		if _, err := s.DB().ExecContext(ctx, `
			INSERT INTO messages (turn_id, seq_in_turn, role, content, created_at)
			VALUES (?, ?, ?, ?, ?)`, t4.ID, m.seq, m.role, m.content, now); err != nil {
			t.Fatalf("插入工具消息失败: %v", err)
		}
	}

	turns, err := s.RecentTurns(ctx, convID, 10)
	if err != nil {
		t.Fatalf("RecentTurns 失败: %v", err)
	}
	if len(turns) != 2 {
		t.Fatalf("返回轮数 = %d，期望 2（只有 ok 轮）", len(turns))
	}
	if turns[0].ID != t1.ID || turns[1].ID != t4.ID {
		t.Fatalf("轮次顺序不正确: 期望 [%d %d]，实际 [%d %d]",
			t1.ID, t4.ID, turns[0].ID, turns[1].ID)
	}
	if len(turns[1].Messages) != 4 {
		t.Fatalf("轮 4 的消息条数 = %d，期望 4（整轮取全）", len(turns[1].Messages))
	}
	for i, m := range turns[1].Messages {
		if m.SeqInTurn != i {
			t.Fatalf("第 %d 条消息 seq_in_turn = %d，期望 %d", i, m.SeqInTurn, i)
		}
	}
}

func TestRecentTurns_RespectsLimit(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	convID := mustConversation(t, s)

	var ids []int64
	for _, msgID := range []string{"m1", "m2", "m3"} {
		ref := beginTurn(t, s, convID, msgID, "问题"+msgID)
		if err := s.FinishTurn(ctx, ref.ID, "回答"+msgID); err != nil {
			t.Fatalf("FinishTurn 失败: %v", err)
		}
		ids = append(ids, ref.ID)
	}

	turns, err := s.RecentTurns(ctx, convID, 2)
	if err != nil {
		t.Fatalf("RecentTurns 失败: %v", err)
	}
	if len(turns) != 2 {
		t.Fatalf("返回轮数 = %d，期望 2", len(turns))
	}
	if turns[0].ID != ids[1] || turns[1].ID != ids[2] {
		t.Fatalf("limit 取到的不是最近两轮: %+v", turns)
	}
}

func TestStaleTurns(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	convID := mustConversation(t, s)

	old := beginTurnAt(t, s, convID, "m-old", "旧问题", time.Now().Add(-10*time.Minute))
	fresh := beginTurn(t, s, convID, "m-fresh", "新问题")

	refs, err := s.StaleTurns(ctx, convID, 5*time.Minute)
	if err != nil {
		t.Fatalf("StaleTurns 失败: %v", err)
	}
	if len(refs) != 1 || refs[0].ID != old.ID {
		t.Fatalf("残轮 = %+v，期望只有 %d", refs, old.ID)
	}

	// 已结束的轮不是残轮
	if err := s.FinishTurn(ctx, fresh.ID, "回答"); err != nil {
		t.Fatalf("FinishTurn 失败: %v", err)
	}
	refs, err = s.StaleTurns(ctx, convID, 5*time.Minute)
	if err != nil {
		t.Fatalf("StaleTurns 失败: %v", err)
	}
	if len(refs) != 1 || refs[0].ID != old.ID {
		t.Fatalf("残轮 = %+v，期望只有 %d", refs, old.ID)
	}
}
