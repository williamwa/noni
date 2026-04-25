// Package proto holds shared JSON-RPC request/response types between
// noni (CLI) and nonid (daemon). See DESIGN.md §2.
package proto

import "time"

type Status string

const (
	StatusCreated      Status = "created"
	StatusRunning      Status = "running"
	StatusWaitingInput Status = "waiting_input"
	StatusStalled      Status = "stalled"
	StatusExited       Status = "exited"
)

type PromptType string

const (
	PromptPassword PromptType = "password"
	PromptYesNo    PromptType = "yesno"
	PromptSelect   PromptType = "select"
	PromptInput    PromptType = "input"
	PromptUnknown  PromptType = "unknown"
)

type SelectOption struct {
	Label    string `json:"label"`
	Selected bool   `json:"selected"`
}

type Prompt struct {
	Type       PromptType     `json:"type"`
	Question   string         `json:"question,omitempty"`
	Options    []SelectOption `json:"options,omitempty"`
	Default    string         `json:"default,omitempty"`
	Echo       bool           `json:"echo"`
	Confidence float64        `json:"confidence"`
}

type Cursor struct {
	Row int `json:"row"`
	Col int `json:"col"`
}

type Snapshot struct {
	SessionID       string    `json:"session_id"`
	Cmd             string    `json:"cmd,omitempty"`
	Status          Status    `json:"status"`
	Screen          []string  `json:"screen"`
	ScreenTruncated bool      `json:"screen_truncated"`
	Cursor          Cursor    `json:"cursor"`
	Prompt          *Prompt   `json:"prompt,omitempty"`
	ExitCode        *int      `json:"exit_code,omitempty"`
	Signal          string    `json:"signal,omitempty"`
	StartedAt       time.Time `json:"started_at"`
	LastActivity    time.Time `json:"last_activity"`
}

// --- Requests ---

type RunReq struct {
	Cmd    string            `json:"cmd"`
	Args   []string          `json:"args"`
	Env    map[string]string `json:"env,omitempty"`
	Cwd    string            `json:"cwd,omitempty"`
	Cols   int               `json:"cols,omitempty"`
	Rows   int               `json:"rows,omitempty"`
	WaitMs int               `json:"wait_ms,omitempty"`
}

type InputReq struct {
	SessionID  string `json:"session_id"`
	Text       string `json:"text"`
	Newline    bool   `json:"newline"`
	HideInLog  bool   `json:"hide_in_log,omitempty"`
}

type KeyReq struct {
	SessionID string   `json:"session_id"`
	Keys      []string `json:"keys"`
}

type SecretReq struct {
	SessionID string `json:"session_id"`
	EnvVar    string `json:"env_var"`
	Newline   bool   `json:"newline"`
}

type IDReq struct {
	SessionID string `json:"session_id"`
}

type ReadReq struct {
	SessionID string `json:"session_id"`
	TailLines int    `json:"tail_lines,omitempty"`
	Raw       bool   `json:"raw,omitempty"`
}

type ReadResp struct {
	Snapshot
	RawBytes string `json:"raw_bytes,omitempty"` // base64
}

type WaitReq struct {
	SessionID string `json:"session_id"`
	TimeoutMs int    `json:"timeout_ms,omitempty"`
	Until     string `json:"until,omitempty"` // state_change|prompt|exit|idle
}

type ListResp struct {
	Sessions []Snapshot `json:"sessions"`
}

type KillReq struct {
	SessionID string `json:"session_id"`
	Signal    string `json:"signal,omitempty"`
}

type OKResp struct {
	OK        bool   `json:"ok"`
	SessionID string `json:"session_id,omitempty"`
}

type ResizeReq struct {
	SessionID string `json:"session_id"`
	Cols      int    `json:"cols"`
	Rows      int    `json:"rows"`
}

type PingResp struct {
	Version  string `json:"version"`
	UptimeS  int64  `json:"uptime_s"`
}

type StreamReq struct {
	SessionID   string `json:"session_id"`
	SkipBacklog bool   `json:"skip_backlog,omitempty"`
}

// StreamFrame is one envelope in a Stream response. Kind values:
//
//	"initial" — backlog bytes already buffered when the stream started
//	"chunk"   — new bytes arriving from the PTY
//	"state"   — status changed (e.g. running → waiting_input)
//	"end"     — terminal frame; client should stop reading
type StreamFrame struct {
	Kind     string  `json:"kind"`
	Bytes    string  `json:"bytes,omitempty"` // base64-encoded raw PTY bytes
	Status   Status  `json:"status,omitempty"`
	Prompt   *Prompt `json:"prompt,omitempty"`
	ExitCode *int    `json:"exit_code,omitempty"`
	Signal   string  `json:"signal,omitempty"`
}

// --- Errors ---

const (
	EBadRequest     = "E_BAD_REQUEST"
	ENotFound       = "E_NOT_FOUND"
	ENotWaiting     = "E_NOT_WAITING"
	EAlreadyExited  = "E_ALREADY_EXITED"
	ETimeout        = "E_TIMEOUT"
	EPTYFailed      = "E_PTY_FAILED"
	EPermission     = "E_PERMISSION"
	EDaemonDown     = "E_DAEMON_DOWN"
	EInternal       = "E_INTERNAL"
)

type RPCError struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

func (e *RPCError) Error() string { return e.Code + ": " + e.Message }

func NewError(code, msg string) *RPCError { return &RPCError{Code: code, Message: msg} }
