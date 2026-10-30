package chat

import (
	"context"
	"fmt"
	"hash/fnv"
	"log/slog"
	"strings"
	"sync"
	"time"

	"qq-bot/internal/config"
	"qq-bot/internal/llm"
	"qq-bot/internal/platform"
)

// 接口定义在消费方：聊天只需要收发消息及运行一次完整模型/工具循环。
type Transport interface {
	Events() <-chan platform.IncomingMessage
	Send(context.Context, platform.OutgoingMessage) (platform.SentMessage, error)
}

type Runner interface {
	Run(context.Context, llm.Request) (llm.Response, error)
}

type Service struct {
	transport Transport
	runner    Runner
	llmCfg    config.LLMConfig
	chatCfg   config.ChatConfig
	timeout   time.Duration
	log       *slog.Logger
}

func New(transport Transport, runner Runner, llmCfg config.LLMConfig, chatCfg config.ChatConfig, log *slog.Logger) (*Service, error) {
	if transport == nil || runner == nil || log == nil {
		return nil, fmt.Errorf("chat: transport、runner 和 logger 必填")
	}
	timeout, err := time.ParseDuration(llmCfg.Timeout)
	if err != nil {
		return nil, fmt.Errorf("chat: 解析模型超时: %w", err)
	}
	if timeout <= 0 || chatCfg.ContextTurns < 1 || chatCfg.SegmentMaxChars < 1 || chatCfg.MaxReplySegments < 1 || chatCfg.MaxReplySegments > 4 {
		return nil, fmt.Errorf("chat: 超时、历史轮数、分段长度必须为正，回复段数必须在 1–4 之间")
	}
	return &Service{transport: transport, runner: runner, llmCfg: llmCfg, chatCfg: chatCfg, timeout: timeout, log: log}, nil
}

// Run 由一个分发协程将同一会话放进同一个队列，每个队列仅有一个消费者。
// 队列和会话状态属于本次 Run；正常运行期间无需跨 worker 共享 map 或加锁。
func (s *Service) Run(ctx context.Context) error {
	const shardCount = 16
	queues := make([]chan platform.IncomingMessage, shardCount)
	busy := make(chan platform.IncomingMessage, 64)
	var wg sync.WaitGroup
	// 过载提示独立发送，避免某个分片排满后卡住整个入站分发器。
	wg.Add(1)
	go func() {
		defer wg.Done()
		for msg := range busy {
			if ctx.Err() != nil {
				return
			}
			sendCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
			_, err := s.transport.Send(sendCtx, platform.OutgoingMessage{
				Conv: msg.Conv, Reply: platform.NewReplyRef(msg.MsgID, msg.EventID), Kind: platform.OutText, Text: "消息较多，请稍后再试。",
			})
			cancel()
			if err != nil {
				s.log.Warn("发送过载提示失败", "error", err)
			}
		}
	}()
	for i := range queues {
		queues[i] = make(chan platform.IncomingMessage, 64)
		wg.Add(1)
		go func(queue <-chan platform.IncomingMessage) {
			defer wg.Done()
			s.work(ctx, queue)
		}(queues[i])
	}
	defer func() {
		close(busy)
		for _, queue := range queues {
			close(queue)
		}
		wg.Wait()
	}()
	for {
		select {
		case <-ctx.Done():
			return nil
		case msg, ok := <-s.transport.Events():
			if !ok {
				return nil
			}
			if msg.Conv.Scene != platform.SceneC2C || strings.TrimSpace(msg.Text) == "" {
				continue
			}
			hash := fnv.New32a()
			_, _ = hash.Write([]byte(msg.Conv.String()))
			queue := queues[hash.Sum32()%shardCount]
			select {
			case queue <- msg:
			case <-ctx.Done():
				return nil
			default:
				s.log.Warn("会话分片队列已满", "conv", msg.Conv.String())
				select {
				case busy <- msg:
				default:
					s.log.Warn("过载提示队列已满，丢弃消息", "msg_id", msg.MsgID)
				}
			}
		}
	}
}

