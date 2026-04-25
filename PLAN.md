# Noni 完整开发计划

## 项目定位

**Noni** 把交互式 CLI 命令变成一系列无状态的 CLI 调用，让任何 AI agent 都能驾驭原本需要人类在终端前操作的命令。

**核心交互模型**：
```
agent: noni run "gh auth login"
noni:  {id: abc, status: waiting_input, prompt: {type: select, ...}}
agent: noni input abc "GitHub.com"
noni:  {id: abc, status: waiting_input, prompt: {type: password, ...}}
agent: noni secret abc --env GH_TOKEN
noni:  {id: abc, status: exited, exit_code: 0}
```

Agent 看到结构化的 prompt 信息，自己决定输入什么。Noni 不替 agent 做决策，只提供"看清楚 + 安全输入"的能力。

## 技术选型

- **语言**：Go（单二进制、PTY 库成熟、并发友好、跨平台）
- **架构**：daemon (`nonid`) + CLI (`noni`)，Unix socket 通信
- **关键依赖**：
  - `github.com/creack/pty` — PTY 管理
  - 虚拟终端库（M0 选型，候选：`vt10x` / `tcell` / `x/exp/term`）
  - `github.com/spf13/cobra` — CLI 框架
  - 标准库 `net/rpc` over Unix socket — IPC
- **数据格式**：JSON 默认，`--human` 给人看
- **存储**：v1 全内存，崩溃即丢；v2 再加 SQLite 持久化

## 项目结构

```
noni/
├── cmd/
│   ├── noni/         # CLI 客户端
│   ├── nonid/        # daemon
│   └── noni-mcp/     # MCP server 包装
├── internal/
│   ├── session/      # PTY session、状态机
│   ├── detector/     # prompt 检测器
│   ├── terminal/     # 虚拟终端、ANSI 处理
│   ├── ipc/          # Unix socket 协议
│   └── proto/        # 共享 JSON schema
├── testdata/         # 真实 CLI 录像，回归测试用
└── DESIGN.md         # 动手前先写
```

## 时间总览

按"有经验的人 + Claude Code 配合"估算，按 session（2-4 小时连续编码）划分：

| 阶段 | Session 数 | 累计耗时 |
|---|---|---|
| M0 设计 | 1 | 2-3 小时 |
| M1 daemon + CLI 骨架 | 1 | 3-4 小时 |
| M2 完整 CLI + 稳定状态 | 1 | 2-3 小时 |
| M3 prompt 检测 | 2 | 4-6 小时 |
| M4 打磨 + MCP | 1 | 2-3 小时 |
| M5 发布 | 1 | 1-2 小时 |

**总计 14-21 小时**，约**一个充实的周末 + 几个晚上**到 v0.1.0 可发布。

## 各阶段详细计划

### M0：设计文档（Session 1，2-3 小时）

**这是最重要的一步**。写之前花的每一分钟，省掉后面两小时返工。

让 Claude Code 产出 `DESIGN.md`，必须定清楚：

**1. RPC 协议** — 每个方法的请求/响应 JSON schema

```
Run(cmd, args, env, cwd) → {session_id, status, screen, prompt}
Input(id, text, newline) → {status, screen, prompt}
Key(id, keys[]) → {status, screen, prompt}  
Secret(id, env_var) → {status, screen, prompt}
Status(id) → {status, started_at, last_activity}
Read(id, tail) → {screen, raw_bytes}
Wait(id, timeout) → {status, ...}
List() → [{id, cmd, status, ...}]
Kill(id, signal) → {ok}
```

**2. Session 状态机**

```
created → running → waiting_input → running → exited
                ↘ exited           ↗
```

定义清楚每个状态的触发条件和超时行为。

**3. CLI 输出 JSON 格式** — 每个命令返回什么字段、什么时候返回

**4. 稳定状态检测算法**（伪代码）
```
on PTY output:
    last_output_at = now
    if echo_off detected: state = waiting_input(password)
    
ticker every 100ms:
    if state == running:
        if process_exited: state = exited
        elif now - last_output_at > 300ms:
            run prompt detector
            if detected: state = waiting_input
```

**5. 错误码** — session 不存在、已退出、超时等用什么 error 码

**6. 配置项** — `~/.noni/config.toml` 有哪些可配置项

你的工作：看 DESIGN.md，挑刺、改字段名、调状态机。**这步不省**，否则后面所有 session 都在错误的地基上。

