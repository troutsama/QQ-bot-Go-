# QQ-bot-Go-

**QQ 官方机器人 + AI 对话服务**（Go 版）——一个以「**学会 Go**」为首要目的的个人学习项目，
从 TypeScript 版 `napcat-platform` 改造而来。

> **现在的状态**：通信底座已跑通（能连 QQ、能收私聊、能回消息），
> 但**还没接大模型**——所以它目前是个复读机，不是 AI 机器人。
> 进度、欠账、下一步见 **[`docs/当前开发进度.md`](docs/当前开发进度.md)**。

QQ 渠道已支持收发消息入库：接收回调先保存再投递事件，`Send` 成功后保存发送记录；临时 `echoLoop` 仍只负责生成回显。

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

## QQ 渠道消息入库

数据库在 `cmd/botd/main.go` 的 `run` 中创建一次，经平台工厂注入 QQ Provider。消息记录保存在独立的 `channel_messages` 表，不要求创建用户、会话或问答轮次，不写原有 `turns/messages` 业务表。以后替换 `echoLoop` 不影响渠道落库。

```text
QQ 接收回调 → 转换 IncomingMessage → 保存入站 → Events() → 业务处理
业务调用 Send → QQ API 返回成功 → 保存出站 → 返回发送回执
```

- 入站按 provider、app、场景、会话、方向、平台消息 ID 去重，重复消息不再次投递。其他 SQL 错误返回给 SDK 回调并记日志，不投递未保存的事件。
- 发送失败不写出站成功记录；主动消息和同一入站的多段回复均可保存。回复记录包含被回复消息 ID、事件 ID 和发送序号。
- QQ 已成功但数据库保存失败时，`Send` 返回回执和 `platform.ErrSendUnrecorded`，用 `errors.Is` 判断。此时消息已经发出，不能自动重发；回显循环会明确记录该错误。
- 已完成发送的保存不受原调用 context 取消影响，但有独立的 5 秒超时。
- 空文本和附件事件也保存入站记录。当前保存标准化正文、发送者、附件标记和 Unix 毫秒时间，不归档原始 SDK 载荷、附件 URL 或二进制。`typing` 通知也以相应类型记录。
- mock 保持原有内存行为，不模拟 QQ 渠道落库；离线落库测试使用临时 SQLite 和 QQ API 替身，无需真实密钥。

`Store.Open` 会为新库或已有库自动补建 `channel_messages`，可重复启动。`database.init_schema` 仍只控制原有七张业务表：仅新库首次需要它们时设为 `true`，之后改回 `false`；旧建表脚本不可重复执行。没有改动已有业务表或自动删除旧数据。

可在 SQLite 客户端中查询收发记录：

```sql
SELECT direction, msg_id, reply_to_msg_id, reply_seq, content, occurred_at
FROM channel_messages
ORDER BY id;
```

当前是同步记录和内存投递，尚未实现可靠队列。如果在入站保存后、业务消费前退出，或 QQ 成功后、记录出站前崩溃，可能留下处理缺口；本轮不会自动补发或恢复。回调返回错误也不代表 SDK 必然重投，需要结合日志排查。

定向验证：

```bash
GOTOOLCHAIN=go1.23.6 go test -race ./internal/store ./internal/platform/provider/qqbot -run TestChannel -count=1
```

全量测试仍有已有失败：`internal/store/TestStore` 在 `Open(InitSchema: true)` 后再次建表，报 `table users already exists`，后续还使用旧的 `test_table`。本次未修改或删除该测试。

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

Go 1.23.6 · [botgo](https://github.com/tencent-connect/botgo) v0.2.1（QQ 官方 SDK）· SQLite（`modernc.org/sqlite` v1.38.2）·
标准库 `log/slog` · `gopkg.in/yaml.v3` · `gopkg.in/natefinch/lumberjack.v2`

**依赖白名单**：除上面这几个之外一律用标准库（不引 gin/echo、不用 ORM、不用 logrus/zap）。
完整约定见 [`AGENTS.md`](AGENTS.md) §5。

自查一下手上的代码是不是好的：

```bash
gofmt -l . && go build ./... && go vet ./... && go test ./... -race
```
