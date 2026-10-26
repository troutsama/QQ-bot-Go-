package chat_test

import (
	"context"
	"database/sql"
	"errors"
	"io"
	"log/slog"
	"path/filepath"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"qq-bot/internal/chat"
	"qq-bot/internal/config"
	"qq-bot/internal/llm"
	llmfake "qq-bot/internal/llm/provider/fake"
	"qq-bot/internal/platform/provider/mock"
	"qq-bot/internal/store"
)

const testNonTextHint = "目前只支持文字消息哦～"

type testEnv struct {
	t    *testing.T
	svc  *chat.Service
	prov *mock.Provider
	fake *llmfake.Client
	st   *store.Store
	cfg  config.Config
}

func testServiceConfig() config.Config {
	return config.Config{
		Chat: config.ChatConfig{
			ContextTurns:      10,
			ContextTimePrefix: false,
			Timezone:          "UTC",
			MaxReplySegments:  3,
			SegmentMaxChars:   100,
		},
		LLM: config.LLMConfig{
			Model:        "test-model",
			SystemPrompt: "你是测试助手",
			MaxTokens:    256,
			Temperature:  0.2,
		},
	}
}

func newTestEnv(t *testing.T) *testEnv {
	t.Helper()

	dbCfg := config.DatabaseConfig{
		Driver:        "sqlite",
		Path:          filepath.Join(t.TempDir(), "test.db"),
		MaxOpenConns:  1,
		MaxIdleConns:  1,
		BusyTimeoutMS: 3000,
	}
	st, err := store.Open(dbCfg)
	if err != nil {
		t.Fatalf("打开数据库失败: %v", err)
	}
	if err := st.Migrate(context.Background()); err != nil {
		_ = st.Close()
		t.Fatalf("迁移失败: %v", err)
	}
	// Cleanup 是 LIFO：下面注册的 Service 清理会先执行，最后才关库。
	t.Cleanup(func() { _ = st.Close() })

	prov := mock.New()
	if err := prov.Start(context.Background()); err != nil {
		t.Fatalf("启动 mock provider 失败: %v", err)
	}
	fakeLLM := llmfake.New()
	cfg := testServiceConfig()
	log := slog.New(slog.NewTextHandler(io.Discard, nil))

	svc := chat.New(chat.Deps{
		Provider: prov,
		LLM:      fakeLLM,
		Store:    st,
		Cfg:      func() config.Config { return cfg },
		Now:      time.Now,
		Loc:      time.UTC,
		Log:      log,
	})

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		defer close(done)
		_ = svc.Run(ctx)
	}()
	t.Cleanup(func() {
		cancel()
		select {
		case <-done:
		case <-time.After(2 * time.Second):
			t.Errorf("Service.Run 在取消后没有退出")
		}
		if err := prov.Stop(context.Background()); err != nil {
			t.Errorf("停止 mock provider 失败: %v", err)
		}
	})

	return &testEnv{t: t, svc: svc, prov: prov, fake: fakeLLM, st: st, cfg: cfg}
}

func (e *testEnv) inject(t *testing.T, msgID, text string, hasAttachment bool) {
	t.Helper()
	msg := mock.EnsureEvent("user-1", text)
	msg.MsgID = msgID
	msg.HasAttachment = hasAttachment
	msg.Timestamp = time.Now()
	if err := e.prov.Inject(context.Background(), msg); err != nil {
		t.Fatalf("注入消息失败: %v", err)
	}
}

func waitFor(t *testing.T, timeout time.Duration, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("等待条件超时（%s）", timeout)
}

type turnRow struct {
	ID       int64
	Status   string
	Reply    string
	Finished *int64
}

