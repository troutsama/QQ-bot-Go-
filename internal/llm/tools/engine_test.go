package tools

import (
	"context"
	"errors"
	"testing"

	"qq-bot/internal/llm"
)

type clientFunc func(context.Context, llm.Request) (llm.Response, error)

func (f clientFunc) Name() string { return "test" }
func (f clientFunc) Complete(ctx context.Context, req llm.Request) (llm.Response, error) {
	return f(ctx, req)
}

func TestRegistryZeroValue(t *testing.T) {
	var registry Registry
	if err := registry.Registry(llm.ToolDef{Name: "example"}, func(context.Context, string) (string, error) {
		return "ok", nil
	}); err != nil {
		t.Fatal(err)
	}
	if err := registry.Registry(llm.ToolDef{Name: "bad"}, nil); err == nil {
		t.Fatal("不应接受 nil handler")
	}
}

func TestEngineMissingRegistry(t *testing.T) {
	engine := NewEngine(clientFunc(func(context.Context, llm.Request) (llm.Response, error) {
		return llm.Response{ToolCalls: []llm.ToolCall{{ID: "one", Name: "missing"}}}, nil
	}), nil, 2)
	if _, err := engine.Run(context.Background(), llm.Request{}); err == nil {
		t.Fatal("无 registry 的工具调用应该返回错误")
	}
}

func TestEngineCancelledBeforeRequest(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	engine := NewEngine(clientFunc(func(context.Context, llm.Request) (llm.Response, error) {
		t.Error("取消后不应请求模型")
		return llm.Response{}, nil
	}), nil, 2)
	if _, err := engine.Run(ctx, llm.Request{}); !errors.Is(err, context.Canceled) {
		t.Fatalf("期望 context.Canceled，实际 %v", err)
	}
}
