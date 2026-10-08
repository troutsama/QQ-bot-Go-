package logx

import (
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"

	"gopkg.in/natefinch/lumberjack.v2"
)

type Config struct {
	File       string // 例：data/logs/botd.log
	MaxSizeMB  int    // 单个文件上限，超了轮转
	MaxAgeDays int    // 保留天数
	Level      string // debug | info | warn | error
	Console    bool   // 是否同时打到 stdout
}

func Setup(cfg Config) (*slog.Logger, func() error, error) {
	if cfg.File == "" {
		return nil, nil, fmt.Errorf("logx: log.file 不能为空")
	}
	if dir := filepath.Dir(cfg.File); dir != "" && dir != "." {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return nil, nil, fmt.Errorf("logx: 创建日志目录 %q: %w", dir, err)
		}
	}

	rotator := &lumberjack.Logger{
		Filename:  cfg.File,
		MaxSize:   cfg.MaxSizeMB,
		MaxAge:    cfg.MaxAgeDays,
		LocalTime: true,
		Compress:  true,
	}

	var w io.Writer = rotator
	if cfg.Console {
		w = io.MultiWriter(os.Stdout, rotator)
	}

	level := new(slog.LevelVar)
	level.Set(ParseLevel(cfg.Level))
	handler := slog.NewTextHandler(w,
		&slog.HandlerOptions{Level: level})
	logger := slog.New(handler).With("service", "botd")

	closeFn := func() error {
		return rotator.Close()
	}
	return logger, closeFn, nil
}

func ParseLevel(s string) slog.Level {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "debug":
		return slog.LevelDebug
	case "warn", "warning":
		return slog.LevelWarn
	case "error":
		return slog.LevelError
	default:
		return slog.LevelInfo
	}
}

func NewRedactor(secrets ...string) func(string) string {
	return func(s string) string {
		for _, secret := range secrets {
			if len(secret) < 8 {
				continue
			}
			s = strings.ReplaceAll(s, secret, "***")
		}
		return s
	}
}
