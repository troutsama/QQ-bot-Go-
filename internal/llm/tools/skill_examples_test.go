package tools

import (
	"context"
	"encoding/json"
	"path/filepath"
	"testing"

	"qq-bot/internal/llm"
)

func TestSkillManager_ExampleSkills(t *testing.T) {
	root := filepath.Dir(filepath.Dir(exampleScript(t, "get_location", "run.py")))
	manager := NewSkillManager(root)
	if err := manager.Reload(); err != nil {
		t.Fatal(err)
	}
	defs := manager.Defs()
	if len(defs) != 2 {
		t.Fatalf("应加载两个工具，实际 %d；检查 YAML 解析是否被忽略", len(defs))
	}
	for _, def := range defs {
		var schema struct {
			Type string `json:"type"`
		}
		if err := json.Unmarshal(def.InputSchema, &schema); err != nil || schema.Type != "object" {
			t.Fatalf("工具 %s 的 schema 无效：%s；err=%v", def.Name, def.InputSchema, err)
		}
	}
	// 连续调用两次，验证 Execute 不会遗留未释放的锁。
	for i := 0; i < 2; i++ {
		location, err := manager.Execute(context.Background(), llm.ToolCall{
			ID: "location", Name: "get_location", Arguments: "{}",
		})
		if err != nil {
			t.Fatal(err)
		}
		weather, err := manager.Execute(context.Background(), llm.ToolCall{
			ID: "weather", Name: "get_weather", Arguments: location,
		})
		if err != nil {
			t.Fatal(err)
		}
		var got struct {
			Location string `json:"location"`
			Weather  string `json:"weather"`
		}
		if err := json.Unmarshal([]byte(weather), &got); err != nil {
			t.Fatal(err)
		}
		if got.Location != "湖南长沙" || got.Weather != "晴天" {
			t.Fatalf("结果不符：%s", weather)
		}
	}
}
