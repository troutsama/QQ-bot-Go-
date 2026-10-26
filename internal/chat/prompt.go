package chat

import (
	"qq-bot/internal/config"
	"qq-bot/internal/llm"
)

// BuildRequest 将配置、历史对话与当前输入整合为标准的中性请求
func BuildRequest(cfg config.LLMConfig, history []Turn, currentText string, tools []llm.ToolDef) llm.Request {
	message := make([]llm.Message, 0, len(history)*2+1)

	// 组装多轮历史
	for _, turn := range history {
		if turn.Status != StatusOK {
			continue // 失败轮次不进入上下文
		}
		message = append(message, llm.Message{
			Role: llm.RoleUser,
			Text: turn.UserText,
		})
		if turn.ReplyText != "" {
			message = append(message, llm.Message{
				Role: llm.RoleAssistant,
				Text: turn.ReplyText,
			})
		}
	}

	// 组装当前用户消息
	message = append(message, llm.Message{
		Role: llm.RoleUser,
		Text: currentText,
	})

	return llm.Request{
		System:      cfg.SystemPrompt,
		Messages:    message,
		Tools:       tools,
		Model:       cfg.Model,
		MaxTokens:   cfg.MaxTokens,
		Temperature: cfg.Temperature,
	}
}
