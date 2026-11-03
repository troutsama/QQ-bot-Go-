# QQ-bot-Go-

一个三人协作的 Go 学习项目：QQ 官方机器人 + AI 对话服务。当前能接收 QQ 单聊并回显文本；大模型对话和消息入库尚未接通。

**第一次参与，请从下面的“从哪里开始”读起。当前练习是亲手把收发消息写进数据库，直接写 SQL，不需要先设计新架构。**

## 先知道我们在做什么

用户给机器人发送“你好”，目前程序回复“你说的是：你好”。这次任务是让程序同时留下可查询的记录：谁发来的、属于哪个会话、原文是什么、回复是什么、处理是否成功。

数据库连接和建表基础是学长准备的；消息入库是特意留给参与者的练习。你需要完成的是把已有零件连起来，并亲手写 SQL，不用重写数据库底层，也不用先封装 Repository、Store 接口或完整的聊天服务。

你可能同时遇到三类陌生内容：Go 的写法、代码之间怎么调用、业务数据怎么对应数据库。先逐个定位，不要求一次看懂整个项目。暂时说不出“哪里不会”时，就从“这一行的变量从哪里来、接下来传给谁”开始。

### 当前代码与未来设计的区别

| 部分 | 当前实际状态 | 这次是否需要理解 |
|---|---|---|
| `cmd/botd/main.go` | 读取配置、创建日志与适配器、连接平台；`echoLoop` 收消息并回显 | 是，主要入口 |
| `internal/platform/` | 收发消息的公共类型与接口；有 QQ 和 mock 实现 | 先看类型和接口 |
| `internal/store/store.go` | 已有 SQLite 连接、可选建表、`DB()` 与 `Close()`；没有消息读写方法 | 是，复用即可 |
| `internal/store/migrations/0001_init.sql` | 已定义 7 张表；文件名叫 migration，但尚无版本迁移执行器 | 先看本任务用到的表 |
| `internal/chat/types.go` | 只有业务数据类型，尚无 `chat.Service` 或业务调度实现 | 按需查字段和状态 |
| `internal/llm/` | 公共接口和 fake；真实协议适配器未实现，回显流程未调用模型 | 本次跳过 |
| `internal/config/`、`internal/logx/` | 配置读取校验、运行日志 | 只看本任务涉及的配置 |
| Admin HTTP、历史上下文、分片队列 | 设计中的目标能力，当前未实现 | 本次跳过 |

`docs/design/` 是目标设计，不是“每个功能都已经写好”的说明。遇到文档与代码不一致，先确认当前代码；找不到规划里的文件，不代表你漏看了。

## 从哪里开始：按一条消息读代码

先只打开下面三个文件。每次读到能回答右侧问题，就进入下一步，不必逐行研究所有实现。

| 顺序 | 打开哪里 | 只看什么 | 读完能回答什么 |
|---|---|---|---|
| 1 | [`cmd/botd/main.go`](cmd/botd/main.go) | `echoLoop` | 哪里收到消息？哪里生成回复？哪里知道发送成功或失败？ |
| 2 | [`internal/platform/port.go`](internal/platform/port.go) | `IncomingMessage`、`OutgoingMessage`、`Provider.Events/Send` | 原文、消息 ID、会话、发送结果分别在哪个字段？ |
| 3 | [`internal/platform/scene.go`](internal/platform/scene.go) | `ConversationKey`、`Sender` | 会话身份和发送者身份分别是什么？ |

当前消息流：

```text
QQ 回调 → qqbot 转成 IncomingMessage → Events() 通道
                                           ↓
main.go 的 echoLoop：取出 msg → 打印日志 → 跳过空文本
                                           ↓
                         构造回复 → p.Send → 成功结果 / error
```

接下来再看：

1. 回到 `main.go` 的 `run`：确认 `cfg`、`ctx`、`provider` 如何创建，`echoLoop` 怎样接收参数。
2. 看 [`internal/store/store.go`](internal/store/store.go) 的 `Open`、`DB`、`Close`：分别是打开、取得 SQL 操作对象、释放连接资源。
3. 看 [`internal/config/config.go`](internal/config/config.go) 的 `DatabaseConfig`：知道 `cfg.Database` 来自哪里。
4. 看 [`0001_init.sql`](internal/store/migrations/0001_init.sql) 的 `users`、`conversations`、`turns`、`messages`，再按需对照 [`chat/types.go`](internal/chat/types.go)。

QQ SDK、日志轮转、模型协议这次可以先当作已提供的工具使用。想追问“QQ 消息怎样变成 msg”，再读 [`inbound.go`](internal/platform/provider/qqbot/inbound.go) 的 `translateC2C`。

### 几个词先翻译成具体动作

