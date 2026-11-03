package logx

import (
	"context"
	"errors"
	"log/slog"
)

// multiHandler 把一条记录同时交给多路 handler：
// 第一路是文件/控制台（运行日志），后面的路是 caller 注入的下游（比如业务事件落库）。
//
// 标准库没有现成的多路分发，所以手写这几十行。关键点在 Enabled：
// 每一路自己判断级别，文件侧按 cfg.level 过滤，落库侧可以放行全部级别 ——
// 不能在这里统一拦掉，否则「文件按 info 过滤」会顺带把 warn 以下的业务事件吞掉。
type multiHandler []slog.Handler

func (m multiHandler) Enabled(ctx context.Context, level slog.Level) bool {
	for _, h := range m {
		if h.Enabled(ctx, level) {
			return true
		}
	}
	return false
}

func (m multiHandler) Handle(ctx context.Context, r slog.Record) error {
	var errs []error
	for _, h := range m {
		if !h.Enabled(ctx, r.Level) {
			continue
		}
		if err := h.Handle(ctx, r); err != nil {
			errs = append(errs, err)
		}
	}
	return errors.Join(errs...)
}

func (m multiHandler) WithAttrs(attrs []slog.Attr) slog.Handler {
	if len(attrs) == 0 {
		return m
	}
	next := make(multiHandler, len(m))
	for i, h := range m {
		next[i] = h.WithAttrs(attrs)
	}
	return next
}

func (m multiHandler) WithGroup(name string) slog.Handler {
	if name == "" {
		return m
	}
	next := make(multiHandler, len(m))
	for i, h := range m {
		next[i] = h.WithGroup(name)
	}
	return next
}
