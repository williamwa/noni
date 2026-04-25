# Noni 设计文档 (v0.1)

> 本文档定义 Noni v0.1 的协议、状态机、检测算法、错误码与配置。实现以本文为准；改动须先改本文。

## 0. 术语

- **session**：一个被 PTY 包裹的子进程及其状态，由 daemon 持有。
- **screen**：虚拟终端渲染后的可见屏幕（行数组），不含 ANSI 控制序列。
- **raw**：未经渲染的原始字节（含 ANSI），调试与回放用。
- **prompt**：检测器对当前屏幕做出的结构化判断（type + 字段）。
- **stable**：PTY 在 `idle_threshold` 内无新输出，且子进程仍存活。

---

## 1. 架构与 IPC

```
┌─────────┐  JSON-RPC 2.0 over Unix socket  ┌──────────┐  PTY  ┌─────────┐
│  noni   │ ──────────────────────────────▶│  nonid   │ ─────▶│  child  │
│  (CLI)  │ ◀──────────────────────────────│ (daemon) │ ◀─────│ process │
└─────────┘         ~/.noni/sock           └──────────┘       └─────────┘
```

- **Socket**：`$XDG_RUNTIME_DIR/noni/sock`，回退到 `~/.noni/sock`。权限 `0600`。
- **协议**：JSON-RPC 2.0，单连接单请求，按行分隔 (`\n`)。
- **自启**：CLI 连不上 socket → `fork+exec` 自身 `nonid --daemon`，等待 socket 出现（最多 2s）。
- **日志**：`~/.noni/log`（追加，按天 rotate，保留 7 天）。

---

## 2. RPC 协议

所有方法走 JSON-RPC 2.0。下面只列 `params` 和 `result` 的 schema；`error` 见 §6。

### 2.1 公共类型

```jsonc
// Snapshot — 几乎每个 RPC 都返回这块
{
  "session_id": "01HXYZ...",        // ULID
  "status": "running",              // §3 状态
  "screen": ["line1", "line2"],     // 渲染后的可见屏幕（截断后）
  "screen_truncated": false,        // 是否做了头/尾截断
  "cursor": {"row": 1, "col": 8},   // 当前光标
  "prompt": {                       // 仅在 status=waiting_input 时存在
    "type": "password",             // password|yesno|select|input|unknown
    "question": "Enter password:",  // 问题文本（去掉 ANSI）
    "options": [                    // 仅 select
      {"label": "GitHub.com", "selected": true},
      {"label": "GitHub Enterprise", "selected": false}
    ],
    "default": "y",                 // 仅 yesno（可空）
    "echo": false,                  // false=密码模式
    "confidence": 0.92              // 0..1
  },
  "exit_code": null,                // 仅 status=exited
  "signal": null,                   // 仅被信号终止时
  "started_at": "2026-04-25T...Z",
  "last_activity": "2026-04-25T...Z"
}
```

### 2.2 方法

| Method   | Params                                                         | Result        |
|----------|----------------------------------------------------------------|---------------|
| `Run`    | `{cmd, args[], env{}, cwd, cols=120, rows=40, wait_ms=500}`    | `Snapshot`    |
| `Input`  | `{session_id, text, newline=true, hide_in_log=false}`          | `Snapshot`    |
| `Key`    | `{session_id, keys[]}`  // 见 §2.3                              | `Snapshot`    |
| `Secret` | `{session_id, env_var, newline=true}`                          | `Snapshot`    |
| `Status` | `{session_id}`                                                 | `Snapshot`    |
| `Read`   | `{session_id, tail_lines=0, raw=false}`                        | `{...Snapshot, raw_bytes?}` |
| `Wait`   | `{session_id, timeout_ms=10000, until="state_change"}`         | `Snapshot`    |
| `List`   | `{}`                                                           | `[Snapshot精简]` |
| `Kill`   | `{session_id, signal="TERM"}`                                  | `{ok: true}`  |
| `Resize` | `{session_id, cols, rows}`                                     | `Snapshot`    |
| `Ping`   | `{}`                                                           | `{version, uptime_s}` |

