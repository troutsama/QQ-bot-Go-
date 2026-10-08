package fake

import (
	"context"
	"qq-bot/internal/llm"
	"sync"
)

var _ llm.Client = (*Client)(nil)

// Client 假模型。
type Client struct {
	mu    sync.Mutex
	calls []llm.Request // 记录每次请求 —— 这是 fake 最大的价值
	reply string
	err   error
}

func New() *Client {
	return &Client{reply: "（fake 模型的固定回复）"}
}

func (c *Client) Name() string { return "fake" }

func (c *Client) Complete(ctx context.Context, req llm.Request) (llm.Response, error) {
	c.mu.Lock()
	c.calls = append(c.calls, req)
	reply, err := c.reply, c.err
	c.mu.Unlock()

	if err != nil {
		return llm.Response{}, err
	}
	return llm.Response{
		Text:       reply,
		StopReason: llm.StopEnd,
		Usage:      llm.Usage{InputTokens: 1, OutputTokens: 1},
	}, nil
}

// Calls 返回收到的请求快照。
//
// 为什么这个方法是 fake 的核心（06 §10.2）：上下文拼接是 V1 最容易出错的地方
// （轮次裁剪、时间前缀、命令污染）。有了它，单测就能断言
// **「模型到底看到了什么」** —— 这是「可测试性」的具象化。
func (c *Client) Calls() []llm.Request {
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([]llm.Request(nil), c.calls...)
}

func (c *Client) SetReply(s string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.reply = s
}

func (c *Client) SetError(err error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.err = err
}
