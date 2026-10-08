# QQ-bot-Go-

**QQ 官方机器人 + AI 对话服务**（Go 版）——一个以「**学会 Go**」为首要目的的个人学习项目，
从 TypeScript 版 `napcat-platform` 改造而来。

> **现在的状态**：通信底座已跑通（能连 QQ、能收私聊、能回消息），
> 但**还没接大模型**——所以它目前是个复读机，不是 AI 机器人。
> 进度、欠账、下一步见 **[`docs/当前开发进度.md`](docs/当前开发进度.md)**。

---

## 快速开始

```bash
go mod tidy
cp config.yaml.example config.yaml    # 填 bot.qqbot.app_id / app_secret，注意 sandbox 要与 AppID 配套
go run ./cmd/botd -config config.yaml
```

不想连 QQ 就先跑假的：把 `config.yaml` 里的 `bot.provider` 改成 `mock`，
程序会把「本应发出去的消息」打印到 stdout，不联任何网络。

```bash
go run ./cmd/botd -version            # 打印版本
```

配置只有 `config.yaml` 一个来源（**不用环境变量**）；它已在 `.gitignore` 里，自检：

```bash
git check-ignore -v config.yaml
```

---

## 文档

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
  llm/                   ② 模型端口（目前只有 fake 实现，三个真实协议待写）
  config/ logx/ version/            地基：配置 / 日志 / 版本号
docs/                    设计文档 + 进度文档
learn/                   本地学习笔记（不入库）
```

---

## 技术栈与约定

Go 1.23 · [botgo](https://github.com/tencent-connect/botgo) v0.2.1（QQ 官方 SDK）·
标准库 `log/slog` · `gopkg.in/yaml.v3` · `gopkg.in/natefinch/lumberjack.v2`

**依赖白名单**：除上面这几个之外一律用标准库（不引 gin/echo、不用 ORM、不用 logrus/zap）。
完整约定见 [`AGENTS.md`](AGENTS.md) §5。

自查一下手上的代码是不是好的：

```bash
gofmt -l . && go build ./... && go vet ./... && go test ./... -race
```