**产出**：`DESIGN.md` 定稿。

---

### M1：daemon + CLI 骨架（Session 2，3-4 小时）

**目标**：架子搭起来，能跑通最简单的 echo。

让 Claude Code 一次性生成：

- 项目脚手架：`go.mod`、目录结构、Makefile、`.golangci.yml`
- daemon (`cmd/nonid`)：
  - 监听 `~/.noni/sock`
  - JSON-RPC handler
  - 优雅关闭（SIGTERM/SIGINT 时清理所有子进程）
  - 日志写到 `~/.noni/log`
- session 模块：
  - `Session` 结构（id、cmd、PTY、状态、ring buffer、时间戳）
  - `Manager`：并发安全 CRUD、自动 GC（60 分钟无活动）
  - PTY 集成：fork 子进程、goroutine 读 stdout 进 ring buffer
- CLI 骨架（`cmd/noni`）：
  - `run` / `status` / `list` / `kill` 四个最基本命令
  - daemon 自启逻辑（socket 不存在时 fork 自己 `--daemon`）

**你的验证**：
```bash
noni run "echo hello"          # 返回 exited，screen 包含 hello
noni run "sleep 60"            # 返回 running
noni list                      # 看到 sleep 进程
noni kill <id>                 # 能干掉
```

**产出**：能跑通非交互命令的最小可用版本。

---

### M2：完整 CLI + 稳定状态检测（Session 3，2-3 小时）

**目标**：交互流程跑通，能 input。

- 补全 CLI 命令：`input` / `read` / `wait` / `key`
- 实现稳定状态检测（**关键**）：
  - idle 300ms + 进程仍在运行 → 标记 `waiting_input`
  - 这一步要反复调，可能要写 5-10 个测试 case
- `Wait` 的语义：阻塞到状态变化或超时，返回新快照
- 输出截断：超过 50 行返回头 10 + 尾 40 + 提示

**你的验证**（必须自己跑，时序问题 Claude 看代码看不出来）：
```bash
# 在 shell 1
noni run "bash -c 'read x; echo got: \$x'"
# 应返回 waiting_input

noni input <id> "hello"
# 应返回 exited，screen 含 "got: hello"
```

如果时序不对（比如 Wait 立刻返回但其实进程还没退出），让 Claude 加 debug log，反复跑直到对。

**产出**：能跑简单交互命令。

---

### M3：ANSI 处理 + prompt 检测（Session 4-5，4-6 小时）

**整个项目最难的一段**。必须**真实样本驱动**。

#### Session 4 上半（1 小时）：录样本 + 选库

你的工作（不能让 Claude 替你做）：
1. 装 `gh`、`ssh`、`npm`，配好账号
2. 用 `script -q -c "gh auth login" /tmp/gh.cast` 录下完整字节流
3. 同样录 `ssh-copy-id`、`npm publish`、`docker login`、`pip install`（apt-get 等）

让 Claude 选库：
- 把同一份录像喂给候选的虚拟终端库（vt10x、tcell、x/exp/term）
- 对比"渲染后屏幕"的准确度
- 选定一个

#### Session 4 下半 + Session 5：检测器开发循环

让 Claude 写检测器，三层叠加：

1. **termios 信号**：echo 关闭 → `password`（最可靠）
2. **正则规则库**：
   - `yesno`：`\((y|Y)/(n|N)\)`、`\[Y/n\]`、`(yes/no)` 等
   - `select`：`?` 开头问题 + 缩进列表 + `>`/`*`/`❯` 标记当前项
   - `generic_input`：`:` 或 `>` 结尾的最后一行
3. **fallback**：都不命中 → `type: unknown`，原样返回 screen

开发循环（重复 3-5 次）：

```
跑真命令 → 检测错了 → 把样本加进 testdata → 让 Claude 改规则 → 跑表驱动测试 → 重复
```

每个新样本写成 testdata 里的文件，规则改完所有历史样本必须仍通过。这是**回归测试的命脉**——CLI 升级会改 prompt 文本，没有这个回归套件你迟早会回到原点。

**M3 验收标准**：

```bash
# 完整流程跑通，无人工干预
gh auth login         # select × 4 + password
ssh-copy-id user@host # yesno + password
npm publish           # OTP password
docker login          # username + password
```

**产出**：detector 模块 + testdata 录像库 + 表驱动测试。

---