func (e *testEnv) lookupTurn(msgID string) (turnRow, bool) {
	var (
		r        turnRow
		finished sql.NullInt64
	)
	err := e.st.DB().QueryRow(
		`SELECT id, status, reply_text, finished_at FROM turns WHERE inbound_msg_id = ?`, msgID,
	).Scan(&r.ID, &r.Status, &r.Reply, &finished)
	if errors.Is(err, sql.ErrNoRows) {
		return turnRow{}, false
	}
	if err != nil {
		e.t.Errorf("查询轮次失败: %v", err)
		return turnRow{}, false
	}
	if finished.Valid {
		v := finished.Int64
		r.Finished = &v
	}
	return r, true
}

func (e *testEnv) waitTurn(t *testing.T, msgID string, want chat.TurnStatus) turnRow {
	t.Helper()
	var got turnRow
	waitFor(t, 2*time.Second, func() bool {
		r, ok := e.lookupTurn(msgID)
		if !ok || r.Status != string(want) {
			return false
		}
		got = r
		return true
	})
	return got
}

func (e *testEnv) eventCount(kind string) int {
	var n int
	if err := e.st.DB().QueryRow(`SELECT count(*) FROM events WHERE kind = ?`, kind).Scan(&n); err != nil {
		return -1
	}
	return n
}

func (e *testEnv) turnCount() int {
	var n int
	if err := e.st.DB().QueryRow(`SELECT count(*) FROM turns`).Scan(&n); err != nil {
		return -1
	}
	return n
}

func TestService_HappyPath(t *testing.T) {
	env := newTestEnv(t)
	env.fake.SetReply("你好呀")

	env.inject(t, "m1", "你好", false)

	turn := env.waitTurn(t, "m1", chat.StatusOK)
	if turn.Reply != "你好呀" || turn.Finished == nil {
		t.Fatalf("轮次落库不正确: %+v", turn)
	}
	waitFor(t, time.Second, func() bool { return len(env.prov.Sent()) == 1 })
	if got := env.prov.Sent()[0].Text; got != "你好呀" {
		t.Fatalf("发送文本 = %q，期望 你好呀", got)
	}
	for _, kind := range []string{"inbound", "llm_call", "llm_result", "outbound"} {
		if env.eventCount(kind) < 1 {
			t.Fatalf("缺少 %s 事件", kind)
		}
	}
}

func TestService_DuplicateMsgIDProcessedOnce(t *testing.T) {
	env := newTestEnv(t)
	env.fake.SetReply("只回一次")

	env.inject(t, "m1", "你好", false)
	env.waitTurn(t, "m1", chat.StatusOK)

	env.inject(t, "m1", "你好", false)
	waitFor(t, time.Second, func() bool { return env.eventCount("inbound_duplicate") >= 1 })
	time.Sleep(50 * time.Millisecond)

	if got := len(env.prov.Sent()); got != 1 {
		t.Fatalf("重复推送后发送条数 = %d，期望 1", got)
	}
	if got := env.turnCount(); got != 1 {
		t.Fatalf("重复推送后轮数 = %d，期望 1", got)
	}
}

func TestService_ReclaimsStaleTurn(t *testing.T) {
	env := newTestEnv(t)
	ctx := context.Background()

	conv, _, err := env.st.GetOrCreateConversation(ctx, mock.EnsureEvent("user-1", "").Conv)
	if err != nil {
		t.Fatalf("创建会话失败: %v", err)
	}
	old, err := env.st.BeginTurn(ctx, chat.TurnParams{
		ConvID:    conv.ID,
		MsgID:     "m9",
		UserText:  "旧问题",
		TraceID:   "m9",
		StartedAt: time.Now().Add(-10 * time.Minute),
	})
	if err != nil || !old.IsNew {
		t.Fatalf("预置残轮失败: ref=%+v err=%v", old, err)
	}

	env.fake.SetReply("接管后的回答")
	env.inject(t, "m9", "旧问题", false)

	turn := env.waitTurn(t, "m9", chat.StatusOK)
	if turn.ID != old.ID {
		t.Fatalf("接管后轮次 ID = %d，期望复用 %d", turn.ID, old.ID)
	}
	waitFor(t, time.Second, func() bool { return len(env.prov.Sent()) == 1 })
	if got := env.turnCount(); got != 1 {
		t.Fatalf("接管不应新建轮次，实际 %d 轮", got)
	}
}

