package openaichat

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"qq-bot/internal/llm"
	"strings"
	"time"
)

type Client struct {
	endpoint string
	apikey   string
	http     *http.Client
}

func New(baseURL, apiKey string, httpClient *http.Client) (*Client, error) {
	baseURL = strings.TrimRight(strings.TrimSpace(baseURL), "/")

	u, err := url.Parse(baseURL)
	if err != nil {
		return nil, fmt.Errorf("openaichat：解析baseURL：%w", err)
	}
	if (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		return nil, fmt.Errorf("openaichat：baseURL 必须为HTTP(S)绝对地址")
	}
	if u.RawQuery != "" || u.Fragment != "" || u.User != nil {
		return nil, fmt.Errorf("openaichat: baseURL 不支持查询参数、片段或用户信息")
	}
	if strings.TrimSpace(apiKey) == "" {
		return nil, fmt.Errorf("openaichat: API key 不能为空")
	}
	if httpClient == nil {
		httpClient = &http.Client{Timeout: 60 * time.Second}
	}

	return &Client{
		endpoint: baseURL + "/chat/completions",
		apikey:   apiKey,
		http:     httpClient,
	}, nil
}

func (c *Client) Name() string {
	return "openai-chat"
}

func tochatRequest(req llm.Request) chatRequest {
	out := chatRequest{
		Model: req.Model,
		// Temperature: req.Temperature,
		MaxTokens: req.MaxTokens,
	}
	if req.System != "" {
		out.Messages = append(out.Messages, chatMessage{
			Role:    "system",
			Content: &req.System,
		})
	}
	for _, msg := range req.Messages {
		if msg.Role == llm.RoleTool {
			for _, result := range msg.ToolResults {
				text := result.Output
				out.Messages = append(out.Messages, chatMessage{
					Role:       "tool",
					Content:    &text,
					ToolCallID: result.CallID,
				})
			}
			continue
		}

		text := msg.Text
		m := chatMessage{
			Role:    string(msg.Role),
			Content: &text,
		}
		if text == "" && len(msg.ToolCalls) > 0 {
			m.Content = nil
		}
		for _, call := range msg.ToolCalls {
			m.ToolCalls = append(m.ToolCalls, chatToolCall{
				ID:   call.ID,
				Type: "function",
				Function: chatCallFunction{
					Name:      call.Name,
					Arguments: call.Arguments,
				},
			})
		}
		out.Messages = append(out.Messages, m)
	}
	for _, tool := range req.Tools {
		out.Tools = append(out.Tools, chatTool{
			Type: "function",
			Function: chatToolFunction{
				Name:        tool.Name,
				Description: tool.Description,
				Parameters:  tool.InputSchema,
			},
		})
	}
	return out
}

func (c *Client) Complete(ctx context.Context, req llm.Request) (llm.Response, error) {
	data, err := json.Marshal(tochatRequest(req))
	if err != nil {
		return llm.Response{}, fmt.Errorf("openaichat：编码请求：%w", err)
	}

	httpReq, err := http.NewRequestWithContext(
		ctx, http.MethodPost, c.endpoint, bytes.NewReader(data),
	)
	if err != nil {
		return llm.Response{}, fmt.Errorf("openaichat: 创建请求: %w", err)
	}

	httpReq.Header.Set("Authorization", "Bearer "+c.apikey)
	httpReq.Header.Set("Content-Type", "application/json")

	resp, err := c.http.Do(httpReq)
	if err != nil {
		// 保留取消或超时原因，交给上层决定是否重试
		if ctx.Err() != nil {
			return llm.Response{}, fmt.Errorf("openaichat: 请求取消: %w", ctx.Err())
		}
		return llm.Response{}, fmt.Errorf(
			"openaichat: 发送请求: %w: %w", llm.ErrRetryable, err,
		)
	}
	defer resp.Body.Close()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		switch {
		case resp.StatusCode == 401 || resp.StatusCode == 403:
			return llm.Response{}, fmt.Errorf("openaichat: HTTP %d: %w", resp.StatusCode, llm.ErrAuth)
		case resp.StatusCode == 429:
			return llm.Response{}, fmt.Errorf("openaichat: HTTP 429: %w", llm.ErrRateLimited)
		case resp.StatusCode >= 500:
			return llm.Response{}, fmt.Errorf("openaichat: HTTP %d: %w", resp.StatusCode, llm.ErrRetryable)
		default:
			return llm.Response{}, fmt.Errorf("openaichat: HTTP %d", resp.StatusCode)
		}
	}

	var body chatResponse
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		return llm.Response{}, fmt.Errorf("openaichat: 解码响应: %w", err)
	}
	if body.Error != nil {
		return llm.Response{}, fmt.Errorf("openaichat: 响应包含错误对象")
	}
	if len(body.Choices) == 0 {
		return llm.Response{}, fmt.Errorf("openaichat: choices 为空: %w", llm.ErrRetryable)
	}
	return toResponse(body), nil
}

func toResponse(body chatResponse) llm.Response {
	choice := body.Choices[0]
	out := llm.Response{
		StopReason: llm.StopOther,
		Usage: llm.Usage{
			InputTokens:  body.Usage.PromptTokens,
			OutputTokens: body.Usage.CompletionTokens,
		},
	}
	if choice.Message.Content != nil {
		out.Text = *choice.Message.Content
	}
	for _, call := range choice.Message.ToolCalls {
		out.ToolCalls = append(out.ToolCalls, llm.ToolCall{
			ID:        call.ID,
			Name:      call.Function.Name,
			Arguments: call.Function.Arguments,
		})
	}
	switch choice.FinishReason {
	case "stop":
		out.StopReason = llm.StopEnd
	case "length":
		out.StopReason = llm.StopMaxTokens
	case "tool_calls":
		out.StopReason = llm.StopToolUse
	}
	return out
}

var _ llm.Client = (*Client)(nil)
