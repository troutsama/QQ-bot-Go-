package openaichat

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"qq-bot/internal/config"
	"qq-bot/internal/llm"
)

func TestCompleteLive(t *testing.T) {
	// 空路径会让配置加载器自动查找项目根目录的 config.yaml。
	cfg, err := config.Load("")
	if err != nil {
		t.Fatalf("加载配置失败: %v", err)
	}

	if cfg.LLM.Protocol != "openai-chat" {
		t.Fatal("请将 llm.protocol 配置为 openai-chat")
	}
	if strings.TrimSpace(cfg.LLM.Model) == "" {
		t.Fatal("llm.model 不能为空")
	}

	timeout, err := time.ParseDuration(cfg.LLM.Timeout)
	if err != nil {
		t.Fatalf("解析模型超时配置失败: %v", err)
	}
	if timeout <= 0 {
		t.Fatal("llm.timeout 必须大于零")
	}

	client, err := New(
		cfg.LLM.BaseURL,
		cfg.LLM.APIKey,
		&http.Client{Timeout: timeout},
	)
	if err != nil {
		t.Fatalf("构造客户端失败: %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()

	got, err := client.Complete(ctx, llm.Request{
		Model:       cfg.LLM.Model,
		System:      cfg.LLM.SystemPrompt,
		Messages:    []llm.Message{{Role: llm.RoleUser, Text: "你好，请简单介绍一下你自己。"}},
		MaxTokens:   cfg.LLM.MaxTokens,
		Temperature: cfg.LLM.Temperature,
	})
	if err != nil {
		t.Fatalf("实际模型调用失败: %v", err)
	}

	if strings.TrimSpace(got.Text) == "" {
		t.Fatalf("模型返回空文本，停止原因: %s", got.StopReason)
	}
	t.Logf("模型回复: %s", got.Text)
	t.Logf("停止原因: %s；输入 tokens: %d；输出 tokens: %d",
		got.StopReason, got.Usage.InputTokens, got.Usage.OutputTokens)
}

func TestComplete(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(
		func(w http.ResponseWriter, r *http.Request) {
			if r.Method != http.MethodPost {
				t.Errorf("method = %q，期望 POST", r.Method)
			}
			if r.URL.Path != "/v1/chat/completions" {
				t.Errorf("path = %q", r.URL.Path)
			}
			if got := r.Header.Get("Authorization"); got != "Bearer test-key" {
				t.Errorf("Authorization = %q", got)
			}
			if got := r.Header.Get("Content-Type"); got != "application/json" {
				t.Errorf("Content-Type = %q", got)
			}

			// 独立声明协议字段，避免与生产 DTO 共享相同的标签错误。
			var body struct {
				Model    string `json:"model"`
				Messages []struct {
					Role    string `json:"role"`
					Content string `json:"content"`
				} `json:"messages"`
				MaxTokens int `json:"max_tokens"`
			}
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
				t.Errorf("解析请求失败: %v", err)
				http.Error(w, "invalid JSON", http.StatusBadRequest)
				return
			}
			if body.Model != "test-model" || body.MaxTokens != 128 {
				t.Errorf("请求参数不符合预期: %+v", body)
			}
			if len(body.Messages) != 1 {
				t.Errorf("消息数 = %d，期望 1", len(body.Messages))
			} else if body.Messages[0].Role != "user" ||
				body.Messages[0].Content != "你好" {
				t.Errorf("消息不符合预期: %+v", body.Messages[0])
			}

			w.Header().Set("Content-Type", "application/json")
			if _, err := w.Write([]byte(`{
				"choices": [{
					"message": {"role": "assistant", "content": "你好，有什么可以帮你？"},
					"finish_reason": "stop"
				}],
				"usage": {"prompt_tokens": 8, "completion_tokens": 6}
			}`)); err != nil {
				t.Errorf("写入响应失败: %v", err)
			}
		},
	))
	defer server.Close()

	client, err := New(server.URL+"/v1/", "test-key", server.Client())
	if err != nil {
		t.Fatalf("构造客户端失败: %v", err)
	}

	got, err := client.Complete(context.Background(), llm.Request{
		Model:     "test-model",
		Messages:  []llm.Message{{Role: llm.RoleUser, Text: "你好"}},
		MaxTokens: 128,
	})
	if err != nil {
		t.Fatalf("Complete 失败: %v", err)
	}
	if got.Text != "你好，有什么可以帮你？" || got.StopReason != llm.StopEnd {
		t.Errorf("回复不符合预期: %+v", got)
	}
	if got.Usage.InputTokens != 8 || got.Usage.OutputTokens != 6 {
		t.Errorf("token 用量不符合预期: %+v", got.Usage)
	}
}