func TestService_SkipsFreshProcessingTurn(t *testing.T) {
	env := newTestEnv(t)
	ctx := context.Background()

	conv, _, err := env.st.GetOrCreateConversation(ctx, mock.EnsureEvent("user-1", "").Conv)
	if err != nil {
		t.Fatalf("创建会话失败: %v", err)
	}
	fresh, err := env.st.BeginTurn(ctx, chat.TurnParams{
		ConvID:    conv.ID,
		MsgID:     "m10",
		UserText:  "正在处理",
		TraceID:   "m10",
		StartedAt: time.Now(),
	})
	if err != nil || !fresh.IsNew {
		t.Fatalf("预置 processing 轮失败: ref=%+v err=%v", fresh, err)
	}

	env.inject(t, "m10", "正在处理", false)
	waitFor(t, time.Second, func() bool { return env.eventCount("inbound_duplicate") >= 1 })
	time.Sleep(100 * time.Millisecond)

	if got := len(env.prov.Sent()); got != 0 {
		t.Fatalf("新 processing 轮不应被接管，却发送了 %d 条", got)
	}
	if got := len(env.fake.Calls()); got != 0 {
		t.Fatalf("新 processing 轮不应调用模型，实际 %d 次", got)
	}
	turn, ok := env.lookupTurn("m10")
	if !ok || turn.Status != string(chat.StatusProcessing) || turn.Finished != nil {
		t.Fatalf("轮次不应被改动: %+v", turn)
	}
}

func TestService_NonTextMessageSkipsLLM(t *testing.T) {
	env := newTestEnv(t)

	env.inject(t, "m-img", "", true)

	waitFor(t, time.Second, func() bool { return len(env.prov.Sent()) == 1 })
	if got := env.prov.Sent()[0].Text; got != testNonTextHint {
		t.Fatalf("非文本提示 = %q，期望 %q", got, testNonTextHint)
	}
	if got := len(env.fake.Calls()); got != 0 {
		t.Fatalf("非文本消息不应调用模型，实际 %d 次", got)
	}
	turn := env.waitTurn(t, "m-img", chat.StatusFiltered)
	if turn.Finished == nil {
		t.Fatal("filtered 轮应有 finished_at")
	}
	if env.eventCount("inbound_filtered") < 1 {
		t.Fatal("缺少 inbound_filtered 事件")
	}
	if env.eventCount("llm_call") != 0 {
		t.Fatal("非文本消息不应产生 llm_call")
	}
}

func TestService_LLMFailureIsUserFriendly(t *testing.T) {
	tests := []struct {
		name string
		err  error
		want string
	}{
		{"auth", llm.ErrAuth, "❌ 模型服务鉴权失败，请联系管理员检查 API Key。"},
		{"rate", llm.ErrRateLimited, "⚠️ 模型服务限流中，请稍后再试。"},
		{"context", llm.ErrContextTooLong, "📏 本次对话太长了，请重新开始一段对话。"},
		{"unknown", errors.New("boom 429 sk-secret"), "❌ 处理出错了，请稍后再试。"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			env := newTestEnv(t)
			env.fake.SetError(tt.err)

			failText := "失败问题-" + tt.name
			env.inject(t, "m-fail", failText, false)

			env.waitTurn(t, "m-fail", chat.StatusFailed)
			waitFor(t, time.Second, func() bool { return len(env.prov.Sent()) == 1 })
			reply := env.prov.Sent()[0].Text
			if reply != tt.want {
				t.Fatalf("兜底文案 = %q，期望 %q", reply, tt.want)
			}
			for _, leak := range []string{"401", "429", "boom", "sk-"} {
				if strings.Contains(reply, leak) {
					t.Fatalf("兜底文案泄漏 %q: %q", leak, reply)
				}
			}
			if strings.Contains(reply, "/clear") {
				t.Fatalf("V1 文案不应提 /clear: %q", reply)
			}

			// 失败轮不能进入下一轮上下文。
			env.fake.SetError(nil)
			env.fake.SetReply("恢复回答")
			env.inject(t, "m-ok", "恢复问题", false)
			env.waitTurn(t, "m-ok", chat.StatusOK)

			calls := env.fake.Calls()
			if len(calls) != 2 {
				t.Fatalf("模型调用次数 = %d，期望 2", len(calls))
			}
			for _, m := range calls[1].Message {
				if strings.Contains(m.Text, failText) {
					t.Fatalf("失败轮进入了上下文: %q", m.Text)
				}
			}
			if len(calls[1].Message) != 1 {
				t.Fatalf("恢复轮上下文消息数 = %d，期望仅当前用户消息", len(calls[1].Message))
			}
		})
	}
}