| 词 | 在本任务里的含义 |
|---|---|
| 初始化 | 调 `store.Open(cfg.Database)`，得到可用的连接对象；不等于已经保存消息 |
| 依赖注入 | `run` 创建数据库对象，再把它作为参数传给 `echoLoop`；跟传 `log` 是同一种动作 |
| `*sql.DB` | Go 提供的数据库操作对象，管理连接，可执行 SQL；通过已有的 `s.DB()` 取得 |
| `context` / `ctx` | 调用携带的取消信号；写库使用 `ExecContext` / `QueryRowContext` 等，让调用能响应取消 |
| `Events()` / channel | 消息进入程序的队列；`msg := <-...` 取出一条消息，这次不需要重写队列 |
| 外键 | 子记录需要指向存在的父记录，所以必须先取得会话 ID，再创建轮次和消息 |
| 事务 | 几条相关 SQL 一起成功或一起撤销，避免留下半条业务记录 |
| 幂等 | 同一个平台消息重复送到时，只处理一次，不能多记一轮、重复回复 |

## 当前练习：收发消息入库的小步路线

**练习约定：直接在 `main.go` 的消息处理路径写 SQL，通过参数传入 `*sql.DB`，沿用现有表结构。暂不新增仓储封装、接口或聊天服务。** 这是本阶段的教学安排；后续理解流程后再讨论整理代码。

默认先完成“单聊、非空文本”的闭环。空文本/附件仍沿用现有跳过逻辑；全量原始事件归档、群聊、LLM 请求快照和日志入库不属于本轮必做内容。`main.go` 中“注入到日志系统”的 TODO 是另一项工作，不是消息入库的前置条件。

### 0. 先画出要保存的东西（暂不写代码）

- **在哪里看**：`echoLoop`、`IncomingMessage` 和建表 SQL。
- **做什么**：用“用户发你好，机器人回复你说的是：你好”写出一组数据。圈出已有字段、不知道从哪来的字段。
- **为什么先做**：不知道要存什么时，直接写 INSERT 很容易把平台 ID 和数据库 ID 混在一起。
- **完成标志**：能指着 `msg.Text`、`msg.MsgID`、`msg.Conv`、`msg.Sender` 说明它们准备存到哪里。

### 1. 在启动时打开数据库，并传给消息循环

- **在哪里做**：`main.go` 的 `run`，现有数据库初始化 TODO 附近；以及 `echoLoop` 的参数和调用处。
- **做什么**：调用已有 `store.Open(cfg.Database)`，检查错误，安排关闭，把 `s.DB()` 传给 `echoLoop`。新增参数后同步调整调用处。
- **为什么这样做**：连接对象在进程启动时创建并复用，每条消息不必重新打开数据库；参数让人一眼看到这个函数需要哪些资源。
- **完成标志**：`go build ./...` 通过；启动能打开数据库，初始化失败能返回错误，退出能释放资源。此时还没有消息记录是正常的。

只需理解这个形状，参数顺序可自行选择（这是待实现的签名示意，不是已有代码）：

```go
func echoLoop(ctx context.Context, p platform.Provider, log *slog.Logger, db *sql.DB) error
```

### 2. 先写一条最小 SQL，建立用户和会话记录

- **在哪里做**：`echoLoop` 取到非空文本消息之后；对照 SQL 中 `users`、`conversations` 的字段与唯一约束。
- **做什么**：先用参数化 SQL 创建或复用用户、会话，再查出各自数据库主键。先只写通一张表，再扩展另一张。
- **为什么这样做**：`turns` 需要数据库的会话 ID，用户 ID 也应来自 `users`；QQ 的字符串 ID 不能直接当数据库数字主键。
- **完成标志**：同一用户连发两条消息仍只有一个用户、一个会话；换用户/会话后能正确区分。

SQL 值使用 `?` 占位符和参数，不拼接用户文本。复用记录时按唯一键查 ID，不用“忽略重复插入后直接取 LastInsertId”的结果当作已有记录 ID。

### 3. 保存收到的消息：创建轮次和 user 片段

- **在哪里做**：步骤 2 后、`p.Send` 前；对照 `turns` 与 `messages`。
- **做什么**：创建 `processing` 轮次，记录入站消息 ID、用户原文等；取得新轮次 ID，再写 `role=user` 的消息片段。将相关写入放进事务，失败回滚。
- **为什么这样做**：先记下收到的消息，即使回复失败也能查到原文；事务防止只有轮次而没有正文。
- **完成标志**：一条消息对应一个轮次和一个 user 片段，外键关联正确。重复的 `(conversation_id, inbound_msg_id)` 不再产生新记录或再次回复；其他 SQL 错误不能被当作重复消息忽略。

