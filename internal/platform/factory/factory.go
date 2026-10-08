// Package factory 按配置创建 platform.Provider。
//
// 为什么工厂要**单独成一个包**，而不是放在 internal/platform/factory.go（设计文档 02 §2.4 的写法）：
//
//	platform 包若 import platform/provider/mock，而 mock 又要 import platform
//	（它必须实现 platform.Provider），就构成**包级 import cycle**，编译直接报
//	"import cycle not allowed"。
//
// Go 的 import 环检测看的是「包」，不是目录层级 —— 父子目录之间照样不能互相 import。
// 所以依赖图是：factory → {platform, mock, qqbot}，而 mock/qqbot → platform，无环。
package factory

import (
	"fmt"
	"log/slog"

	"qq-bot/internal/config"
	"qq-bot/internal/platform"
	"qq-bot/internal/platform/provider/mock"
	"qq-bot/internal/platform/provider/qqbot"
)

// New 按配置创建 Provider。这是全项目**唯一的 provider switch**。
//
// 为什么不用 init() 注册表（02 §2.4）：
//   - init 的隐式副作用让「谁注册了什么」要靠全局搜索
//   - 单测里无法控制注册内容
//   - 重复注册要 panic
//   - V1 只有 2 个实现，注册表是过度设计
//
// 升级路径：真需要插件化时，把 switch 换成
// map[string]func(config.BotConfig, *slog.Logger) (platform.Provider, error) + Register()，
// 调用点一行不改。
//
// ⚠️ 这个工厂**不缓存实例**，每次调用返回新对象。
// 一旦写成 if inst != nil { return inst }，它就变成了隐式的全局单例：
// TestA 拿到的 mock 会被 TestB 复用，t.Parallel() 立刻互相污染。
// 「全进程只有一份」这件事由组合根（cmd/botd/main.go）保证，不由工厂保证。
func New(cfg config.BotConfig, log *slog.Logger) (platform.Provider, error) {
	switch cfg.Provider {
	case "qqbot":
		return qqbot.New(cfg, log)
	case "mock":
		return mock.New(), nil
	default:
		// 返回 error 而不是 panic：启动失败要能被上层优雅汇报（04 决策表 D-1）
		return nil, fmt.Errorf("platform: 未知 provider %q（可用：qqbot, mock）", cfg.Provider)
	}
}
