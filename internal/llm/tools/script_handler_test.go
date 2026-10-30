package tools

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"testing"
	"time"
)

// 根据测试文件位置找脚本，不依赖 go test 的工作目录。
func exampleScript(t *testing.T, name, entry string) string {
	t.Helper()
	_, filename, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("无法定位测试文件")
	}
	return filepath.Join(filepath.Dir(filename), "..", "..", "..", "skills", name, entry)
}

func TestScriptHandlers_LocationThenWeather(t *testing.T) {
	locationHandler := makeScriptHandler(exampleScript(t, "get_location", "run.py"))
	if locationHandler == nil {
		t.Fatal("未创建位置 handler")
	}
	locationJSON, err := locationHandler(context.Background(), "{}")
	if err != nil {
		t.Fatal(err)
	}
	var location struct {
		Location string `json:"location"`
	}
	if err := json.Unmarshal([]byte(locationJSON), &location); err != nil {
		t.Fatal(err)
	}
	if location.Location != "湖南长沙" {
		t.Fatalf("位置 = %q", location.Location)
	}

	// 本例的位置输出恰好符合天气输入协议，直接传递即可。
	weatherHandler := makeScriptHandler(exampleScript(t, "get_weather", "run.js"))
	weatherJSON, err := weatherHandler(context.Background(), locationJSON)
	if err != nil {
		t.Fatal(err)
	}
	var weather struct {
		Location string `json:"location"`
		Weather  string `json:"weather"`
	}
	if err := json.Unmarshal([]byte(weatherJSON), &weather); err != nil {
		t.Fatal(err)
	}
	if weather.Location != "湖南长沙" || weather.Weather != "晴天" {
		t.Fatalf("天气结果不符：%+v", weather)
	}
}

func TestScriptHandler_RejectsBadWeatherArguments(t *testing.T) {
	handler := makeScriptHandler(exampleScript(t, "get_weather", "run.js"))
	if handler == nil {
		t.Fatal("未创建天气 handler")
	}
	output, err := handler(context.Background(), "{}")
	if err == nil || output != "" {
		t.Fatalf("期望参数错误，得到 output=%q err=%v", output, err)
	}
}

func TestScriptHandler_DeadlineStopsRunningProcess(t *testing.T) {
	script := filepath.Join(t.TempDir(), "slow.py")
	if err := os.WriteFile(script, []byte("import time\ntime.sleep(30)\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
	defer cancel()
	start := time.Now()
	handler := makeScriptHandler(script)
	if handler == nil {
		t.Fatal("未创建超时 handler")
	}
	_, err := handler(ctx, "{}")
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("期望 DeadlineExceeded，得到 %v", err)
	}
	if time.Since(start) > 5*time.Second {
		t.Fatal("子进程没有及时退出")
	}
}

func TestScriptHandler_RejectsPollutedStdout(t *testing.T) {
	script := filepath.Join(t.TempDir(), "noisy.py")
	if err := os.WriteFile(script, []byte("print('debug log')\nprint('{}')\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	handler := makeScriptHandler(script)
	if handler == nil {
		t.Fatal("未创建输出校验 handler")
	}
	output, err := handler(context.Background(), "{}")
	if err == nil || output != "" {
		t.Fatalf("期望 JSON 输出错误，得到 output=%q err=%v", output, err)
	}
}