func TestService_LongReplySegmented(t *testing.T) {
	env := newTestEnv(t)
	long := strings.Repeat("长", 5000)
	env.fake.SetReply(long)

	env.inject(t, "m-long", "讲个长的", false)
	turn := env.waitTurn(t, "m-long", chat.StatusOK)

	waitFor(t, time.Second, func() bool { return len(env.prov.Sent()) == 3 })
	sent := env.prov.Sent()
	if len(sent) > 3 {
		t.Fatalf("分段数 = %d，超过上限 3", len(sent))
	}
	for i, msg := range sent {
		if n := utf8.RuneCountInString(msg.Text); n > 100 {
			t.Fatalf("第 %d 段长度 = %d rune，超过上限 100", i+1, n)
		}
	}
	if !strings.Contains(sent[len(sent)-1].Text, "…（内容过长已截断）") {
		t.Fatalf("最后一段缺少截断后缀: %q", sent[len(sent)-1].Text)
	}
	if got := utf8.RuneCountInString(turn.Reply); got != 5000 {
		t.Fatalf("turns.reply_text 应保存完整回复，实际 %d rune", got)
	}

	var joined strings.Builder
	for _, msg := range sent {
		joined.WriteString(msg.Text)
	}
	if !strings.HasPrefix(joined.String(), strings.Repeat("长", 200)) {
		t.Fatal("发送内容不是原文前缀")
	}
}

func TestService_ContextAccumulates(t *testing.T) {
	env := newTestEnv(t)

	env.fake.SetReply("回答一")
	env.inject(t, "m1", "问题一", false)
	env.waitTurn(t, "m1", chat.StatusOK)

	env.fake.SetReply("回答二")
	env.inject(t, "m2", "问题二", false)
	env.waitTurn(t, "m2", chat.StatusOK)

	calls := env.fake.Calls()
	if len(calls) != 2 {
		t.Fatalf("模型调用次数 = %d，期望 2", len(calls))
	}
	msgs := calls[1].Message
	if len(msgs) != 3 {
		t.Fatalf("第二轮上下文消息数 = %d，期望 3（user1/assistant1/user2）", len(msgs))
	}
	wantRoles := []llm.Role{llm.RoleUser, llm.RoleAssistant, llm.RoleUser}
	for i, m := range msgs {
		if m.Role != wantRoles[i] {
			t.Fatalf("第 %d 条角色 = %q，期望 %q", i, m.Role, wantRoles[i])
		}
	}
	if !strings.Contains(msgs[0].Text, "问题一") {
		t.Fatalf("第 1 条应包含第一轮问题，实际 %q", msgs[0].Text)
	}
	if !strings.Contains(msgs[1].Text, "回答一") {
		t.Fatalf("第 2 条应包含第一轮回答，实际 %q", msgs[1].Text)
	}
	if msgs[2].Text != "问题二" {
		t.Fatalf("第 3 条应是当前问题，实际 %q", msgs[2].Text)
	}
}
