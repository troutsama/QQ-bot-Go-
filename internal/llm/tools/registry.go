package tools

import (
	"context"
	"fmt"
	"qq-bot/internal/llm"
	"sort"
	"sync"
)

// ToolHandler 工具实际执行的回调函数
type ToolHandler func(ctx context.Context, argsJSON string) (string, error)

type registeredTool struct {
	def     llm.ToolDef
	handler ToolHandler
}

// Registry 工具注册与分发中心
type Registry struct {
	mu    sync.RWMutex
	tools map[string]registeredTool
}

func NewRegistry() *Registry {
	return &Registry{tools: make(map[string]registeredTool)}
}

// 注册一个工具
func (r *Registry) Registry(def llm.ToolDef, handler ToolHandler) error {
	r.mu.Lock()
	defer r.mu.Unlock()

	if def.Name == "" {
		return fmt.Errorf("tool：工具名不能为空")
	}
	if handler == nil {
		return fmt.Errorf("tool：工具 %q 的 handler 不能为空", def.Name)
	}
	if r.tools == nil {
		r.tools = make(map[string]registeredTool)
	}
	if _, exists := r.tools[def.Name]; exists {
		return fmt.Errorf("tool：工具 %q 重复注册", def.Name)
	}
	r.tools[def.Name] = registeredTool{
		def:     def,
		handler: handler,
	}
	return nil
}

// 获取所以已注册的工具的中性定义（传给模型）
func (r *Registry) Defs() []llm.ToolDef {
	r.mu.RLock()
	defer r.mu.RUnlock()

	defs := make([]llm.ToolDef, 0, len(r.tools))
	for _, t := range r.tools {
		defs = append(defs, t.def)
	}
	sort.Slice(defs, func(i, j int) bool { return defs[i].Name < defs[j].Name })
	return defs
}

// execute 执行指定工具调用
func (r *Registry) Execute(ctx context.Context, call llm.ToolCall) (string, error) {
	if err := ctx.Err(); err != nil {
		return "", fmt.Errorf("工具调用已取消: %w", err)
	}
	r.mu.RLock()
	t, ok := r.tools[call.Name]
	r.mu.RUnlock()

	if !ok {
		return "", fmt.Errorf("未知的工具：%q", call.Name)
	}

	return t.handler(ctx, call.Arguments)
}
