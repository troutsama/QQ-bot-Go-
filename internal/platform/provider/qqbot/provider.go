package qqbot

import (
	"context"
	"fmt"
	"log/slog"
	"qq-bot/internal/config"
	"qq-bot/internal/platform"
	"sync"

	"github.com/tencent-connect/botgo"
	"github.com/tencent-connect/botgo/dto"
	"github.com/tencent-connect/botgo/event"
	"github.com/tencent-connect/botgo/openapi"
	"github.com/tencent-connect/botgo/token"
	"golang.org/x/oauth2"
)

var _ platform.Provider = (*Provider)(nil)

type Provider struct {
	cfg      config.BotConfig
	appID    string
	log      *slog.Logger
	api      openapi.OpenAPI
	tokenSrc oauth2.TokenSource

	events chan platform.IncomingMessage
	done   chan struct{}

	sendMu   sync.RWMutex
	closed   bool
	mu       sync.RWMutex
	status   platform.Status
	stopOnce sync.Once
}

func New(cfg config.BotConfig, log *slog.Logger) (*Provider, error) {
	if log == nil {
		log = slog.Default()
	}
	q := cfg.QQBot
	if q.AppID == "" || q.AppSecret == "" {
		return nil, fmt.Errorf("qqbot: app_id 与 app_secret 必填")
	}
	// 接管 sdk 日志
	botgo.SetLogger(botgoLogger{l: log.With("component", "botgo")})
	// token source
	ts := token.NewQQBotTokenSource(&token.QQBotCredentials{
		AppID:     q.AppID,
		AppSecret: q.AppSecret,
	})
	// openapi
	var api openapi.OpenAPI
	if q.Sandbox {
		api = botgo.NewSandboxOpenAPI(q.AppID, ts)
	} else {
		api = botgo.NewOpenAPI(q.AppID, ts)
	}
	return &Provider{
		cfg:      cfg,
		appID:    q.AppID,
		log:      log,
		api:      api,
		tokenSrc: ts,
		events:   make(chan platform.IncomingMessage, 256),
		done:     make(chan struct{}),
		status:   platform.StatusDisconnected,
	}, nil
}

func (p *Provider) Name() string {
	return "qqbot"
}

func (p *Provider) Events() <-chan platform.IncomingMessage {
	return p.events
}

func (p *Provider) Start(ctx context.Context) error {
	p.setStatus(platform.StatusConnecting)

	if err := token.StartRefreshAccessToken(ctx, p.tokenSrc); err != nil {
		p.setStatus(platform.StatusDisconnected)
		return fmt.Errorf("qqbot：启动 token 刷新：%w", err)
	}
	intents := event.RegisterHandlers(event.C2CMessageEventHandler(p.onC2CMessage))
	apInfo, err := p.api.WS(ctx, nil, "")
	if err != nil {
		p.setStatus(platform.StatusDisconnected)
		return fmt.Errorf("qqbot：获取 WS 接入点：%w", err)
	}
	p.setStatus(platform.StatusConnected)

	// 启动会话管理
	go func() {
		if err := botgo.NewSessionManager().Start(apInfo, p.tokenSrc, &intents); err != nil {
			p.log.Error("qqbot：会话管理退出", "error", err)
			p.setStatus(platform.StatusDisconnected)
		}
	}()
	p.log.Info("qqbot：已连接", "app_id", p.appID, "sandbox", p.cfg.QQBot.Sandbox, "shards", apInfo.Shards)
	return nil
}

func (p *Provider) Stop(ctx context.Context) error {
	p.stopOnce.Do(func() {
		close(p.done)
		p.sendMu.Lock()
		p.closed = true
		close(p.events)
		p.sendMu.Unlock()

		p.setStatus(platform.StatusDisconnected)
		p.log.Info("qqbot：已断开")
	})
	return nil
}

func (p *Provider) Status() platform.Status {
	p.mu.RLock()
	defer p.mu.RUnlock()
	return p.status
}

func (p *Provider) setStatus(s platform.Status) {
	p.mu.Lock()
	p.status = s
	p.mu.Unlock()
}

func (p *Provider) deliver(msg platform.IncomingMessage) {
	p.sendMu.RLock()
	defer p.sendMu.RUnlock()
	if p.closed {
		return
	}
	select {
	case p.events <- msg:
	case <-p.done:
	}
}

func (p *Provider) onC2CMessage(_ *dto.WSPayload, data *dto.WSC2CMessageData) error {
	msg, err := translateC2C(p.appID, data)
	if err != nil {
		p.log.Warn("qqbot：丢弃无法解析的事件", "error", err)
		return nil
	}
	p.deliver(msg)
	return nil
}
