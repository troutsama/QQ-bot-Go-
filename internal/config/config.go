package config

import (
	"fmt"
	"os"
	"path/filepath"

	"gopkg.in/yaml.v3"
)

type Config struct {
	QQ  QQConfig  `yaml:"qq"`
	LLM LLMConfig `yaml:"llm"`
}

type QQConfig struct {
	AppID     string `yaml:"app_id"`
	AppSecret string `yaml:"app_secret"`
}

type LLMConfig struct {
	BaseURL string `yaml:"base_url"`
	APIKey  string `yaml:"api_key"`
	Model   string `yaml:"model"`
	Timeout string `yaml:"timeout"`
}

// findProjectRoot 从当前目录向上查找 go.mod 所在目录即项目根目录
func findProjectRoot() (string, error) {
	cwd, err := os.Getwd()
	if err != nil {
		return "", err
	}
	dir := cwd
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir, nil
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return "", fmt.Errorf("未找到 go.mod，无法确定项目根目录")
		}
		dir = parent
	}
}

// resolvePath 按优先级确定配置文件路径：
// 1. 指定路径（非空直接使用）
// 2. 可执行文件同目录下的 config.yaml（编译后运行）
// 3. 项目根目录（go.mod 所在目录）下的 config.yaml（go run 开发）
// 4. 当前工作目录下的 config.yaml
func resolvePath(path string) string {
	if path != "" {
		return path
	}

	// 优先找可执行文件同目录
	exe, err := os.Executable()
	if err == nil {
		candidate := filepath.Join(filepath.Dir(exe), "config.yaml")
		if _, err := os.Stat(candidate); err == nil {
			return candidate
		}
	}

	// 其次找项目根目录（go.mod 所在目录）
	root, err := findProjectRoot()
	if err == nil {
		candidate := filepath.Join(root, "config.yaml")
		if _, err := os.Stat(candidate); err == nil {
			return candidate
		}
	}

	// 最后回退到当前工作目录
	return "config.yaml"
}

// Load 加载配置文件。path 为空时自动从项目根目录或可执行文件同目录查找 config.yaml。
func Load(path string) (*Config, error) {
	actualPath := resolvePath(path)
	data, err := os.ReadFile(actualPath)
	if err != nil {
		return nil, err
	}
	var cfg Config
	if err := yaml.Unmarshal(data, &cfg); err != nil {
		return nil, err
	}

	return &cfg, nil
}
