package logx

import (
	"errors"
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

// Setup 建好「文件 + 控制台」这条日志轨道，再按需挂上下游 handler。
//
// extra 是调用方注入的额外下游，目前唯一的用途是 store.NewEventHandler(st)：
// 让带 kind 属性的业务事件额外写进 events 表（03 §5.7 日志双轨 ——
// 文件负责排障，DB 负责审计与查询，两者不要混为一谈）。
//
// logx 不认识 store，只认识 slog.Handler：具体由谁落库、落到哪，全部在组合根决定。
func Setup(cfg Config, extra ...slog.Handler) (*slog.Logger, func() error, error) {
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
	var handler slog.Handler = slog.NewTextHandler(w,
		&slog.HandlerOptions{Level: level})
	if len(extra) > 0 {
		handler = multiHandler(append([]slog.Handler{handler}, extra...))
	}
	logger := slog.New(handler).With("service", "botd")

	closeFn := func() error {
		var errs []error
		// 先关下游，再关文件：落库侧若还有缓冲，需要在文件句柄销毁前收尾。
		for _, h := range extra {
			if c, ok := h.(io.Closer); ok {
				errs = append(errs, c.Close())
			}
		}
		errs = append(errs, rotator.Close())
		return errors.Join(errs...)
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
