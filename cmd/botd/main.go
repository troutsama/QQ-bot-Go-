// Command botd 是 QQ 机器人的唯一二进制。
//
// 本文件是「组合根」：所有对象的创建都集中在这里，然后按参数往下传。
// 没有包级变量、没有 init() 魔法、没有 GetXxx() —— 这是可测试性的前提（02 §2.1）。
package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"qq-bot/internal/config"
	"qq-bot/internal/logx"
	"qq-bot/internal/store"
	"qq-bot/internal/version"
	"syscall"
	"time"

	// 两个工厂包都叫 factory，必须起别名，否则撞名。
	llmfactory "qq-bot/internal/llm/factory"
	platformfactory "qq-bot/internal/platform/factory"
)

func main() {
	cfgPath := flag.String("config", "", "配置文件路径（默认自动查找 config.yaml）")
	showVersion := flag.Bool("version", false, "打印版本后退出")
	flag.Parse()

	if *showVersion {
		fmt.Printf("botd %s (build %s)\n", version.GitHash, version.BuildAt)
		return
	}

	// 只做三件事：解析 flag → 调 run → 处理退出码。
	// 业务逻辑一行都不写在这里，这样 run() 才有被测试的可能。
	if err := run(*cfgPath); err != nil {
		// ⚠️ 不用 panic：启动失败要打印一行人能读懂的错，并以退出码 1 结束，
		// 这样 systemd / launchd 才能正确判断（04 决策表 D-1）。
		fmt.Fprintln(os.Stderr, "启动失败:", err)
		os.Exit(1)
	}
}

// run 把「启动 → 跑 → 优雅关闭」放在一个函数里，好处是 defer 的作用域清晰。
func run(cfgPath string) error {
	// ── 1. 配置 ──
	// 全部从配置文件读，没有环境变量覆盖（见 internal/config 的说明）。
	cfg, err := config.Load(cfgPath)
	if err != nil {
		return err
	}
	if err := cfg.Validate(); err != nil {
		return err
	}

	// ── 2. 数据库 ──
	// 先建库、后建日志：日志要挂一路下游，把业务事件写进 events 表，
	// 那一路需要 st 才能工作。
	st, err := store.Open(cfg.Database)
	if err != nil {
		return fmt.Errorf("数据库初始化失败: %w", err)
	}
	// defer 是 LIFO：st.Close 必须**最后**执行（日志收尾时还要写库），所以最先注册。
	defer func() { _ = st.Close() }()

	// ── 3. 日志 ──
	// 第一路：文件 + 控制台，负责排障；第二路：store.EventHandler，负责审计。
	// 两路打的是同一条记录，但只有带 kind 属性的记录会进 events 表（03 §5.7）。
	log, closeLog, err := logx.Setup(logx.Config{
		File:       cfg.Log.File,
		MaxSizeMB:  cfg.Log.MaxSizeMB,
		MaxAgeDays: cfg.Log.MaxAgeDays,
		Level:      cfg.Log.Level,
		Console:    true,
	}, store.NewEventHandler(st))
	if err != nil {
		return err
	}
	// closeLog 要在 st.Close 之前跑：它先把落库侧收尾，之后才轮得到关库连接。
	defer func() { _ = closeLog() }()

	log.Info("启动", "version", version.GitHash, "config_path", cfgPath)

	// ── 4. 信号 → ctx ──
	// 一行把 SIGINT/SIGTERM 变成可 select 的 ctx.Done()，比自己写信号 channel 干净；
	// 而且取消会向下传递（token 刷新协程、WS 连接都靠它停）。
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	// ── 5. 适配器：全项目唯一的两个 switch，都在工厂包里 ──
	// 数据库实例交给工厂，由它注入到需要落库的适配器；
	// main 自己不往下传，业务循环也不再拿到它。
	provider, err := platformfactory.New(cfg.Bot, log, st)
	if err != nil {
		return err
	}
	llmClient, err := llmfactory.New(cfg.LLM, log) // TODO 数据库实例需要注入这里
	if err != nil {
		return err
	}
	log.Info("适配器就绪", "provider", provider.Name(), "llm", llmClient.Name())

	// ── 6. 连接平台 ──
	if err := provider.Start(ctx); err != nil {
		return fmt.Errorf("平台连接失败: %w", err)
	}

	// ── 7. 等待退出信号 ──
	// 收消息、入库、回复的循环已经在适配器内部以 goroutine 跑起来了（见 qqbot.Run），
	// main 不再处理消息，只阻塞到 SIGINT/SIGTERM，再走下面的关闭。
	// 将来这层会换成 chat.New(chat.Deps{Provider: provider, LLM: llmClient, ...}).Run(ctx)（07 M3）。
	<-ctx.Done()
	log.Info("收到退出信号", "reason", ctx.Err())

	// ── 8. 优雅关闭（顺序见 03 §5.8：先停收，再停发，最后关资源）──
	// 关闭走**显式调用**而不是只靠 defer：因为要给它设超时上限。
	log.Info("开始关闭")
	shutCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := provider.Stop(shutCtx); err != nil {
		log.Warn("断开平台出错", "error", err) // 关闭出错也不阻塞退出
	}
	log.Info("已退出")
	return nil
}
