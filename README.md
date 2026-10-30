# QQ-bot-Go-

**QQ 官方机器人 + AI 对话服务**（Go 版）——一个以「**学会 Go**」为首要目的的个人学习项目，
从 TypeScript 版 `napcat-platform` 改造而来。

> **现在的状态**：已接通 QQ 私聊 → OpenAI Chat 模型 → Python 位置 / Node 天气工具 → QQ 回复。
> 支持有界内存历史、同会话串行化、去重和中文分段；数据库持久化仍待完成。
> 进度、欠账、下一步见 **[`docs/当前开发进度.md`](docs/当前开发进度.md)**。

---

## 快速开始

开发环境基准为 **Go 1.23.6**，`go.mod` 保持 `go 1.23.6`。直接依赖和间接依赖都必须兼容该版本；升级依赖时不能连带提高 Go 版本要求。升级 Go 工具链须先由作者明确同意，并同步更新本文与 `AGENTS.md`。

先确认本机版本，再使用本机工具链整理依赖，避免自动切换到更高版本掩盖兼容问题：

```bash
go version                         # 本项目基准：go1.23.6
GOTOOLCHAIN=local go mod tidy -go=1.23.6
cp config.yaml.example config.yaml    # 填 QQ 凭据及模型 api_key/model/base_url；不要覆盖已有配置
go run ./cmd/botd -config config.yaml
```

本机需要 `python3`、`node`。部署时将 `skills/` 与配置文件、二进制一起保留；`tools.directory` 相对配置文件定位。
在 QQ 私聊发送“我在哪里，那里的天气怎么样？”，模型可依次调用两个工具。它们固定返回湖南长沙和晴天，属于演示数据。

不连接 QQ 时，将 `bot.provider` 改成 `mock`；完全离线还需将 `llm.protocol` 改成 `fake`。
mock 的消息通过测试注入，不会自动读取终端输入。普通 `fake` 只返回固定文本，不会主动发起工具调用。
完整配置、运行、离线测试与真实模型工具验收见 [QQ 对话与工具联调](docs/QQ对话与工具联调.md)。

```bash
go run ./cmd/botd -version            # 打印版本
```

配置只有 `config.yaml` 一个来源（**不用环境变量**）；它已在 `.gitignore` 里，自检：

```bash
git check-ignore -v config.yaml
```

---

## 文档

VS Code 断点与启动配置见 [调试指南](docs/调试指南.md)。按 F5 可选择模型调用单测或主程序启动流程。

| 想看什么 | 去哪 |
|---------|------|
| **第一次接触这个项目** | [`docs/当前开发进度.md`](docs/当前开发进度.md) |
| 全部文档导航 | [`docs/README.md`](docs/README.md) |
| 设计文档集（架构 / 接口 / 表结构 / 风险 / 实施计划） | [`docs/design/README.md`](docs/design/README.md) |
| 已落地代码的逐文件讲解 + 陷阱 + 测试清单 | `learn/01-地基与通信底座.md`（本地，已 gitignore） |
| 给 AI agent 的项目约定与速查表 | [`AGENTS.md`](AGENTS.md) |

---

## 目录结构

```text
cmd/botd/                组合根：全项目唯一 new 对象的地方
internal/
  platform/              ① 通信底座（端口 + 适配器）
    scene.go port.go errors.go        Provider 接口、出入站类型、会话身份、哨兵错误
    factory/                          按配置选 mock / qqbot（唯一 switch）
    provider/mock/                    假平台（单测 + 本地调试用）
    provider/qqbot/                   真 QQ：连接 / 收单聊 / 发单聊 / 接管 SDK 日志
  llm/                   ② 模型端口（fake、openai-chat、脚本工具加载与多步调用）
  chat/                  核心服务：分片队列、内存历史、去重与分段回复
  config/ logx/ version/            地基：配置 / 日志 / 版本号
docs/                    设计文档 + 进度文档
learn/                   本地学习笔记（不入库）
skills/                  Python 位置与 Node 天气演示工具（含元数据和单测）
```

---

## 技术栈与约定

Go 1.23.6 · [botgo](https://github.com/tencent-connect/botgo) v0.2.1（QQ 官方 SDK）·
`modernc.org/sqlite` v1.38.2（纯 Go SQLite）·
标准库 `log/slog` · `gopkg.in/yaml.v3` · `gopkg.in/natefinch/lumberjack.v2`

**依赖白名单**：除上面这几个之外一律用标准库（不引 gin/echo、不用 ORM、不用 logrus/zap）。
完整约定见 [`AGENTS.md`](AGENTS.md) §3。

自查一下手上的代码是不是好的：

```bash
export GOTOOLCHAIN=local
gofmt -l . && go build ./... && go vet ./... && go test ./... -race
```
