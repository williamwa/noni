// Package session manages PTY-backed child processes.
//
// M1 scope: PTY lifecycle, ring buffer of raw bytes, simplistic screen
// rendering (line splitting), and a tick loop that flips status to exited
// on child reap. Real prompt detection lands in M3.
package session

import (
	"bytes"
	"crypto/rand"
	"errors"
	"io"
	"os"
	"os/exec"
	"sort"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/creack/pty"
	"github.com/oklog/ulid/v2"

	"github.com/williamwang/noni/internal/proto"
)

const (
	defaultCols    = 120
	defaultRows    = 40
	ringBufferSize = 256 * 1024
	screenMaxLines = 50
)

type Session struct {
	ID        string
	Cmd       string
	FullCmd   []string
	StartedAt time.Time

	mu           sync.Mutex
	status       proto.Status
	exitCode     *int
	signal       string
	lastOutputAt time.Time
	lastInputAt  time.Time
	lastAccessAt time.Time
	prompt       *proto.Prompt

	cmd  *exec.Cmd
	ptmx *os.File
	ring *ringBuffer

	doneCh chan struct{} // closed when reaped
}

type Manager struct {
	mu       sync.RWMutex
	sessions map[string]*Session
	entropy  io.Reader
	stopCh   chan struct{}
}

func NewManager() *Manager {
	m := &Manager{
		sessions: make(map[string]*Session),
		entropy:  rand.Reader,
		stopCh:   make(chan struct{}),
	}
	go m.gcLoop()
	return m
}

func (m *Manager) Stop() {
	close(m.stopCh)
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, s := range m.sessions {
		_ = s.kill(syscall.SIGTERM)
	}
}

func (m *Manager) newID() string {
	return ulid.MustNew(ulid.Timestamp(time.Now()), m.entropy).String()
}

// Run forks a child wrapped in a PTY.
func (m *Manager) Run(req proto.RunReq) (*Session, error) {
	if req.Cmd == "" {
		return nil, proto.NewError(proto.EBadRequest, "cmd is required")
	}
	cols, rows := req.Cols, req.Rows
	if cols == 0 {
		cols = defaultCols
	}
	if rows == 0 {
		rows = defaultRows
	}

	c := exec.Command(req.Cmd, req.Args...)
	if req.Cwd != "" {
		c.Dir = req.Cwd
	}
	c.Env = os.Environ()
	for k, v := range req.Env {
		c.Env = append(c.Env, k+"="+v)
	}

	ptmx, err := pty.StartWithSize(c, &pty.Winsize{Cols: uint16(cols), Rows: uint16(rows)})
	if err != nil {
		return nil, proto.NewError(proto.EPTYFailed, err.Error())
	}

	now := time.Now()
	full := append([]string{req.Cmd}, req.Args...)
	s := &Session{
		ID:           m.newID(),
		Cmd:          req.Cmd,
		FullCmd:      full,
		StartedAt:    now,
		status:       proto.StatusRunning,
		lastOutputAt: now,
		lastAccessAt: now,
		cmd:          c,
		ptmx:         ptmx,
		ring:         newRingBuffer(ringBufferSize),
		doneCh:       make(chan struct{}),
	}

	m.mu.Lock()
	m.sessions[s.ID] = s
	m.mu.Unlock()

	go s.readLoop()
	go s.waitChild()

	return s, nil
}

func (m *Manager) Get(id string) (*Session, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	s, ok := m.sessions[id]
	if !ok {
		return nil, proto.NewError(proto.ENotFound, "session not found: "+id)
	}
	return s, nil
}

func (m *Manager) List() []*Session {
	m.mu.RLock()
	defer m.mu.RUnlock()
	out := make([]*Session, 0, len(m.sessions))
	for _, s := range m.sessions {
		out = append(out, s)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].StartedAt.Before(out[j].StartedAt) })
	return out
}

func (m *Manager) Kill(id, sig string) error {
	s, err := m.Get(id)
	if err != nil {
		return err
	}
	signum := syscall.SIGTERM
	switch strings.ToUpper(sig) {
	case "", "TERM", "SIGTERM":
		signum = syscall.SIGTERM
	case "KILL", "SIGKILL":
		signum = syscall.SIGKILL
	case "INT", "SIGINT":
		signum = syscall.SIGINT
	case "HUP", "SIGHUP":
		signum = syscall.SIGHUP
	}
	return s.kill(signum)
}

// gcLoop reaps exited sessions after 60 minutes of inactivity.
func (m *Manager) gcLoop() {
	t := time.NewTicker(1 * time.Minute)
	defer t.Stop()
	for {
		select {
		case <-m.stopCh:
			return
		case <-t.C:
			cutoff := time.Now().Add(-60 * time.Minute)
			m.mu.Lock()
			for id, s := range m.sessions {
				s.mu.Lock()
				if s.status == proto.StatusExited && s.lastAccessAt.Before(cutoff) {
					delete(m.sessions, id)
				}
				s.mu.Unlock()
			}
			m.mu.Unlock()
		}
	}
}

