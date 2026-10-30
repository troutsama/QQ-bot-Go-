package llm

import (
	"context"
	"encoding/json"
)

type Role string

const (
	RoleUser      Role = "user"
	RoleAssistant Role = "assistant"
	RoleTool      Role = "tool" // 工具结果
)

// 中性消息结构
type Message struct {
	Role        Role
	Text        string       // 可为空：若只有工具调用
	ToolCalls   []ToolCall   // assistant 发起的调用
	ToolResults []ToolResult // role = tool 时的结果
}

// 模型返回的工具调用指令
type ToolCall struct {
	ID        string
	Name      string
	Arguments string
}

// 本地执行工具后的结果
type ToolResult struct {
	CallID string
	Output string
}

// 工具声明
type ToolDef struct {
	Name        string
	Description string
	InputSchema json.RawMessage // json 原文
}

// 一次模型请求
type Request struct {
	System      string
	Messages    []Message
	Tools       []ToolDef
	Model       string
	MaxTokens   int
	Temperature float64
}

type StopReason string

const (
	StopEnd       StopReason = "end"
	StopMaxTokens StopReason = "max_tokens"
	StopToolUse   StopReason = "tool_use"
	StopOther     StopReason = "other"
)

type Usage struct {
	InputTokens  int
	OutputTokens int
}

type Response struct {
	Text       string
	ToolCalls  []ToolCall
	StopReason StopReason
	Usage      Usage
}

// 统一端口接口
type Client interface {
	Name() string
	Complete(ctx context.Context, req Request) (Response, error)
}
