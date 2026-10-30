# QQ 私聊、模型与脚本工具联调

## 已接通的链路

`main → platform.Provider → chat.Service → tools.Engine → llm.Client → 脚本工具 → QQ 被动回复`。

`main` 在启动时加载 `skills/`，将工具定义和执行 handler 注册到 Registry，创建 Engine 并注入 chat.Service。模型自行决定调用工具；每次工具结果按原 call_id 回填，再请求模型，直到得到最终文本或达到步数上限。

当前 `get_location` 是 Python 脚本，固定返回 `{"location":"湖南长沙"}`；`get_weather` 是 Node 脚本，接收 location，固定返回晴天。不是实时定位或天气服务。

## 本机配置和启动

使用 Go 1.23.6，并确认 `python3 --version`、`node --version` 正常。
编辑本机 config.yaml，保留已有密钥，不覆盖成示例空值：

```yaml
bot:
  provider: qqbot
  qqbot:
    app_id: "你的 AppID"
    app_secret: "你的 AppSecret"
    sandbox: true # 必须与实际机器人环境配套
llm:
  protocol: openai-chat
  base_url: "https://你的模型服务/v1" # 适配器会追加 /chat/completions
  api_key: "你的模型密钥"
  model: "支持工具调用的模型名"
  timeout: "60s" # 整轮模型与工具循环总时间上限
tools:
  directory: skills # 相对本配置文件所在目录，也可配置绝对路径
  max_steps: 5
```

这是现有配置中的相关字段片段，其余字段参照 config.yaml.example。不要把真实密钥写进文档或提交 Git。

```bash
GOTOOLCHAIN=local go build -o bin/botd ./cmd/botd
./bin/botd -config config.yaml
```

部署时同时保留配置、skills/ 与 Python/Node 运行环境；二进制并未内嵌脚本。日志出现 `适配器就绪 ... tools=2` 及 QQ READY 后，在 QQ 私聊测试：

1. “你好”——验证普通回复。
2. “我在哪里？”——模型可调用 get_location。
3. “那里的天气怎么样？”——验证上下文与 get_weather。
4. “请先查我的位置，再查那里的天气”——验证同一轮串联两个工具。

工具调用日志包含工具名、耗时和成功状态，不打印凭据或整段脚本参数。QQ 收到模型故障时显示简短失败提示，具体原因查看日志。
前台运行按 Ctrl+C 退出。开发时不要同时启动多个同 AppID 的机器人进程。

## 可重复的验证

默认测试不访问真实 QQ/模型：

```bash
gofmt -l .
GOTOOLCHAIN=local go build ./...
GOTOOLCHAIN=local go vet ./...
GOTOOLCHAIN=local go test -race ./... -timeout 30s
python3 -m unittest discover -s skills/get_location -p 'test_*.py' -v
node --test skills/get_weather/run.test.js
```

`TestQQModelToolsConversation` 使用本地 HTTP 服务、真实两个脚本和 mock QQ，断言三次模型请求的工具定义、call_id、参数、结果与最终回复。
Service 测试覆盖历史隔离、去重、失败不进入历史、取消、中文分段与过载会话隔离。

真实模型验证需显式开启，使用本机 config.yaml，会产生服务端调用：

```bash
GOTOOLCHAIN=local go test ./internal/llm/provider/openaichat -run '^TestCompleteLive$' -count=1 -v -args -live
GOTOOLCHAIN=local go test ./cmd/botd -run '^TestLiveToolsConversation$' -count=1 -timeout 90s -v -args -live-tools
```

第二条测试不连接 QQ，但真实请求已配置的模型、执行 Python/Node，并校验两个工具都被调用且结果用于回复。

## 当前边界

- 只接入现有 QQ 单聊；群聊、流式输出、运维 API 和数据库存储仍待实现。
- 历史只保存用户文本与最终回复，工具往返保留在本轮请求中；重启丢失历史和去重记录。
- 16 个分片，每片队列 64 条；每片最多保留 64 个活跃会话，每会话保留最近 128 条消息 ID。超出容量会淘汰最久未使用的会话。
- 分片满时拒绝新的排队请求，并通过独立有界队列发送繁忙提示；提示队列也满时记录并丢弃，避免无限占用资源。共享分片的不同会话仍可能排队等待。
- 回复按 rune 分段，每次入站共用 ReplyRef，达到配置段数后截断并加省略号；发送失败不写入成功历史。
- 脚本单次最长 5 秒，父 context 可更早取消；只保证终止直接启动的解释器，本例不派生其他进程。
- 当前只在启动时加载工具，不自动监控文件变化；command 字段未实现斜杠命令路由。
- 本阶段没有自动 HTTP 重试，llm.max_attempts 尚未接入重试逻辑；模型服务错误会结束本轮并提示稍后再试。

## 2026-10-30 联调记录

- 格式、编译、vet、race 及 Python/Node 测试通过。
- 真实模型文本调用与两个工具串联调用通过。
- 本机 QQ 进程收到 READY；作者实际发来普通消息、位置查询与天气查询，服务端记录三轮成功发送，其中 Python/Node 工具均执行成功。
- 当前分支直接实现，按模型、工具、配置、业务服务、主程序接入分阶段本地提交；未推送或合并，本次启动不配置系统自动启动。