**`Run` 行为**：fork+PTY，立刻返回 (`wait_ms=0`) 或等待最多 `wait_ms` 让状态稳定后再返回快照。默认 500ms 让常见 banner 跑完。

**`Wait.until`**：
- `state_change`：状态从当前值变化即返回
- `prompt`：进入 `waiting_input` 即返回
- `exit`：进程退出才返回
- `idle`：稳定 ≥ `idle_threshold` 返回（不等状态变化）

**`Key.keys`** 字面量：`enter|tab|esc|backspace|space|up|down|left|right|home|end|pgup|pgdn|ctrl-c|ctrl-d|ctrl-z|ctrl-l|ctrl-u|ctrl-w|f1..f12`。一次调用按顺序发送。

---

## 3. Session 状态机

```
                  ┌───────────────────────────────┐
                  │                               ▼
  [created] ──▶ [running] ──idle+detected──▶ [waiting_input]
                  │                               │
                  │   ◀──input/key sent───────────┘
                  │
                  ├──child exit──▶ [exited]
                  ├──signal─────▶ [exited]   (signal != 0)
                  └──kill rpc───▶ [exited]
                                     │
                                     └──gc 60min──▶ [reaped]  (从 List 移除)
```

| 状态            | 进入条件                                | 离开条件                               | 超时               |
|----------------|---------------------------------------|---------------------------------------|--------------------|
| `created`      | `Run` 调用刚返回，PTY 未起               | PTY 起来                               | 1s 起不来 → `error` |
| `running`      | 子进程存活，PTY 有输出                   | idle ≥ `idle_threshold` 且检测命中 / 退出 | —                  |
| `waiting_input`| 稳定检测命中                            | 收到 `Input/Key/Secret` 后回 `running` / 退出 | `prompt_ttl`=10min（无人响应转 `stalled`） |
| `stalled`      | `waiting_input` 超时未响应               | 收到输入 / 被 kill                       | —                  |
| `exited`       | 子进程退出 / 被 kill                     | 60min 未访问 → `reaped`                  | —                  |

**关键不变量**：
- 只有处于 `waiting_input | stalled` 时 `Input/Key/Secret` 合法；其它状态返回 `E_NOT_WAITING`（除非 `--force`）。
- `running ↔ waiting_input` 由后台 ticker 驱动（每 100ms 评估一次稳定性）。
- 输入写入后立刻置回 `running`，不等下一次 tick。

---

## 4. 稳定状态检测算法

```
state.last_output_at        — 每次 PTY read 更新
state.last_input_at         — 每次写入 PTY 更新
state.echo_off              — 由 termios ECHO 位变化触发
state.process_alive         — waitpid(WNOHANG) 每 tick 评估

# 写入信号通道（PTY reader goroutine）
on read(bytes):
    vt.feed(bytes)
    state.last_output_at = now
    if state == waiting_input: state = running   # 收到新输出说明对方仍在动

# 评估通道（每 idle_tick=100ms）
loop:
    if !state.process_alive:
        state = exited; reap_exit_code(); break

    idle = now - max(last_output_at, last_input_at)

    if state.echo_off and idle >= idle_threshold_password (150ms):
        state = waiting_input
        prompt = {type: password, echo: false, confidence: 0.99}
        continue

    if state == running and idle >= idle_threshold (300ms):
        result = detector.run(vt.screen, vt.cursor)
        if result.confidence >= 0.6:
            state = waiting_input; prompt = result
        elif idle >= idle_threshold_unknown (1000ms):
            state = waiting_input
            prompt = {type: unknown, confidence: 0.0}
```

参数（可配）：
- `idle_threshold = 300ms`
- `idle_threshold_password = 150ms`（echo 关闭时更短）
- `idle_threshold_unknown = 1000ms`（迟迟检测不出再降级）
- `idle_tick = 100ms`

---

## 5. CLI 输出 JSON

**默认 JSON**，`--human` 切人类视图。所有命令成功路径输出包含 `Snapshot` 字段；失败路径见 §6。

