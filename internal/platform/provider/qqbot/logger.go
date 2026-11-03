package qqbot

import (
	"fmt"
	"log/slog"

	botgolog "github.com/tencent-connect/botgo/log"
)

// botgoLogger 把 botgo 的日志接口适配到 slog（04 R12：不接管会出现两套格式、
// 而且 botgo 的日志既不进文件也不带我们的字段）。
//
// ⚠️ botgo v0.2.1 的接口有 **9 个方法**，最后一个是 Sync() error。
//
// 设计文档 06 §10.7.4 的示例只写了 8 个 —— 照抄会得到
// `*qqbot.botgoLogger does not implement log.Logger (missing method Sync)`。
// 下面这行编译期断言就是为了第一时间抓住「接口方法集不完整」。
//
// 注：Go 1.19 之后 gofmt 会把**缩进的注释行**当成代码块重新排版（自动加空行 + Tab），
// 所以这里刻意不缩进续行。自己写的时候如果 gofmt 老是改你的注释，就是这个原因。
type botgoLogger struct {
	l *slog.Logger
}

var _ botgolog.Logger = botgoLogger{}

func (b botgoLogger) Debug(v ...any) { b.l.Debug(fmt.Sprint(v...)) }
func (b botgoLogger) Info(v ...any)  { b.l.Info(fmt.Sprint(v...)) }
func (b botgoLogger) Warn(v ...any)  { b.l.Warn(fmt.Sprint(v...)) }
func (b botgoLogger) Error(v ...any) { b.l.Error(fmt.Sprint(v...)) }

func (b botgoLogger) Debugf(format string, v ...any) { b.l.Debug(fmt.Sprintf(format, v...)) }
func (b botgoLogger) Infof(format string, v ...any)  { b.l.Info(fmt.Sprintf(format, v...)) }
func (b botgoLogger) Warnf(format string, v ...any)  { b.l.Warn(fmt.Sprintf(format, v...)) }
func (b botgoLogger) Errorf(format string, v ...any) { b.l.Error(fmt.Sprintf(format, v...)) }

// Sync：log.Logger 接口在 v0.2.1 里的第 9 个方法。
// slog **没有**缓冲，不需要 flush；文件落盘由 lumberjack 自己负责 → 返回 nil 是对的。
func (b botgoLogger) Sync() error { return nil }
