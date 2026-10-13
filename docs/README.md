# 项目文档导航与实用手册

> 本目录记录 qq-bot-go 的核心架构设计、数据表规划与开发进度。
> 架构设计摒弃复杂冗余的框架，坚持实用、轻量与原生 Go 风格。

---

## 一、模块与目录职责速查

所有核心业务代码均位于 `internal/`，遵循清晰的**端口/适配器架构**：

| 目录 | 职责与定位 | 关键说明 |
|------|------------|----------|
| `cmd/botd/` | **程序唯一入口（组合根）** | 负责读取配置、初始化数据库、装配各层依赖、监听系统退出信号 |
| `internal/config/` | **单文件配置管理** | 解析 `config.yaml`，提供默认值填充与启动/重载校验 |
| `internal/platform/`| **通信平台端口与适配器** | 定义事件与收发接口；包含 `qqbot`（官方 WS/API）和 `mock`（单测与本地离线调试） |
| `internal/llm/` | **大模型协议适配层** | 定义标准 Prompt/Response 中性格式；直连各模型协议（OpenAI Chat、Anthropic 等） |
| `internal/chat/` | **核心业务领域层** | 处理会话管理、历史多轮拼装、防重去重、消息分段回复、串行队列调度 |
| `internal/store/` | **数据持久化适配器** | 纯 Go SQLite 实现；封装所有 SQL 操作，提供数据迁移与双向追溯读写 |
| `internal/logx/` | **基础运行日志** | 基于标准库 `log/slog`，支持控制台与文件双写、自动文件轮转与密钥脱敏 |
| `internal/admin/` | **运维与调试 HTTP API** | 提供健康检查、手工消息注入测试、日志与事件分页查询 |

---

## 二、核心数据库架构（方案 A 定稿）

数据库采用 SQLite（通过 `modernc.org/sqlite` 免 CGO），目前包含 **6 张业务核心表 + 1 张审计表**：

```
users (用户资料) ──────┐
                       ├──> conversations (会话房间) ──> turns (问答轮次) ──> messages (上下文明细)
groups (群组信息) ─────┘                                    │
                                                            └──> llm_requests (全量请求/响应快照)
events (系统运维事件审计)
```

1. **`users`**：被动记录与机器人产生交互的用户基本资料（`openid`, `nickname`, `avatar`）。
2. **`groups`**：记录机器人所在的群组资料（`group_openid`, `name`）。
3. **`conversations`**：会话聚合，统一以 `scene_id`（承接 `user_openid` 或 `group_openid`）与 `scene` 组合，零前置查询开销。
4. **`llm_requests`**：**全量无损快照**。存入完整的输入 prompt 上下文、参数、模型回复、工具调用与 Token 消耗 JSON，支持无损溯源。
5. **`turns`**：一问一答周期，通过 `UNIQUE(conversation_id, inbound_msg_id)` 实现平台消息幂等；挂载 `llm_request_id` 实现双向定位。
6. **`messages`**：轮内消息片段（`role`, `content`, `seq_in_turn`），专供极速拉取最近 10 轮有效 Prompt 上下文。
7. **`events`**：记录断线重连、配置重载、过滤拦截等系统运行事件，供 Admin 面板分页排障。

---

## 三、设计文档精炼索引

如需查阅具体实现细节，可按需参考 `docs/design/` 下的文件：

- **架构与接口设计**：[`design/02-架构与接口.md`](design/02-架构与接口.md)
  - 核心接口定义（`Provider` / `Client` / `Store`）与依赖装配逻辑。
- **踩坑与风险手册**：[`design/04-风险与决策.md`](design/04-风险与决策.md)
  - 重点阅读：`R1`（被动回复次数限额）、`R2`（消息重推幂等）、`R3`（同会话并发乱序）、`R6`（沙箱与正式 AppID 隔离）。
- **逐包实现示范**：[`design/06-V1-编码指南.md`](design/06-V1-编码指南.md)
  - 包含 SQLite 迁移、标准库 slog 配置、表驱动测试示范与常见报错速查。
- **任务分步清单**：[`design/07-V1-实施计划.md`](design/07-V1-实施计划.md)
  - 里程碑 M0（地基）到 M7（收尾）的逐步推进清单。

---

## 四、快速开发上手

```bash
# 1. 复制配置文件
cp config.yaml.example config.yaml

# 2. 本地 Mock 模式验证启动（无需真实 QQ 机器人配置）
go run ./cmd/botd -config config.yaml

# 3. 运行静态检查与单测
gofmt -l -w . && go vet ./...
go test -v -race ./...
```
