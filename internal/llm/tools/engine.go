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
	// 每次运行前都注入当前注册的所有工具声明
	if e.registry != nil {
		req.Tools = e.registry.Defs()
	}

	for step := 0; step < e.maxSteps; step++ {
		if err := ctx.Err(); err != nil {
			return llm.Response{}, fmt.Errorf("模型调用已取消: %w", err)
		}
		// 请求模型
		resp, err := e.client.Complete(ctx, req)
		if err != nil {
			return llm.Response{}, fmt.Errorf("llm 调用失败：%w", err)
		}

		// 如果没有发起工具调用，说明请求已经结束
		if len(resp.ToolCalls) == 0 {
			return resp, nil
		}
		if e.registry == nil {
			return llm.Response{}, fmt.Errorf("模型请求工具调用，但未配置工具注册中心")
		}

		// 执行工具调用
		req.Messages = append(req.Messages, llm.Message{
			Role:      llm.RoleAssistant,
			Text:      resp.Text,
			ToolCalls: resp.ToolCalls,
		})

		// 逐个执行工具
		var results []llm.ToolResult
		for _, call := range resp.ToolCalls {
			if err := ctx.Err(); err != nil {
				return llm.Response{}, fmt.Errorf("工具调用已取消: %w", err)
			}
			output, err := e.registry.Execute(ctx, call)
			if err != nil {
				// 如果工具执行报错不打断流程，将错误信息传递给模型
				output = fmt.Sprintf("工具： %s  调用出错：%v", call.Name, err)
			}
			results = append(results, llm.ToolResult{
				CallID: call.ID,
				Output: output,
			})
		}

		// 将工具调用结果加入到列表中后，进入下一轮循环
		req.Messages = append(req.Messages, llm.Message{
			Role:        llm.RoleTool,
			ToolResults: results,
		})
	}
	return llm.Response{}, fmt.Errorf("模型调用超过步数上限（%d），已终止", e.maxSteps)
}