### 4. 发送回复，再保存发送结果

- **在哪里做**：当前构造回复文本和 `p.Send` 的附近。
- **做什么**：用同一份回复文本发送和落库。发送成功后，在事务中写 `assistant` 片段、更新 `turns.reply_text`、状态和完成时间；发送失败则将轮次标为 `failed`，保留已收到的消息并记录错误。
- **为什么这样做**：准备发送不代表发送成功，不能先把回复记作已成功。数据库事务也不能与外部 QQ API 自动变成同一个事务。
- **完成标志**：正常回显后，轮次为 `ok`，user/assistant 顺序正确；发送失败时不留下“发送成功”的 assistant 记录。失败状态写库也要检查错误。

先提交收消息的事务，再调用 QQ；不要在等待网络发送时一直占着数据库写事务。若 QQ 已发送成功但数据库更新失败，应明确记录并暴露错误，不能为了补库自动重发；崩溃恢复和可靠重试留作后续讨论。

### 5. 用离线测试证明链路能跑通

- **在哪里做**：后续实现时可在 `cmd/botd/main_test.go` 写业务测试；mock 辅助方法见 [`provider/mock/provider.go`](internal/platform/provider/mock/provider.go)。
- **做什么**：用临时 SQLite 文件初始化一次，创建 mock，运行消息循环，用 `Inject` 投入消息，查询表中记录并通过 `Sent()` 检查回复，最后取消 context、等待循环退出并关闭资源。
- **为什么这样做**：不用真实 QQ 和密钥，也能反复检查 SQL、外键、状态和收发顺序。不要只凭日志“看起来没报错”判断入库成功。
- **完成标志**：覆盖正常中文消息、重复入站、两条不同消息、发送失败和 SQL 失败。发送失败可用测试用的小型 Provider 替身模拟，普通 mock 默认发送成功。

`mock.EnsureEvent` 的默认 `MsgID` 固定为 `mock-msg-1`；模拟两条不同消息时必须自己换 ID。用完成信号或有超时的等待协调测试，避免靠固定 sleep 猜测处理完成。

### 为什么按这个顺序拆

```text
知道要存什么 → 拿到数据库连接 → 拿到父记录 ID
             → 保存入站 → 发送并记录结果 → 用测试验证整条链
```

这对应实际的数据依赖：连接没传到函数就不能执行 SQL，没有会话 ID 就不能正确创建轮次，没有轮次 ID 就不能写消息片段，没拿到发送结果就不能确定回复状态。每次只新增一个可观察的结果，出错时能定位到刚完成的那一步。

### 字段对应速查（写 SQL 时再查）

| 数据库字段 | 来源 / 本练习建议 |
|---|---|
| `users.app_id / openid / nickname` | `msg.Conv.AppID` / `msg.Sender.OpenID` / `msg.Sender.Nickname`；头像当前没有来源，使用默认空值 |
| `conversations.app_id / scene / scene_id` | `msg.Conv.AppID` / `msg.Conv.Scene` / `msg.Conv.SceneID`，三者共同标识会话 |
| `turns.conversation_id / user_id` | 先写入或查询父表，取得数据库主键 |
| `turns.inbound_msg_id / user_text` | `msg.MsgID` / `msg.Text` |
| `turns.status` | 开始 `processing`，成功 `ok`，失败 `failed`；常量见 `chat/types.go` |
| `turns.trace_id` | 本轮排错关联标识；练习可先由会话标识和入站消息 ID 组合，贯穿同一轮日志 |
| `turns.llm_request_id` | 尚未调用模型，留 SQL NULL；不要伪造 LLM 请求记录 |
| `messages.turn_id / role / content` | 新轮次 ID；入站用 `user` 与原文，成功出站用 `assistant` 与回复正文 |
| `messages.seq_in_turn` | 本练习约定 user=1、assistant=2；序号用于轮内顺序 |
| 时间列 | INTEGER，使用 Unix 毫秒；接收时间可取 `msg.Timestamp.UnixMilli()`，处理/完成时间取当前时间 |

`turns.user_text/reply_text` 是轮次摘要，`messages` 是片段明细，现有设计两处都保留。`IncomingMessage.Raw` 不写进正文。`SentMessage.MsgID` 是出站平台 ID，现有 `messages` 表没有对应列；本轮不擅自改表，可先写入结构化日志，需要长期保存时再讨论表设计。

## 第一次跑起来

在仓库根目录操作，Go 版本以 [`go.mod`](go.mod) 为准（当前为 `1.26.0`）。先执行 `go version`，再编译：

```bash
go build ./...
```

**已有 `config.yaml` 时不要覆盖它。** 新建本地配置时，可复制 `config.yaml.example`，但当前示例的 `database.driver` 行中文说明前漏了 `#`，需在本地副本改成下面的合法写法。该问题是已有基线问题，不是你消息入库写错了。

