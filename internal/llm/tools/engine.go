package tools

import (
	"context"
	"fmt"
	"qq-bot/internal/llm"
)

type Engine struct {
	client   llm.Client
	registry *Registry
	maxSteps int // 循环安全上限
}

func NewEngine(client llm.Client, registry *Registry, maxSteps int) *Engine {
	if maxSteps <= 0 {
		maxSteps = 5
	}
	return &Engine{
		client:   client,
		registry: registry,
		maxSteps: maxSteps,
	}
}

func (e *Engine) Run(ctx context.Context, req llm.Request) (llm.Response, error) {
	if e.registry != nil {
		req.Tools = e.registry.Defs()
	}

	for step := 0; step < e.maxSteps; step++ {
		resp, err := e.client.Complete(ctx, req)
		if err != nil {
			return llm.Response{}, fmt.Errorf("llm 调用失败：%w", err)
		}

		if len(resp.ToolCalls) == 0 {
			return resp, nil
		}

		req.Messages = append(req.Messages, llm.Message{
			Role:      llm.RoleAssistant,
			Text:      resp.Text,
			ToolCalls: resp.ToolCalls,
		})

		var results []llm.ToolResult
		for _, call := range resp.ToolCalls {
			output, err := e.registry.Execute(ctx, call)
			if err != nil {
				output = fmt.Sprintf("工具： %s  调用出错：%v", call.Name, err)
			}
			results = append(results, llm.ToolResult{
				CallID: call.ID,
				Output: output,
			})
		}

		req.Messages = append(req.Messages, llm.Message{
			Role:        llm.RoleTool,
			ToolResults: results,
		})
	}
	return llm.Response{}, fmt.Errorf("工具调用超过部署上限（%d），已终止", e.maxSteps)
}
