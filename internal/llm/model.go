package llm

import "encoding/json"

type Role string

const (
	RoleUser      Role = "user"
	RoleAssistant Role = "assistant"
	RoleTool      Role = "tool" // 工具结果
)

type Message struct {
	Role        Role
	Text        string       // 可为空：若只有工具调用
	ToolCalls   []ToolCall   // assistant 发起的调用
	ToolResults []ToolResult // role = tool 时的结果
}

type ToolCall struct {
	ID        string
	Name      string
	Arguments string
}

type ToolResult struct {
	CallID string
	Output string
}

type ToolDef struct {
	Name        string
	Description string
	InputSchema json.RawMessage // json 原文
}

// 一次模型请求
type Request struct {
	System      string
	Message     []Message
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
	Toolcalls  []ToolCall
	StopReason StopReason
	Usage      Usage
}