### M4：打磨 + MCP server（Session 6，2-3 小时）

让 Claude 一次性补齐：

**易用性**：
- `noni input --no-newline`
- `noni key <id> enter` / `up` / `down` / `tab` / `ctrl-c` / `ctrl-d`
- `noni secret <id> --env GH_TOKEN`（不进 shell history、不进日志）
- `noni doctor`（检查 daemon 状态、PTY 支持、socket 权限）
- `noni version`
- 错误信息友好化（session 不存在、daemon 没启动、超时各有清晰提示）

**MCP server**（`cmd/noni-mcp`）：
- 200 行 stdio MCP server，包装上述 RPC
- 工具描述写得 LLM 友好（example、参数说明）
- 在 README 里给 Claude Desktop / Cursor / Cline 各贴一份配置

**产出**：发布前的 v0.0.9。

---

### M5：发布（Session 7，1-2 小时）

让 Claude 处理工程部分：
- GoReleaser 配置：Linux/macOS × x64/arm64
- Homebrew tap 仓库
- GitHub Actions：CI（lint + test + build）+ Release
- `go install` 路径
- deb/rpm 包

你处理营销部分：
- README 写动机、quick start、对比表（vs expect/pexpect/Interactive Shell MCP）
- 用 asciinema 录三个 demo（gh / ssh-copy-id / npm publish）
- "Agent 使用指南"：一段可以贴在 system prompt 里的说明
- 提交：HN Show、awesome-mcp、awesome-go、reddit r/golang、Twitter

**产出**：v0.1.0 release。

---

## 关键技术风险

| 风险 | 影响 | 缓解 |
|---|---|---|
| 稳定状态误判（命令还在跑就以为在等输入） | 体验崩，agent 乱输入 | 多信号融合：idle + 进程状态 + termios；不确定时返回 `maybe_waiting`，agent 自己再 `wait` |
| 虚拟终端库选型不当 | 屏幕乱码 | M3 开始前花 30 分钟对比候选库 |
| prompt 检测覆盖率低 | 用户失望 | 检测不出时优雅降级到 `unknown` + 返回 raw screen，**不卡死**；首版接受 70% 覆盖率 |
| Go PTY 在 macOS 行为差异 | 跨平台 bug | M1 起双平台同步开发，CI 双跑 |
| 真实 CLI 升级导致 prompt 文本变了 | 回归 | testdata 录像 + 表驱动测试，每次外部 CLI 升级跑一遍 |

## 测试策略

- **单元测试**：detector 用 testdata 真实样本表驱动测试
- **集成测试**：bash/expect 写假交互命令，端到端跑
- **录像回归**：asciinema cast 文件存全套真实输出，CI 重放
- **CI**：GitHub Actions Linux + macOS

## 立即可做的第一步

新开一个 Claude Code 会话，把这份计划贴进去，然后只让它做一件事：

> 读这份计划。**不要写任何实现代码**。先写 `DESIGN.md`，包含：
> 1. RPC 协议每个方法的请求/响应 JSON schema
> 2. Session 状态机（状态 + 转换 + 触发条件 + 超时）
> 3. CLI 每个命令的输出 JSON 格式
> 4. 稳定状态检测算法（伪代码）
> 5. 错误码定义
> 6. 配置文件 schema
>
> 我看完给反馈后再开始 M1。

设计定稿之后，每个 session 开头把 DESIGN.md + 当前 milestone 描述 + 上个 session 的代码贴给 Claude Code，让它一次性产出。

## 后续路线（v0.2+）

按外部反馈优先级排，不预先承诺：

- session 持久化（SQLite，daemon 重启不丢）
- prompt 规则库扩充（社区贡献，每个 CLI 一个 yaml）
- TUI 检测：识别 vim/htop 这类全屏应用，明确返回 `tui_unsupported`
- 流式输出：`noni stream <id>` 实时跟踪
- 沙箱：`--sandbox` 选项隔离子进程
- Windows ConPTY 支持（按用户呼声决定）
- LLM fallback 检测器（前两层不命中时调小模型）

## 成功指标

- **M3 结束**：自己用 noni + Claude 完成 5 个真实交互式任务，不切终端
- **发布 1 个月**：100+ stars，3+ 外部用户提 issue
- **3 个月**：被一个知名 agent 框架集成或推荐
- **长期**：成为 "agent 跑交互式命令" 的默认选择

---
