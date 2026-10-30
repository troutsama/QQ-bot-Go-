package config

import (
	"os"
	"path/filepath"
	"testing"
)

func TestExampleAndSkillDirectory(t *testing.T) {
	raw, err := os.ReadFile("../../config.yaml.example")
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "config.yaml")
	if err := os.WriteFile(path, raw, 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Tools.Directory != filepath.Join(filepath.Dir(path), "skills") {
		t.Fatalf("技能目录 = %q", cfg.Tools.Directory)
	}
	if cfg.Tools.MaxSteps != 5 {
		t.Fatalf("max_steps = %d", cfg.Tools.MaxSteps)
	}
}

func TestValidateRuntimeLimits(t *testing.T) {
	base := Config{
		Server: ServerConfig{AdminToken: "test"}, Bot: BotConfig{Provider: "mock"},
		LLM: LLMConfig{Protocol: "fake"}, Log: LogConfig{File: "test.log"},
	}
	base.applyDefaults()
	for _, tc := range []struct {
		name   string
		change func(*Config)
	}{
		{"timeout", func(c *Config) { c.LLM.Timeout = "0s" }},
		{"segments", func(c *Config) { c.Chat.MaxReplySegments = -1 }},
		{"chars", func(c *Config) { c.Chat.SegmentMaxChars = -1 }},
		{"history", func(c *Config) { c.Chat.ContextTurns = -1 }},
		{"tools", func(c *Config) { c.Tools.MaxSteps = -1 }},
		{"model_credentials", func(c *Config) { c.LLM.Protocol = "openai-chat"; c.LLM.BaseURL = "https://example.test/v1" }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cfg := base
			tc.change(&cfg)
			if cfg.Validate() == nil {
				t.Fatal("非法配置应失败")
			}
		})
	}
}
