package config

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"gopkg.in/yaml.v3"
)

type Config struct {
	Server   ServerConfig   `yaml:"server"`
	Bot      BotConfig      `yaml:"bot"`
	LLM      LLMConfig      `yaml:"llm"`
	Chat     ChatConfig     `yaml:"chat"`
	Log      LogConfig      `yaml:"log"`
	Database DatabaseConfig `yaml:"database"`
}

type ServerConfig struct {
	Listen     string `yaml:"listen"`
	AdminToken string `yaml:"admin_token"`
}

type BotConfig struct {
	Provider string      `yaml:"provider"` // qqbot | mock
	QQBot    QQBotConfig `yaml:"qqbot"`
}

type QQBotConfig struct {
	AppID     string `yaml:"app_id"`
	AppSecret string `yaml:"app_secret"`
	Sandbox   bool   `yaml:"sandbox"`
}

type LLMConfig struct {
	Protocol     string  `yaml:"protocol"` // fake | openai-chat | openai-responses | anthropic
	BaseURL      string  `yaml:"base_url"`
	APIKey       string  `yaml:"api_key"`
	Model        string  `yaml:"model"`
	SystemPrompt string  `yaml:"system_prompt"`
	MaxTokens    int     `yaml:"max_tokens"`
	Temperature  float64 `yaml:"temperature"`
	Timeout      string  `yaml:"timeout"` // 先用 string，Validate 里 ParseDuration
	MaxAttempts  int     `yaml:"max_attempts"`
}

type ChatConfig struct {
	ContextTurns      int    `yaml:"context_turns"`
	ContextTimePrefix bool   `yaml:"context_time_prefix"`
	Timezone          string `yaml:"timezone"`
	MaxReplySegments  int    `yaml:"max_reply_segments"`
	SegmentMaxChars   int    `yaml:"segment_max_chars"`
}

type LogConfig struct {
	Level      string `yaml:"level"`
	File       string `yaml:"file"`
	MaxSizeMB  int    `yaml:"max_size_mb"`
	MaxAgeDays int    `yaml:"max_age_days"`
}

type DatabaseConfig struct {
	Path string `yaml:"path"`
}

// Load 读取配置。path 为空时按优先级查找（见 resolvePath）。
//
// 密钥直接写在配置文件里 —— config.yaml 已进 .gitignore，只提交 config.yaml.example。
// 本项目**不做** ${VAR} 展开、也**不做**环境变量覆盖：一份文件就是全部配置，
// 「配置从哪来」只有一处答案，排障时不用去猜环境变量。
func Load(path string) (*Config, error) {
	actual := resolvePath(path)
	raw, err := os.ReadFile(actual)
	if err != nil {
		return nil, fmt.Errorf("config: 读取 %s: %w", actual, err)
	}

	var cfg Config
	if err := yaml.Unmarshal(raw, &cfg); err != nil {
		return nil, fmt.Errorf("config: 解析 %s: %w", actual, err)
	}
	cfg.applyDefaults()
	return &cfg, nil
}

// 注：这里刻意**没有** ${VAR} 展开与环境变量覆盖（与设计文档 03 §5.1 的差异，见 README/AGENTS）。
// 代价是密钥只受 .gitignore 一层保护，所以：
//   - config.yaml 永远不进 git（已在 .gitignore 里）
//   - 别把 config.yaml 贴给别人/截图；要分享配置就用 config.yaml.example
// 收益是配置来源单一：出问题时只需看一个文件。

func (c *Config) applyDefaults() {
	if c.Server.Listen == "" {
		c.Server.Listen = "127.0.0.1:8081"
	}
	if c.Log.Level == "" {
		c.Log.Level = "info"
	}
	if c.Log.MaxSizeMB == 0 {
		c.Log.MaxSizeMB = 50
	}
	if c.Log.MaxAgeDays == 0 {
		c.Log.MaxAgeDays = 7
	}
	if c.Chat.Timezone == "" {
		c.Chat.Timezone = "Asia/Shanghai"
	}
	if c.Chat.ContextTurns == 0 {
		c.Chat.ContextTurns = 10
	}
	if c.Chat.MaxReplySegments == 0 {
		c.Chat.MaxReplySegments = 3 // 单聊被动回复上限 4，留 1 次余量（03 §4.4）
	}
	if c.LLM.Timeout == "" {
		c.LLM.Timeout = "60s"
	}
	if c.Database.Path == "" {
		c.Database.Path = "data/bot.db"
	}
}

