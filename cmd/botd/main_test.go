package main

import (
	"bytes"
	"context"
	"encoding/json"
	"flag"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"qq-bot/internal/config"
	"qq-bot/internal/platform"
	"qq-bot/internal/platform/provider/mock"
)

var liveTools = flag.Bool("live-tools", false, "显式使用本机模型配置验证两个真实脚本工具，不连接 QQ")

func TestLiveToolsConversation(t *testing.T) {
	if !*liveTools {
		t.Skip("真实模型工具联调：go test ./cmd/botd -run TestLiveToolsConversation -args -live-tools")
	}
	cfg, err := config.Load("")
	if err != nil {
		t.Fatal(err)
	}
	if cfg.LLM.Protocol != "openai-chat" {
		t.Fatal("需要 llm.protocol=openai-chat")
	}
	if err := cfg.Validate(); err != nil {
		t.Fatal(err)
	}
	var logs bytes.Buffer
	p := mock.New()
	s, err := newChatService(*cfg, p, slog.New(slog.NewTextHandler(&logs, nil)))
	if err != nil {
		t.Fatal(err)
	}
	timeout, _ := time.ParseDuration(cfg.LLM.Timeout)
	ctx, cancel := context.WithTimeout(context.Background(), timeout+5*time.Second)
	defer cancel()
	if err := p.Start(ctx); err != nil {
		t.Fatal(err)
	}
	defer p.Stop(context.Background())
	done := make(chan error, 1)
	go func() { done <- s.Run(ctx) }()
	if err := p.Inject(ctx, mock.EnsureEvent("live-test", "请先调用 get_location 查询位置，再将返回的 location 传给 get_weather 查询天气。两个工具都执行后，告诉我位置和天气，并说明这是演示数据。")); err != nil {
		t.Fatal(err)
	}
	ticker := time.NewTicker(20 * time.Millisecond)
	defer ticker.Stop()
waitLive:
	for {
		select {
		case <-ctx.Done():
			t.Error("等待真实模型工具回复超时")
			break waitLive
		case <-ticker.C:
			if len(p.Sent()) > 0 {
				break waitLive
			}
		}
	}
	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Service 未及时退出")
	}
	sent := p.Sent()
	if len(sent) == 0 {
		t.Fatal("没有收到回复")
	}
	var text strings.Builder
	for _, msg := range sent {
		text.WriteString(msg.Text)
	}
	if !strings.Contains(logs.String(), "tool=get_location") || !strings.Contains(logs.String(), "tool=get_weather") {
		t.Fatalf("模型没有执行两个工具，最终回复：%s", text.String())
	}
	if !strings.Contains(text.String(), "长沙") || !strings.Contains(text.String(), "晴") {
		t.Fatalf("工具结果未用于回复：%s", text.String())
	}
	t.Logf("真实模型已执行 Python 位置与 Node 天气工具；回复：%s", text.String())
}