type session struct {
	history    []Turn
	seen       map[string]struct{}
	messageIDs []string
	lastUsed   time.Time
}

func (s *Service) work(ctx context.Context, queue <-chan platform.IncomingMessage) {
	// 内存状态有界。会话被淘汰或进程重启后，历史及去重状态会丢失。
	const maxSessions = 64
	const maxSeen = 128
	sessions := make(map[platform.ConversationKey]*session)
	for msg := range queue {
		if ctx.Err() != nil {
			return
		}
		state := sessions[msg.Conv]
		if state == nil {
			if len(sessions) >= maxSessions {
				var oldest platform.ConversationKey
				var oldestAt time.Time
				for key, candidate := range sessions {
					if oldestAt.IsZero() || candidate.lastUsed.Before(oldestAt) {
						oldest, oldestAt = key, candidate.lastUsed
					}
				}
				delete(sessions, oldest)
			}
			state = &session{seen: make(map[string]struct{})}
			sessions[msg.Conv] = state
		}
		state.lastUsed = time.Now()
		if msg.MsgID != "" {
			if _, exists := state.seen[msg.MsgID]; exists {
				continue
			}
			if len(state.messageIDs) >= maxSeen {
				delete(state.seen, state.messageIDs[0])
				state.messageIDs = state.messageIDs[1:]
			}
			state.seen[msg.MsgID] = struct{}{}
			state.messageIDs = append(state.messageIDs, msg.MsgID)
		}
		s.respond(ctx, msg, state)
	}
}

func (s *Service) respond(ctx context.Context, msg platform.IncomingMessage, state *session) {
	s.log.Info("收到消息", "conv", msg.Conv.String(), "msg_id", msg.MsgID)
	req := BuildRequest(s.llmCfg, state.history, msg.Text, nil)
	modelCtx, cancel := context.WithTimeout(ctx, s.timeout)
	resp, err := s.runner.Run(modelCtx, req)
	cancel()
	if ctx.Err() != nil {
		return
	}
	text := strings.TrimSpace(resp.Text)
	success := err == nil && text != ""
	if !success {
		s.log.Warn("模型回复失败", "msg_id", msg.MsgID, "error", err, "empty_text", text == "")
		text = "暂时无法生成回复，请稍后再试。"
	}
	segments := splitReply(text, s.chatCfg.SegmentMaxChars, s.chatCfg.MaxReplySegments)
	reply := platform.NewReplyRef(msg.MsgID, msg.EventID)
	for _, segment := range segments {
		sendCtx, stop := context.WithTimeout(ctx, 10*time.Second)
		_, sendErr := s.transport.Send(sendCtx, platform.OutgoingMessage{Conv: msg.Conv, Reply: reply, Kind: platform.OutText, Text: segment})
		stop()
		if sendErr != nil {
			s.log.Error("发送 QQ 回复失败", "msg_id", msg.MsgID, "error", sendErr)
			return
		}
	}
	if success {
		// 只把用户实际收到的最终文本放入跨轮历史，工具往返由 Engine 在本轮保留。
		state.history = append(state.history, Turn{Status: StatusOK, UserText: msg.Text, ReplyText: strings.Join(segments, "")})
		if len(state.history) > s.chatCfg.ContextTurns {
			state.history = append([]Turn(nil), state.history[len(state.history)-s.chatCfg.ContextTurns:]...)
		}
	}
	s.log.Info("回复完成", "msg_id", msg.MsgID, "segments", len(segments), "success", success)
}

func splitReply(text string, maxChars, maxSegments int) []string {
	runes := []rune(text)
	var result []string
	for len(runes) > 0 && len(result) < maxSegments {
		n := maxChars
		if len(runes) < n {
			n = len(runes)
		}
		part := string(runes[:n])
		if len(result) == maxSegments-1 && len(runes) > n {
			part = string(runes[:n-1]) + "…"
		}
		result = append(result, part)
		runes = runes[n:]
	}
	return result
}
