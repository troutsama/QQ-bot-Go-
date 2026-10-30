package tools

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

func makeScriptHandler(scriptPath string) ToolHandler {
	// 在创建 handler 时固定路径，调用期间不改变进程全局工作目录。
	absPath, pathErr := filepath.Abs(scriptPath)
	return func(ctx context.Context, argsJSON string) (string, error) {
		if pathErr != nil {
			return "", fmt.Errorf("解析脚本路径: %w", pathErr)
		}
		var interpreter string
		switch filepath.Ext(absPath) {
		case ".py":
			interpreter = "python3"
		case ".js":
			interpreter = "node"
		default:
			return "", fmt.Errorf("不支持的脚本后缀 %q", filepath.Ext(absPath))
		}

		execCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
		defer cancel()
		if err := execCtx.Err(); err != nil {
			return "", fmt.Errorf("脚本调用已取消: %w", err)
		}
		cmd := exec.CommandContext(execCtx, interpreter, absPath, argsJSON)
		cmd.Dir = filepath.Dir(absPath)
		// 防止子进程退出后，继承输出管道的进程让 Run 一直等待。
		cmd.WaitDelay = time.Second
		var stdout, stderr bytes.Buffer
		cmd.Stdout = &stdout
		cmd.Stderr = &stderr
		if err := cmd.Run(); err != nil {
			if ctxErr := execCtx.Err(); ctxErr != nil {
				return "", fmt.Errorf("脚本调用取消或超时: %w", ctxErr)
			}
			return "", fmt.Errorf("脚本 %s 执行失败: %w; stderr=%s",
				filepath.Base(absPath), err, strings.TrimSpace(stderr.String()))
		}
		output := strings.TrimSpace(stdout.String())
		if !json.Valid([]byte(output)) {
			return "", fmt.Errorf("脚本 %s 的 stdout 必须是单个 JSON 值", filepath.Base(absPath))
		}
		return output, nil
	}
}