// --- Session methods ---

func (s *Session) readLoop() {
	buf := make([]byte, 4096)
	for {
		n, err := s.ptmx.Read(buf)
		if n > 0 {
			s.mu.Lock()
			s.ring.Write(buf[:n])
			s.lastOutputAt = time.Now()
			if s.status == proto.StatusWaitingInput {
				s.status = proto.StatusRunning
				s.prompt = nil
			}
			s.mu.Unlock()
		}
		if err != nil {
			return
		}
	}
}

func (s *Session) waitChild() {
	err := s.cmd.Wait()
	s.mu.Lock()
	defer s.mu.Unlock()
	s.status = proto.StatusExited
	s.prompt = nil
	if err == nil {
		code := 0
		s.exitCode = &code
	} else {
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) {
			ws, ok := exitErr.Sys().(syscall.WaitStatus)
			if ok {
				if ws.Signaled() {
					s.signal = ws.Signal().String()
				}
				code := ws.ExitStatus()
				s.exitCode = &code
			} else {
				code := 1
				s.exitCode = &code
			}
		} else {
			code := -1
			s.exitCode = &code
		}
	}
	_ = s.ptmx.Close()
	close(s.doneCh)
}

func (s *Session) kill(sig syscall.Signal) error {
	s.mu.Lock()
	if s.status == proto.StatusExited {
		s.mu.Unlock()
		return proto.NewError(proto.EAlreadyExited, "session already exited")
	}
	proc := s.cmd.Process
	s.mu.Unlock()
	if proc == nil {
		return nil
	}
	return proc.Signal(sig)
}

func (s *Session) WriteInput(text string, newline bool) error {
	s.mu.Lock()
	st := s.status
	s.mu.Unlock()
	if st == proto.StatusExited {
		return proto.NewError(proto.EAlreadyExited, "session already exited")
	}
	data := []byte(text)
	if newline {
		data = append(data, '\r')
	}
	_, err := s.ptmx.Write(data)
	if err != nil {
		return proto.NewError(proto.EInternal, err.Error())
	}
	s.mu.Lock()
	s.lastInputAt = time.Now()
	if s.status == proto.StatusWaitingInput {
		s.status = proto.StatusRunning
		s.prompt = nil
	}
	s.mu.Unlock()
	return nil
}

func (s *Session) Resize(cols, rows int) error {
	return pty.Setsize(s.ptmx, &pty.Winsize{Cols: uint16(cols), Rows: uint16(rows)})
}

// Snapshot builds a proto.Snapshot. Callers may pass tailLines>0 to
// override the default screen window.
func (s *Session) Snapshot(tailLines int) proto.Snapshot {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.lastAccessAt = time.Now()

	raw := s.ring.Bytes()
	screen, truncated := renderScreen(raw, tailLines)
	snap := proto.Snapshot{
		SessionID:       s.ID,
		Cmd:             strings.Join(s.FullCmd, " "),
		Status:          s.status,
		Screen:          screen,
		ScreenTruncated: truncated,
		Cursor:          proto.Cursor{Row: 0, Col: 0},
		Prompt:          s.prompt,
		ExitCode:        s.exitCode,
		Signal:          s.signal,
		StartedAt:       s.StartedAt,
		LastActivity:    maxTime(s.lastOutputAt, s.lastInputAt),
	}
	return snap
}

func (s *Session) RawBytes() []byte {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.ring.Bytes()
}

func (s *Session) Done() <-chan struct{} { return s.doneCh }

func (s *Session) Status() proto.Status {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.status
}

func maxTime(a, b time.Time) time.Time {
	if a.After(b) {
		return a
	}
	return b
}

// renderScreen does a placeholder render: strips CR, splits on LF,
// truncates to head 10 + tail 40 if too long. Real virtual terminal is M3.
func renderScreen(raw []byte, tailLines int) ([]string, bool) {
	cleaned := stripANSI(raw)
	cleaned = bytes.ReplaceAll(cleaned, []byte("\r\n"), []byte("\n"))
	cleaned = bytes.ReplaceAll(cleaned, []byte("\r"), []byte("\n"))
	lines := strings.Split(strings.TrimRight(string(cleaned), "\n"), "\n")
	if tailLines > 0 && len(lines) > tailLines {
		return lines[len(lines)-tailLines:], true
	}
	if len(lines) <= screenMaxLines {
		return lines, false
	}
	out := make([]string, 0, 51)
	out = append(out, lines[:10]...)
	out = append(out, "... ["+itoa(len(lines)-50)+" lines truncated] ...")
	out = append(out, lines[len(lines)-40:]...)
	return out, true
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	neg := n < 0
	if neg {
		n = -n
	}
	var buf [20]byte
	i := len(buf)
	for n > 0 {
		i--
		buf[i] = byte('0' + n%10)
		n /= 10
	}
	if neg {
		i--
		buf[i] = '-'
	}
	return string(buf[i:])
}
