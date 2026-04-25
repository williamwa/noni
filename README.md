# noni

> Drive interactive CLIs from anywhere. Turn `gh auth login`, `ssh-copy-id`, `npm publish` and friends into a sequence of stateless calls that any agent — or script — can drive.

```
agent: noni run "gh auth login"
noni:  {id: abc, status: waiting_input, prompt: {type: select, options: [...]}}
agent: noni key abc enter
noni:  {id: abc, status: waiting_input, prompt: {type: yesno, default: "y"}}
agent: noni input abc Y
noni:  {id: abc, status: waiting_input, prompt: {type: password, echo: false}}
agent: noni secret abc --env GH_TOKEN
noni:  {id: abc, status: exited, exit_code: 0}
```

The agent sees structured prompts and decides what to send. `noni` doesn't make decisions for you — it just gives you "see clearly + input safely".

## Status

**v0.1.0-dev** — pre-release, in active development. See [PLAN.md](PLAN.md) for the milestone roadmap and [DESIGN.md](DESIGN.md) for the protocol/state-machine spec.

| Milestone | Status |
|---|---|
| M0 design | ✅ |
| M1 daemon + CLI scaffold | ✅ |
| M2 stable-state detection + key/secret | ✅ |
| M3 prompt detector | 🚧 rules done, real-CLI testdata pending |
| M4 polish + MCP server | ⏳ |
| M5 release | ⏳ |

Linux only for now. macOS works in theory (termios constants are wired) but untested. Windows: not in v0.1.

## Architecture

```
┌────────┐   JSON-RPC 2.0 over   ┌─────────┐   PTY   ┌─────────┐
│  noni  │ ────── Unix socket ──▶│  nonid  │ ──────▶ │  child  │
│  CLI   │ ◀─────────────────────│  daemon │ ◀────── │ process │
└────────┘                       └─────────┘         └─────────┘
```

- **`noni`** — stateless CLI client. Each invocation is one RPC.
- **`nonid`** — long-lived daemon that owns PTY-backed sessions. Auto-spawned by the CLI on first call.
- **socket** — `$XDG_RUNTIME_DIR/noni/sock` (falls back to `~/.noni/sock`), mode 0600.

## Build & install

```bash
git clone https://github.com/williamwa/noni
cd noni
make build           # produces ./bin/noni and ./bin/nonid
make install         # copies both to ~/.local/bin/
```

Requires Go 1.22+.

## Quick start

```bash
# non-interactive
noni run -- echo hello

# interactive: bash read with a prompt
OUT=$(noni run --wait 1500 -- bash -c 'read -p "name: " x; echo got:$x')
ID=$(echo "$OUT" | jq -r .session_id)
noni input "$ID" alice
noni wait "$ID" --until exit

# password (echo off → detected as type=password)
noni run --wait 1500 -- bash -c 'read -s -p "pw: " p; echo'

# pass a secret without putting it on the wire
GH_TOKEN=ghp_xxx ./bin/nonid &      # daemon must hold the env var
noni run --wait 1500 -- gh auth login
# … navigate the menus with `noni key <id> down enter` …
noni secret <id> --env GH_TOKEN

# send named keys
noni run -- cat
noni key <id> ctrl-c

# health
noni ping
noni list
noni status <id>
```

## Commands

| Command | Purpose |
|---|---|
| `noni run [flags] -- <cmd> [args...]` | Start a command in a PTY |
| `noni input <id> <text>` | Send text (newline appended unless `--no-newline`) |
| `noni key <id> <key>...` | Send named keys: `enter`, `tab`, `up`, `down`, `ctrl-c`, `f1`, … |
| `noni secret <id> --env VAR` | Send daemon's `$VAR` as input — never on RPC wire |
| `noni read <id> [--tail N] [--raw]` | Read current screen |
| `noni wait <id> [--until X]` | Block until state change / exit / prompt / idle |
| `noni status <id>` | Snapshot |
| `noni list` | List active sessions |
| `noni kill <id> [--signal SIG]` | TERM / KILL / INT / HUP |
| `noni ping` | Daemon liveness |
| `noni version` | Version |

All commands emit JSON to stdout. Non-zero exit codes: `1` user error, `2` daemon/PTY error, `3` timeout.

## Prompt types

`noni` returns a `prompt` block when a session is `waiting_input`:

| Type | Trigger | Confidence |
|---|---|---|
| `password` | termios ECHO disabled | 0.99 |
| `yesno` | `(y/n)`, `[Y/n]`, `(yes/no)` patterns; `default` extracted from capital letter | 0.9 |
| `select` | `>` / `❯` / `*` marker + indented option block; `selected` flag per option | 0.85 |
| `input` | trailing `:` / `?` / `>` | 0.7 |
| `unknown` | fallback after 1s idle when nothing matches | 0.0 |

When the detector falls back to `unknown`, the agent can still inspect `screen` and decide what to send.

## Security

- Socket is `0600`, no TCP listener.
- `noni secret` reads from the **daemon's** environment — secrets never appear in RPC params or the daemon log.
- Daemon log: `~/.noni/log` (sensitive payloads redacted).

## Status machine

```
                ┌────────────────────────────┐
                │                            ▼
[created] ──▶ [running] ──idle+detect──▶ [waiting_input]
                │       ◀──input/key────────┘
                │
                └──exit──▶ [exited] ──60min──▶ [reaped]
```

See [DESIGN.md](DESIGN.md) for the full RPC protocol, error codes, and config schema.

## Tests

```bash
go test ./...
```

The detector ships with table-driven tests covering each prompt type. Real-CLI recordings (`gh auth login`, `ssh-copy-id`, …) will be added under `testdata/` as M3 progresses.

## License

TBD.