// 使用真实 OpenAI HTTP 适配器、真实 Python/Node 脚本与 mock QQ 平台。
func TestQQModelToolsConversation(t *testing.T) {
	var requests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Model string `json:"model"`
			Tools []struct {
				Function struct {
					Name       string          `json:"name"`
					Parameters json.RawMessage `json:"parameters"`
				} `json:"function"`
			} `json:"tools"`
			Messages []struct {
				Role       string  `json:"role"`
				Content    *string `json:"content"`
				ToolCallID string  `json:"tool_call_id"`
				ToolCalls  []struct {
					ID string `json:"id"`
				} `json:"tool_calls"`
			} `json:"messages"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Error(err)
			http.Error(w, "bad JSON", 400)
			return
		}
		if r.URL.Path != "/v1/chat/completions" || body.Model != "test-model" || len(body.Tools) != 2 {
			t.Errorf("请求路径/模型/工具不符: %s %+v", r.URL.Path, body)
		}
		for _, tool := range body.Tools {
			if !json.Valid(tool.Function.Parameters) {
				t.Error("schema 不是 JSON")
			}
		}
		w.Header().Set("Content-Type", "application/json")
		switch step := requests.Add(1); step {
		case 1:
			io.WriteString(w, `{"choices":[{"message":{"role":"assistant","content":null,"tool_calls":[{"id":"loc-1","type":"function","function":{"name":"get_location","arguments":"{}"}}]},"finish_reason":"tool_calls"}]}`)
		case 2:
			if len(body.Messages) != 4 {
				t.Errorf("位置回填消息数=%d", len(body.Messages))
			} else {
				tool := body.Messages[3]
				if tool.Role != "tool" || tool.ToolCallID != "loc-1" || tool.Content == nil {
					t.Errorf("位置工具 ID 不符: %+v", tool)
				} else {
					var got map[string]string
					if err := json.Unmarshal([]byte(*tool.Content), &got); err != nil || got["location"] != "湖南长沙" {
						t.Errorf("位置结果不符: %s", *tool.Content)
					}
				}
				if len(body.Messages[2].ToolCalls) != 1 || body.Messages[2].ToolCalls[0].ID != "loc-1" {
					t.Error("assistant 工具调用丢失")
				}
			}
			io.WriteString(w, `{"choices":[{"message":{"role":"assistant","content":null,"tool_calls":[{"id":"weather-1","type":"function","function":{"name":"get_weather","arguments":"{\"location\":\"湖南长沙\"}"}}]},"finish_reason":"tool_calls"}]}`)
		case 3:
			if len(body.Messages) != 6 {
				t.Errorf("天气回填消息数=%d", len(body.Messages))
			} else {
				tool := body.Messages[5]
				if tool.ToolCallID != "weather-1" || tool.Content == nil {
					t.Error("天气工具 ID 丢失")
				} else {
					var got map[string]string
					if err := json.Unmarshal([]byte(*tool.Content), &got); err != nil || got["location"] != "湖南长沙" || got["weather"] != "晴天" {
						t.Errorf("天气结果不符: %s", *tool.Content)
					}
				}
			}
			io.WriteString(w, `{"choices":[{"message":{"role":"assistant","content":"湖南长沙今天晴天。"},"finish_reason":"stop"}]}`)
		default:
			t.Errorf("意外模型请求次数=%d", step)
			http.Error(w, "too many requests", 500)
		}
	}))
	defer server.Close()
	root, err := filepath.Abs("../..")
	if err != nil {
		t.Fatal(err)
	}
	cfg := config.Config{
		LLM:   config.LLMConfig{Protocol: "openai-chat", BaseURL: server.URL + "/v1", APIKey: "test", Model: "test-model", SystemPrompt: "请按需调用位置和天气工具。", Timeout: "5s", MaxTokens: 256},
		Chat:  config.ChatConfig{ContextTurns: 2, MaxReplySegments: 3, SegmentMaxChars: 100},
		Tools: config.ToolsConfig{Directory: filepath.Join(root, "skills"), MaxSteps: 5},
	}
	p := mock.New()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	if err := p.Start(ctx); err != nil {
		t.Fatal(err)
	}
	defer p.Stop(context.Background())
	s, err := newChatService(cfg, p, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { done <- s.Run(ctx) }()
	if err := p.Inject(ctx, mock.EnsureEvent("user-1", "我在哪里，那里天气怎样？")); err != nil {
		t.Fatal(err)
	}
	timer := time.NewTimer(5 * time.Second)
	defer timer.Stop()
	ticker := time.NewTicker(10 * time.Millisecond)
	defer ticker.Stop()
wait:
	for {
		select {
		case <-timer.C:
			t.Fatal("等待 QQ 回复超时")
		case <-ticker.C:
			if len(p.Sent()) > 0 {
				break wait
			}
		}
	}
	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("Service 未退出")
	}
	sent := p.Sent()
	if len(sent) != 1 || sent[0].Text != "湖南长沙今天晴天。" || sent[0].Reply == nil || sent[0].Reply.MsgID != "mock-msg-1" {
		t.Fatalf("QQ 回复不符: %+v", sent)
	}
	if requests.Load() != 3 {
		t.Fatalf("模型调用次数=%d", requests.Load())
	}
	if sent[0].Conv.Scene != platform.SceneC2C {
		t.Fatal("回复场景错误")
	}
}
