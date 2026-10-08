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

	"qq-bot/internal/config"
	"qq-bot/internal/logx"
	"qq-bot/internal/platform"
	"qq-bot/internal/version"

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

	// ── 4. 适配器：全项目唯一的两个 switch，都在工厂包里 ──
	provider, err := platformfactory.New(cfg.Bot, log)
	if err != nil {
		return err
	}
	llmClient, err := llmfactory.New(cfg.LLM, log)
	if err != nil {
		return err
	}
	log.Info("适配器就绪", "provider", provider.Name(), "llm", llmClient.Name())

	// ── 5. 连接平台 ──
	if err := provider.Start(ctx); err != nil {
		return fmt.Errorf("平台连接失败: %w", err)
	}

	// ── 6. 临时业务：收到什么回什么 ──
	// 将来这里替换为 chat.New(chat.Deps{Provider: provider, LLM: llmClient, ...}).Run(ctx)（07 M3）。
	if err := echoLoop(ctx, provider, log); err != nil {
		return err
	}

	// ── 7. 优雅关闭（顺序见 03 §5.8：先停收，再停发，最后关资源）──
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

// echoLoop 是临时「业务」，唯一目的是证明通信底座通了：
// ① 事件能收到；② 被动回复凭据（msg_id）能用；③ Send 能真的发出去。
//
// 它会在 M3 被 chat.Service 整体替换，所以刻意写得很薄、不掺业务逻辑。
func echoLoop(ctx context.Context, p platform.Provider, log *slog.Logger) error {
	for {
		select {
		case <-ctx.Done():
			return nil
		case msg, ok := <-p.Events():
			if !ok {
				return nil // Provider 已关闭事件流
			}
			log.Info("收到消息",
				"conv", msg.Conv.String(),
				"msg_id", msg.MsgID,
				"has_attachment", msg.HasAttachment,
				"text", msg.Text,
			)

			if msg.Text == "" {
				continue // 只发了图片/语音，V1 不处理（01 §1.3 的处理表）
			}

			// ⚠️ 每条入站消息都要**新建**一个 ReplyRef：
			//    「一次入站消息 → 一个 ReplyRef → 一串递增的 msg_seq」是绑定的一对。
			reply := platform.NewReplyRef(msg.MsgID, msg.EventID)

			if _, err := p.Send(ctx, platform.OutgoingMessage{
				Conv:  msg.Conv,
				Reply: reply,
				Kind:  platform.OutText,
				Text:  "你说的是：" + msg.Text,
			}); err != nil {
				log.Error("回显失败", "error", err, "msg_id", msg.MsgID)
			}
		}
	}
}