本地离线练习可以直接使用下面这份最小 `config.yaml`（无真实密钥）：

```yaml
server:
  admin_token: local-dev-token
bot:
  provider: mock
llm:
  protocol: fake
log:
  level: info
  file: data/logs/botd.log
database:
  driver: sqlite # 当前只实现 SQLite
  path: data/practice.db
  max_open_conns: 1
  max_idle_conns: 1
  busy_timeout_ms: 5000
  init_schema: false
```

```bash
go run ./cmd/botd -config config.yaml
```

看到“适配器就绪”且程序等待消息属于正常现象，Ctrl+C 退出。**mock 不读取终端输入，不自动生成消息；当前没有可用的 Admin 注入接口。** 离线触发消息使用步骤 5 的测试调用 `Inject`。当前未接数据库，即使配置了路径，启动也不会创建业务数据库；完成步骤 1 后才会调用 `store.Open`。

接通数据库后，首次创建全新练习库时将 `init_schema` 设为 `true`，建表成功后再改回 `false`。当前 DDL 没有 `IF NOT EXISTS`，重复建表会报表已存在；不要删除已有数据来掩盖报错。`init_schema: false` 也不会给新库自动建表。

配置只从 YAML 读取，没有环境变量覆盖。`config.yaml` 含本地配置，不要提交或粘贴真实密钥；可用 `git check-ignore -v config.yaml` 检查忽略规则。

## 验证与现有问题

```bash
gofmt -l .
go build ./...
go vet ./...
go test -race ./...
```

截至本次文档整理，运行 `go test ./...` 的已知基线失败为 `internal/store` 的 `TestStore`：`InitSchema: true` 已建表，测试又调用 `InitSchema`，报 `table users already exists`。此外，该测试后面使用的 `test_table` 不在当前嵌入的业务 schema 中。因此不能把这份旧测试当作消息入库已完成的证明。

开始动手前记下基线结果；改动后分别判断“原有失败”和“本次新增失败”。这次只整理文档，以上代码和配置示例的问题仍保留，修复时请单独说明范围。新增业务测试可先用 `go test ./cmd/botd -run Test你的测试名 -v` 单独验证，再检查全量结果。

## 不会用 AI 时，直接复制这段

在仓库根目录开启 AI 会话，让它先读 `AGENTS.md` 和 README。不同工具对 `AGENTS.md` 的自动加载支持不同，可以在第一句话明确要求读取。

> 请先阅读 AGENTS.md 和 README，再查看当前代码。我在做“收发消息入库”练习，数据库基础已准备好，本轮要直接写 SQL，不封装仓储层。我现在不知道从哪里开始。先给我一张简短的消息调用路线，只展开 README 的第 0 步：告诉我打开哪个文件、看哪个函数、这段代码的输入和输出是什么，并给我一个小问题确认我理解了。先不要修改代码，不要一次列出整份实现。后续每轮只教一个可验证的小任务，讲清做什么、为什么、在哪里做、怎么做、完成后怎么看结果。如果我说“看不懂”，请用具体变量和值重讲，不要只换一批术语。

后续可以这样问，不必先准确说出知识点名称：

- “我读到了 `case msg, ok := <-p.Events()`，不知道这一行拿到了什么。请用一条‘你好’消息解释。”
- “我不理解为什么要传 db 参数，请对照已有 log 参数画出谁创建、谁使用。”
- “这一步的会话 ID 是 QQ 给的还是数据库生成的？请指出来源。”
- “我完成了第 2 步，请检查我改的 diff，只指出问题和验证方法，先别代改。”
- “这是命令和错误输出，请区分已有问题与我的改动，再给我最小排查动作。”
- “请解释为什么当前步骤必须在下一步之前做，用这两步的数据依赖说明。”

## 其他文档按需查

| 想了解什么 | 去哪里 |
|---|---|
| AI 的教学方式与项目约定 | [`AGENTS.md`](AGENTS.md) |
| 文档总导航与目标架构 | [`docs/README.md`](docs/README.md) |
| 历史开发进度（部分描述可能落后于代码） | [`docs/当前开发进度.md`](docs/当前开发进度.md) |
| 长期架构、接口、数据库与里程碑设计 | [`docs/design/README.md`](docs/design/README.md) |

技术栈：Go、QQ 官方 `botgo` SDK、纯 Go SQLite `modernc.org/sqlite`、`yaml.v3`、标准库 `log/slog` 与 `lumberjack.v2`。本任务使用标准库 `database/sql` 执行 SQL，不引入 ORM。当前配置只从文件读取，与部分早期设计文档不同。
