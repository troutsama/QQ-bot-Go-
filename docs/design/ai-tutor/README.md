# ai-tutor · 让 AI「只讲解不代写」的配置（pi 时代，现作参考）

> **现状**：本目录原先叫 `go项目待置入/`，其中的 `AGENTS.md` **已经放到仓库根**并按本项目改写
> （见仓库根的 `AGENTS.md`，那才是当前生效的机制）。
> 仓库根还有 `learn/`（本地、不入库）放「练习册参考答案」。
>
> 这里保留的是 **pi** 那套「四层防护」的原始配置与原理说明。用别的 agent（如 DSH）时，
> 第 1 层（`.pi/settings.json` 物理移除工具）不生效，真正起作用的是 `AGENTS.md` 的软约束。
> 留着它的价值：**它是「为什么要把约束写成文件」这个设计的记录**。

---

## 一、当时打算怎么放（已完成，保留作记录）

```bash
# 假设 Go 项目在 ~/code/qq-bot-go
cp -R "napcatQQ/go 改造/go项目待置入/." ~/code/qq-bot-go/

# 结果（注意 .pi 和 .agents 是隐藏目录，用 ls -a 才看得到）
# ~/code/qq-bot-go/
# ├── AGENTS.md
# ├── .pi/
# │   ├── settings.json
# │   └── APPEND_SYSTEM.md
# └── .agents/
#     └── skills/
#         └── go-tutor/
#             └── SKILL.md
```

设计文档同时放进 `docs/design/`（这样 agent 讲解时能引用章节号）：

```bash
mkdir -p ~/code/qq-bot-go/docs/design
cp "napcatQQ/go 改造"/*.md ~/code/qq-bot-go/docs/design/
```

**实际结果**：只做了 `AGENTS.md`（放仓库根，且大幅改写），`.pi/` 与 `.agents/` **没有**复制到仓库根 ——
因为换了 agent 工具，这两层不再生效。如果你以后要用 pi，再把它们拷到仓库根即可。

---

## 二、四层防护，强弱不同

| 层 | 文件 | 作用机制 | 强度 |
|----|------|----------|------|
| 1 | `.pi/settings.json` | **物理移除工具**：`defaultTools` 只留 `read/grep/find/ls`，`edit`/`write`/`bash` 根本不存在 | 🔒 **硬约束**（工具层面无法调用） |
| 2 | `.pi/APPEND_SYSTEM.md` | 追加到系统提示的强制指令 | 🔒 强（模型每次都会看到） |
| 3 | `.agents/skills/go-tutor/SKILL.md` | 按需加载的详细教学协议（可 `/skill:go-tutor` 主动调用） | 🔒 中强 |
| 4 | `AGENTS.md` | 项目上下文指令（每次启动加载） | ⚠️ 中（上下文越长越容易被稀释） |

**关键点是第 1 层**：`edit`/`write` 不在工具列表里，模型**没有办法**创建或修改文件 —— 这不是「让它别做」，而是「它做不到」。其余三层负责让它的**回答方式**符合教学需求。

> 为什么还要写 4 层？因为第 1 层只挡「写文件」，挡不住「在聊天里输出一大段实现让你复制」。第 2、3、4 层就是挡这个的。这三层是**软约束**（模型可能不完全遵守），所以 AGENTS.md 里明确写了「如果我偷懒，请拒绝并指出」，把判断权交给对话。

---

## 三、首次使用注意

### 1. 需要信任项目

pi 在加载项目级 `.pi/settings.json` 与技能前会询问是否信任该目录（因为项目内配置能改变 agent 行为）。**必须同意**，否则第 1 层的工具白名单不会生效：

- 交互式启动时会弹一次询问 → 选同意
- 或用 `/trust` 记住决定（写入 `~/.pi/agent/trust.json`）
- 或单次运行加 `--approve` / `-a`

### 2. 验证约束真的生效

```bash
cd ~/code/qq-bot-go && pi
# 启动时的 header 会列出已加载的 AGENTS.md / skills
```

然后故意试一次越界请求：

```
帮我创建 cmd/botd/main.go
```

期望回应：**拒绝**，并说明「本项目为学习模式，我只讲解不代写」，同时给你行动指引。

也可以看当前可用工具（pi 的 `/tools` 命令或启动 header）。

---

## 四、逃生通道（需要时用）

| 需求 | 命令 |
|------|------|
| 我需要它跑 `go test` 帮我分析失败 | `pi --tools read,grep,find,ls,bash` |
| 我需要它读代码 + 写代码（例如 V2.5 的 school 翻译） | `pi --tools read,grep,find,ls,edit,write,bash` |
| 只是这一次想让它写一个文件 | 在对话框里显式说「**这次允许你写 `internal/xxx/yyy.go`**」，并在结束时说明「恢复默认约束」 |

⚠️ **注意 `bash` 等于半个 `write`**：`bash` 可以执行 `cat > file`、`sed -i`、`mkdir` 等。所以「只解锁 bash 用于跑测试」在实践中是**不成立的**（模型有可能用 bash 写文件）。如果只是想让编译器/测试给你反馈，最稳的做法是：**你自己跑命令，把输出贴给它**。

---

## 五、例外流程要写进对话

`AGENTS.md` §8 定义了例外流程。实践中建议这样用：

```
你：这次例外：允许你创建 internal/school/timetable/parse.go，只写这一个文件。
   （原因：V2.5 的 school 是既有 TS 逻辑的翻译，docs/design/01 §1.4 允许借助 AI）

[它写完]

你：回到默认约束，接下来照样只讲解。
```

这样既利用了一次性效率，又不会让例外变成常态。

---

## 六、如果你想换掉这套约束

这套配置的目的是「学习」，等 Go 熟练之后可以整体删除：

```bash
rm -rf .pi .agents AGENTS.md
```

或只删系统提示那一层（保留技能）：

```bash
rm .pi/APPEND_SYSTEM.md
```
