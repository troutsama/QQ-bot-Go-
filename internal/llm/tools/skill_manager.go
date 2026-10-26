package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"qq-bot/internal/llm"
	"sync"
	"time"

	"gopkg.in/yaml.v3"
)

type SkillMeta struct {
	Name        string          `yaml:"name"`
	Description string          `yaml:"description"`
	Command     string          `yaml:"command"`
	Parameters  json.RawMessage `yaml:"parameters"`
	Entrypoint  string          `yaml:"entrypoint"`
}

type SkillManager struct {
	dir   string
	mu    sync.RWMutex
	defs  []llm.ToolDef
	execs map[string]ToolHandler
}

func NewSkillManager(dir string) *SkillManager {
	return &SkillManager{
		dir:   dir,
		execs: make(map[string]ToolHandler),
	}
}

// reload扫描 skills/ 目录并替换当前所有技能
func (m *SkillManager) Reload() error {
	entries, err := os.ReadDir(m.dir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil // 目录不存在则静默忽略
		}
		return fmt.Errorf("读取 skills 目录失败：%w", err)
	}

	newDefs := make([]llm.ToolDef, 0, len(entries))
	newExecs := make(map[string]ToolHandler, len(entries))

	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		skillDir := filepath.Join(m.dir, entry.Name())
		yamlPath := filepath.Join(skillDir, "skill.yaml")

		data, err := os.ReadFile(yamlPath)
		if err != nil {
			continue // 如果改目录没有skill.yaml跳过
		}
		var meta SkillMeta
		if err := yaml.Unmarshal(data, &meta); err != nil {
			continue
		}
		toolDef := llm.ToolDef{
			Name:        meta.Name,
			Description: meta.Description,
			InputSchema: meta.Parameters,
		}

		scriptPath := filepath.Join(skillDir, meta.Entrypoint)
		handler := makeScriptHandler(scriptPath)

		newDefs = append(newDefs, toolDef)
		newExecs[meta.Name] = handler
	}

	m.mu.Lock()
	m.defs = newDefs
	m.execs = newExecs
	m.mu.Unlock()

	return nil
}

// 返回所有热加载技能的定义
func (m *SkillManager) Defs() []llm.ToolDef {
	m.mu.Lock()
	defer m.mu.Unlock()
	return append([]llm.ToolDef(nil), m.defs...)
}

// 执行热加载技能
func (m *SkillManager) Execute(ctx context.Context, call llm.ToolCall) (string, error) {
	m.mu.Lock()
	handler, ok := m.execs[call.Name]

	if !ok {
		return "", fmt.Errorf("未知的技能插件：%q", call.Name)
	}
	return handler(ctx, call.Arguments)
}

func makeScriptHandler(scriptPath string) ToolHandler {
	return func(ctx context.Context, argsJSON string) (string, error) {
		execCtx, cancel := context.WithTimeout(ctx, 15*time.Second)
		defer cancel()

		var cmd *exec.Cmd
		switch filepath.Ext(scriptPath) {
		case ".py":
			cmd = exec.CommandContext(execCtx, "python3", scriptPath, argsJSON)
		case ".sh":
			cmd = exec.CommandContext(execCtx, "bash", scriptPath, argsJSON)
		case ".js":
			cmd = exec.CommandContext(execCtx, "node", scriptPath, argsJSON)
		default:
			cmd = exec.CommandContext(execCtx, scriptPath, argsJSON)
		}

		output, err := cmd.Output()
		if err != nil {
			return "", fmt.Errorf("脚本执行失败：%w， 详情：%s", err, string(output))
		}
		return string(output), nil
	}
}
