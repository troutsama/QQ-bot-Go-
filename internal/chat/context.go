package chat

import (
	"time"

	"qq-bot/internal/llm"
)

// contextTimeLayout 是上下文时间前缀的格式：月-日 时:分。
// 年份省略是为了让模型看到相对时间，同时减少 token。
const contextTimeLayout = "01-02 15:04"

// BuildMessages 把数据库里的历史轮次转换成模型消息。
//
// 只取 user/assistant：V1 没有工具调用，tool 消息不该出现在模型上下文里；
// 未来做工具时再在这里补 ToolCalls/ToolResults。
//
// 当前用户消息**不在这里追加**：它所属的轮仍是 processing，RecentTurns 取不到。
// aiStep 在 BuildMessages 之后显式 append 当前入站文本。
//
// 时区来自调用方（main 按配置加载）；loc 为 nil 时退化为 UTC，
// 绝不使用 time.Local —— 部署到 UTC 机器时那会让时间前缀整体偏移。
func BuildMessages(turns []Turn, loc *time.Location, timePrefix bool) []llm.Message {
	if len(turns) == 0 {
		return []llm.Message{}
	}
	if loc == nil {
		loc = time.UTC
	}

	msgs := make([]llm.Message, 0, len(turns)*2)
	for _, turn := range turns {
		for _, m := range turn.Messages {
			var role llm.Role
			switch m.Role {
			case "user":
				role = llm.RoleUser
			case "assistant":
				role = llm.RoleAssistant
			default:
				continue // tool 等角色 V1 不进上下文
			}

			text := m.Content
			if timePrefix && m.CreatedAt > 0 {
				ts := time.UnixMilli(m.CreatedAt).In(loc).Format(contextTimeLayout)
				text = "[" + ts + "] " + text
			}
			msgs = append(msgs, llm.Message{Role: role, Text: text})
		}
	}
	return msgs
}
