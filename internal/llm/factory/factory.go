package factory

import (
	"fmt"
	"log/slog"

	"qq-bot/internal/config"
	"qq-bot/internal/llm"
	"qq-bot/internal/llm/provider/fake"
)

// New 按配置创建 Client。与 platform 的工厂同构：唯一 switch、不缓存实例、default 返回 error。
func New(cfg config.LLMConfig, log *slog.Logger) (llm.Client, error) {
	switch cfg.Protocol {
	case "fake":
		return fake.New(), nil

	case "openai-chat", "openai-responses", "anthropic":
		// ⬜ 预留：三个真实适配器见 07 T14–T19 / 02 §3.3.2。
		//    这里**显式报错**，绝不静默返回一个不可用的 client ——
		//    静默失败会让你以为「模型没配好」，实际是「你还没写」。
		return nil, fmt.Errorf("llm: 协议 %q 的适配器尚未实现（见 07 T14–T19）", cfg.Protocol)

	default:
		return nil, fmt.Errorf("llm: 未知协议 %q（可用：fake, openai-chat, openai-responses, anthropic）", cfg.Protocol)
	}
}