| 命令                              | 输出                                              |
|----------------------------------|---------------------------------------------------|
| `noni run <cmd...>`              | `Snapshot`                                       |
| `noni input <id> <text>`         | `Snapshot`                                       |
| `noni key <id> <key>...`         | `Snapshot`                                       |
| `noni secret <id> --env VAR`     | `Snapshot`（不回显 text）                         |
| `noni status <id>`               | `Snapshot`                                       |
| `noni read <id> [--tail N] [--raw]` | `Snapshot` (+ `raw_bytes` base64)              |
| `noni wait <id> [--timeout N] [--until X]` | `Snapshot`                              |
| `noni list`                      | `{sessions: [...]}`                              |
| `noni kill <id> [--signal SIG]`  | `{ok: true, session_id}`                         |
| `noni doctor`                    | `{daemon, socket, pty, version, warnings[]}`     |
| `noni version`                   | `{version, commit, go_version}`                  |
| `noni ping`                      | `{version, uptime_s}`                            |

退出码：成功 `0`；用户错（参数错、id 不存在）`1`；daemon 错（连不上、内部 panic）`2`；超时 `3`；子进程非零退出（仅 `wait --propagate-exit`）`4`。

---

## 6. 错误码

JSON-RPC `error.data.code` 用字符串：

| Code              | HTTP 类比 | 含义                                  |
|-------------------|----------|--------------------------------------|
| `E_BAD_REQUEST`   | 400      | 参数缺失/类型错                         |
| `E_NOT_FOUND`     | 404      | session 不存在                         |
| `E_NOT_WAITING`   | 409      | 当前状态不接受输入                       |
| `E_ALREADY_EXITED`| 410      | session 已退出                         |
| `E_TIMEOUT`       | 408      | `Wait` 超时                            |
| `E_PTY_FAILED`    | 500      | PTY/fork 失败                           |
| `E_PERMISSION`    | 403      | socket 权限或文件访问被拒                 |
| `E_DAEMON_DOWN`   | 503      | CLI 端：连不上 daemon（自启失败）          |
| `E_INTERNAL`      | 500      | 兜底                                   |

CLI 端把这些 code 映射成人话写到 stderr，并以 §5 的 exit code 退出。

---

## 7. 配置文件

`~/.noni/config.toml`（可选；不存在用默认值）：

```toml
[daemon]
socket_path = ""          # 空=自动 (XDG_RUNTIME_DIR/noni/sock)
log_path    = "~/.noni/log"
log_level   = "info"      # debug|info|warn|error
session_gc_after = "60m"
prompt_stalled_after = "10m"

[detector]
idle_threshold          = "300ms"
idle_threshold_password = "150ms"
idle_threshold_unknown  = "1000ms"
idle_tick               = "100ms"
min_confidence          = 0.6

[output]
default_cols = 120
default_rows = 40
ring_buffer_kb = 256       # 每 session raw 缓冲
screen_max_lines = 50      # 超出做头10+尾40截断

[secret]
env_passthrough = ["GH_TOKEN","NPM_TOKEN","DOCKER_PASSWORD"]   # Secret 允许引用的白名单
```

环境变量覆盖：`NONI_SOCKET`、`NONI_LOG_LEVEL`、`NONI_CONFIG`。

---

## 8. 安全

- socket `0600`，不监听 TCP。
- `Secret` 从 daemon 进程的环境变量读取，**不**走 RPC payload，不入日志（即使 `log_level=debug` 也只记 `<redacted len=N>`）。
- `Input.hide_in_log=true` 时同样脱敏。
- 子进程默认继承 daemon 的环境；`Run.env` 是叠加而非替换，敏感变量靠白名单。

---

## 9. 待 M1 落地前敲定的开放问题

1. **ULID vs UUID**：用 ULID（时间排序 + 短）。
2. **screen 编码**：UTF-8 字符串数组；宽字符按 grapheme cluster 算列。
3. **PTY 库**：`creack/pty`。
4. **虚拟终端库**：M3 决定，先在 `internal/terminal` 留接口 `Feed([]byte)` / `Snapshot() Screen`。
5. **Windows**：v0.1 不支持，daemon 启动时若 GOOS=windows 直接报 `E_INTERNAL: windows not supported in v0.1`。