// Validate 启动时和热重载时都要调用（03 §5.1）。
// 用「收集所有问题再一起报」而不是遇到第一个就返回 —— 启动失败时能一次看完。
func (c *Config) Validate() error {
	var problems []string

	if c.Server.AdminToken == "" {
		problems = append(problems, "server.admin_token 不能为空（admin API 的鉴权凭据）")
	}
	switch c.Bot.Provider {
	case "qqbot":
		if c.Bot.QQBot.AppID == "" || c.Bot.QQBot.AppSecret == "" {
			problems = append(problems, "bot.provider=qqbot 时 bot.qqbot.app_id / app_secret 必填")
		}
	case "mock":
		// 不需要任何凭据 —— 这正是 mock 的价值（02 §3.2.3）
	case "":
		problems = append(problems, "bot.provider 不能为空（可选：qqbot, mock）")
	default:
		problems = append(problems, fmt.Sprintf("bot.provider=%q 未知（可选：qqbot, mock）", c.Bot.Provider))
	}

	switch c.LLM.Protocol {
	case "fake", "openai-chat", "openai-responses", "anthropic":
	case "":
		problems = append(problems, "llm.protocol 不能为空（可选：fake, openai-chat, openai-responses, anthropic）")
	default:
		problems = append(problems, fmt.Sprintf("llm.protocol=%q 未知", c.LLM.Protocol))
	}
	if c.LLM.Protocol != "fake" && c.LLM.BaseURL == "" {
		problems = append(problems, "llm.base_url 必填")
	}
	if c.LLM.Temperature < 0 || c.LLM.Temperature > 2 {
		// ⚠️ Anthropic 上限是 1；这里只挡住明显非法的值，钳制交给适配器（02 §3.3.2 陷阱 3）
		problems = append(problems, "llm.temperature 必须在 0–2 之间")
	}
	if _, err := time.ParseDuration(c.LLM.Timeout); err != nil {
		problems = append(problems, fmt.Sprintf("llm.timeout=%q 不是合法时长（例：60s）", c.LLM.Timeout))
	}
	if _, err := time.LoadLocation(c.Chat.Timezone); err != nil {
		problems = append(problems, fmt.Sprintf("chat.timezone=%q 无法加载", c.Chat.Timezone))
	}
	if c.Chat.MaxReplySegments > 4 {
		problems = append(problems, "chat.max_reply_segments 不能超过 4（单聊被动回复次数上限）")
	}
	if c.Log.File == "" {
		problems = append(problems, "log.file 不能为空")
	}
	if c.Database.Path == "" {
		problems = append(problems, "database.path 不能为空")
	}

	if len(problems) > 0 {
		return fmt.Errorf("config: 校验失败：\n  - %s", strings.Join(problems, "\n  - "))
	}
	return nil
}

// resolvePath 按优先级确定配置文件路径：
// 1. 显式指定  2. 可执行文件同目录  3. 项目根（go.mod 所在目录）  4. 当前目录
func resolvePath(path string) string {
	if path != "" {
		return path
	}
	if exe, err := os.Executable(); err == nil {
		candidate := filepath.Join(filepath.Dir(exe), "config.yaml")
		if _, err := os.Stat(candidate); err == nil {
			return candidate
		}
	}
	if root, err := findProjectRoot(); err == nil {
		candidate := filepath.Join(root, "config.yaml")
		if _, err := os.Stat(candidate); err == nil {
			return candidate
		}
	}
	return "config.yaml"
}

func findProjectRoot() (string, error) {
	dir, err := os.Getwd()
	if err != nil {
		return "", err
	}
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir, nil
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return "", fmt.Errorf("config: 未找到 go.mod")
		}
		dir = parent
	}
}
