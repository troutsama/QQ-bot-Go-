package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"qq-bot/internal/llm"
	"sync"

	"gopkg.in/yaml.v3"
)

type SkillMeta struct {
	Name        string         `yaml:"name"`
	Description string         `yaml:"description"`
	Command     string         `yaml:"command"`
	Parameters  map[string]any `yaml:"parameters"`
	Entrypoint  string         `yaml:"entrypoint"`
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
			return fmt.Errorf("技能目录不存在 %s: %w", m.dir, err)
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
			if os.IsNotExist(err) {
				continue
			}
			return fmt.Errorf("读取 %s: %w", yamlPath, err)
		}
		var meta SkillMeta
		if err := yaml.Unmarshal(data, &meta); err != nil {
			return fmt.Errorf("解析 %s: %w", yamlPath, err)
		}
		if meta.Name == "" || meta.Entrypoint == "" || meta.Parameters == nil {
			return fmt.Errorf("技能 %s 缺少 name、entrypoint 或 parameters", yamlPath)
		}
		if _, exists := newExecs[meta.Name]; exists {
			return fmt.Errorf("技能名称 %q 重复", meta.Name)
		}
		schema, err := json.Marshal(meta.Parameters)
		if err != nil {
			return fmt.Errorf("编码 %s 的 parameters: %w", yamlPath, err)
		}
		toolDef := llm.ToolDef{
			Name:        meta.Name,
			Description: meta.Description,
			InputSchema: schema,
		}

		scriptPath := filepath.Join(skillDir, meta.Entrypoint)
		if !filepath.IsLocal(meta.Entrypoint) {
			return fmt.Errorf("技能 %s 的 entrypoint 必须位于技能目录内", meta.Name)
		}
		if info, err := os.Stat(scriptPath); err != nil {
			return fmt.Errorf("读取技能入口 %s: %w", scriptPath, err)
		} else if !info.Mode().IsRegular() {
			return fmt.Errorf("技能入口 %s 不是普通文件", scriptPath)
		}
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
	m.mu.RLock()
	defer m.mu.RUnlock()
	return append([]llm.ToolDef(nil), m.defs...)
}

// 执行热加载技能
func (m *SkillManager) Execute(ctx context.Context, call llm.ToolCall) (string, error) {
	m.mu.RLock()
	handler, ok := m.execs[call.Name]
	m.mu.RUnlock()

	if !ok {
		return "", fmt.Errorf("未知的技能插件：%q", call.Name)
	}
	return handler(ctx, call.Arguments)
}
