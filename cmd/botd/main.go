// Command botd 是 QQ 机器人的唯一二进制。
//
// 本文件是「组合根」：所有对象的创建都集中在这里，然后按参数往下传。
// 没有包级变量、没有 init() 魔法、没有 GetXxx() —— 这是可测试性的前提（02 §2.1）。
package main

import (
	"context"
	"flag"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"syscall"
	"time"

	"qq-bot/internal/chat"
	"qq-bot/internal/config"
	"qq-bot/internal/logx"
	"qq-bot/internal/store"
	"qq-bot/internal/version"

	// 两个工厂包都叫 factory，必须起别名，否则撞名。
	llmfactory "qq-bot/internal/llm/factory"
	platformfactory "qq-bot/internal/platform/factory"
)

func main() {
	cfgPath := flag.String("config", "", "配置文件路径（默认自动查找 config.yaml）")
	showVersion := flag.Bool("version", false, "打印版本后退出")
	migrateStatus := flag.Bool("migrate-status", false, "打印数据库迁移版本后退出（不启动服务）")
	flag.Parse()

	if *showVersion {
		fmt.Printf("botd %s (build %s)\n", version.GitHash, version.BuildAt)
		return
	}

	// 只做三件事：解析 flag → 调 run → 处理退出码。
	// 业务逻辑一行都不写在这里，这样 run() 才有被测试的可能。
	if err := run(*cfgPath, *migrateStatus); err != nil {
		// ⚠️ 不用 panic：启动失败要打印一行人能读懂的错，并以退出码 1 结束，
		// 这样 systemd / launchd 才能正确判断（04 决策表 D-1）。
		fmt.Fprintln(os.Stderr, "启动失败:", err)
		os.Exit(1)
	}
}

// run 把「启动 → 跑 → 优雅关闭」放在一个函数里，好处是 defer 的作用域清晰。
func run(cfgPath string, migrateStatus bool) error {
	// ── 1. 配置 ──
	// 全部从配置文件读，没有环境变量覆盖（见 internal/config 的说明）。
	cfg, err := config.Load(cfgPath)
	if err != nil {
		return err
	}
	if err := cfg.Validate(); err != nil {
		return err
	}

	// 业务展示时区只在这里加载一次：chat 包不碰 time.Local，
	// 部署到 UTC 机器也不会让上下文时间前缀整体偏移（R20）。
	loc, err := time.LoadLocation(cfg.Chat.Timezone)
	if err != nil {
		return fmt.Errorf("加载时区 %q 失败: %w", cfg.Chat.Timezone, err)
	}

	// ── 2. 日志 ──
	log, closeLog, err := logx.Setup(logx.Config{
		File:       cfg.Log.File,
		MaxSizeMB:  cfg.Log.MaxSizeMB,
		MaxAgeDays: cfg.Log.MaxAgeDays,
		Level:      cfg.Log.Level,
		Console:    true,
	})
	if err != nil {
		return err
	}
	// defer 是 LIFO（最后注册的最先执行）：日志要**最晚**关，
	// 这样其它清理动作才能往日志里写字。
	defer func() { _ = closeLog() }()

	log.Info("启动", "version", version.GitHash, "config_path", cfgPath)

	// ── 3. 信号 → ctx ──
	// 一行把 SIGINT/SIGTERM 变成可 select 的 ctx.Done()，比自己写信号 channel 干净；
	// 而且取消会向下传递（token 刷新协程、WS 连接都靠它停）。
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	// ── 4. 数据库：进程内打开 + 启动时迁移 ──
	// SQLite 没有服务进程，不存在「先启动数据库」这一步；Open 只连接，
	// 表结构由 Migrate 按 PRAGMA user_version 推进（03 §5.6）。
	st, err := store.Open(cfg.Database)
	if err != nil {
		return err
	}
	defer func() {
		if err := st.Close(); err != nil {
			log.Warn("关闭数据库出错", "error", err)
		}
	}()

	// 只读子命令：打印版本后退出，不启动平台连接与业务循环。
	if migrateStatus {
		current, err := st.MigrationVersion(ctx)
		if err != nil {
			return err
		}
		latest, err := st.LatestMigrationVersion()
		if err != nil {
			return err
		}
		fmt.Printf("user_version = %d (target %d)\n", current, latest)
		return nil
	}

	if err := st.Migrate(ctx); err != nil {
		return err
	}
	ver, err := st.MigrationVersion(ctx)
	if err != nil {
		return err
	}
	log.Info("迁移完成", "user_version", ver)

	// ── 4.1 审计事件清理（M1 待办 T5.2）──
	// 启动时清一次、之后每 24h 一次。协程挂在独立的 ctx 上，退出时先 cancel
	// 再等它结束 —— 否则它可能正卡在 DELETE 上，和后面的 st.Close() 抢连接。
	cleanupCtx, stopCleanup := context.WithCancel(ctx)
	cleanupDone := make(chan struct{})
	go func() {
		defer close(cleanupDone)
		cleanupEvents(cleanupCtx, st, log, eventRetentionDays)
	}()
	defer func() {
		stopCleanup()
		<-cleanupDone
	}()

	// ── 5. 适配器：全项目唯一的两个 switch，都在工厂包里 ──
	provider, err := platformfactory.New(cfg.Bot, log)
	if err != nil {
		return err
	}
	llmClient, err := llmfactory.New(cfg.LLM, log)
	if err != nil {
		return err
	}
	log.Info("适配器就绪", "provider", provider.Name(), "llm", llmClient.Name())

	// ── 6. 连接平台 ──
	if err := provider.Start(ctx); err != nil {
		return fmt.Errorf("平台连接失败: %w", err)
	}

	// ── 7. 业务服务：去重 → 分片串行 → 上下文 → 模型 → 分段回复 ──
	// Cfg 传函数而不是值：将来热重载时每条消息都能读到最新快照（M3 只做读取，不接热重载）。
	svc := chat.New(chat.Deps{
		Provider: provider,
		LLM:      llmClient,
		Store:    st,
		Cfg:      func() config.Config { return *cfg },
		Now:      time.Now,
		Loc:      loc,
		Log:      log,
	})
	if err := svc.Run(ctx); err != nil {
		return err
	}
	log.Info("业务服务已退出")

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

// eventRetentionDays 是审计事件的保留天数。
// 暂时硬编码（M1 待办 T5.2）；等 log.event_retention_days 进配置后改为读配置。
const eventRetentionDays = 14

// cleanupEvents 启动时清理一次过期事件，之后每 24 小时重复一次，直到 ctx 取消。
//
// 用 Ticker 而不是 time.After 驱动循环：Ticker 只建一个定时器、反复使用，
// 而 time.After 每轮都会新建一个 Timer（长时间运行的服务里前者才是惯用法）。
// 先清理再等待，是为了进程重启后立刻回收上次留下的过期数据。
func cleanupEvents(ctx context.Context, st *store.Store, log *slog.Logger, retainDays int) {
	ticker := time.NewTicker(24 * time.Hour)
	defer ticker.Stop()

	for {
		n, err := st.CleanupEvents(ctx, retainDays)
		switch {
		case err != nil:
			// 关闭时 ctx 先被取消，查询会跟着失败；这类错误不必刷日志。
			if ctx.Err() == nil {
				log.Warn("清理过期事件失败", "error", err)
			}
		case n > 0:
			log.Info("清理过期事件", "deleted", n, "retain_days", retainDays)
		}

		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}
