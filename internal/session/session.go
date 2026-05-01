// Package session manages PTY-backed child processes.
package session

import (
	"bytes"
	"crypto/rand"
	"errors"
	"fmt"
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

	"github.com/williamwa/noni/internal/detector"
	"github.com/williamwa/noni/internal/proto"
	"github.com/williamwa/noni/internal/terminal"
)

const (
	defaultCols    = 120
	defaultRows    = 40
	ringBufferSize = 256 * 1024
	screenMaxLines = 50

	idleThreshold        = 300 * time.Millisecond
	idleThresholdUnknown = 1000 * time.Millisecond
	idleTick             = 100 * time.Millisecond
)

type Session struct {
	ID        string
	Cmd       string
	FullCmd   []string
	StartedAt time.Time

	mu           sync.Mutex
	cond         *sync.Cond
	status       proto.Status
	version      uint64 // bumped on any state-relevant change; for Wait
	exitCode     *int
	signal       string
	lastOutputAt time.Time
	lastInputAt  time.Time
	lastAccessAt time.Time
	prompt       *proto.Prompt

	cmd      *exec.Cmd
	ptmx     *os.File
	ring     *ringBuffer
	term     *terminal.Terminal
	detector detector.Detector
	subs     []*subscriber

	dsrCarry []byte // trailing bytes of an unfinished CSI from last read

	doneCh chan struct{} // closed when reaped
	stopCh chan struct{} // signals ticker to exit
}

type Manager struct {
	mu       sync.RWMutex
	sessions map[string]*Session
	entropy  io.Reader
	det      detector.Detector
	stopCh   chan struct{}
}

func NewManager() *Manager {
	m := &Manager{
		sessions: make(map[string]*Session),
		entropy:  rand.Reader,
		det:      detector.Default(),
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
		term:         terminal.New(cols, rows),
		detector:     m.det,
		doneCh:       make(chan struct{}),
		stopCh:       make(chan struct{}),
	}
	s.cond = sync.NewCond(&s.mu)

	m.mu.Lock()
	m.sessions[s.ID] = s
	m.mu.Unlock()

	go s.readLoop()
	go s.waitChild()
	go s.tickLoop()

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
			data := append([]byte(nil), buf[:n]...)
			s.term.Feed(data)
			s.replyDSR(data)
			s.mu.Lock()
			s.ring.Write(data)
			s.lastOutputAt = time.Now()
			if s.status == proto.StatusWaitingInput {
				s.status = proto.StatusRunning
				s.prompt = nil
			}
			subs := append([]*subscriber(nil), s.subs...)
			s.bumpLocked()
			s.mu.Unlock()
			for _, sub := range subs {
				select {
				case sub.ch <- data:
				default: // slow consumer — drop
				}
			}
		}
		if err != nil {
			return
		}
	}
}

// replyDSR scans the child's output for Device Status Report queries
// (CSI 5 n / CSI 6 n) and writes the expected reply back to the PTY.
// Many CLIs (gh, glab, …) probe terminal size by emitting CSI 6 n and
// blocking on stdin until the reply arrives — vt10x doesn't surface
// these, so we answer them here. dsrCarry preserves a partial sequence
// that straddles read boundaries.
func (s *Session) replyDSR(data []byte) {
	scan := data
	if len(s.dsrCarry) > 0 {
		scan = append(s.dsrCarry, data...)
	}
	i := 0
	for i < len(scan) {
		j := bytes.IndexByte(scan[i:], 0x1b)
		if j < 0 {
			i = len(scan)
			break
		}
		idx := i + j
		// Need at least ESC [ <param> n — 4 bytes.
		if idx+3 >= len(scan) {
			i = idx
			break
		}
		if scan[idx+1] != '[' {
			i = idx + 1
			continue
		}
		// Plain DSR: ESC [ 5 n  or  ESC [ 6 n  (no '?' private marker).
		if scan[idx+3] == 'n' {
			switch scan[idx+2] {
			case '6':
				row, col, _, _ := s.term.CursorAndSize()
				resp := fmt.Sprintf("\x1b[%d;%dR", row+1, col+1)
				_, _ = s.ptmx.Write([]byte(resp))
			case '5':
				_, _ = s.ptmx.Write([]byte("\x1b[0n"))
			}
			i = idx + 4
			continue
		}
		i = idx + 1
	}
	if i < len(scan) {
		// Keep the dangling tail (at most a few bytes) for next read.
		tail := scan[i:]
		if len(tail) > 8 {
			tail = tail[len(tail)-8:]
		}
		s.dsrCarry = append(s.dsrCarry[:0], tail...)
	} else {
		s.dsrCarry = s.dsrCarry[:0]
	}
}

func (s *Session) waitChild() {
	err := s.cmd.Wait()
	s.mu.Lock()
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
	subs := s.subs
	s.subs = nil
	s.bumpLocked()
	s.mu.Unlock()
	for _, sub := range subs {
		close(sub.ch)
	}
	close(s.doneCh)
	close(s.stopCh)
}

type subscriber struct {
	ch chan []byte
}

// Subscribe returns the bytes already received plus a channel of future
// chunks. cancel removes the subscription. The channel is closed when
// the session exits.
func (s *Session) Subscribe(skipBacklog bool) (initial []byte, ch <-chan []byte, cancel func()) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if !skipBacklog {
		initial = s.ring.Bytes()
	}
	if s.status == proto.StatusExited {
		// Closed channel so the caller's range exits immediately.
		closed := make(chan []byte)
		close(closed)
		return initial, closed, func() {}
	}
	sub := &subscriber{ch: make(chan []byte, 64)}
	s.subs = append(s.subs, sub)
	cancel = func() {
		s.mu.Lock()
		defer s.mu.Unlock()
		for i, x := range s.subs {
			if x == sub {
				s.subs = append(s.subs[:i], s.subs[i+1:]...)
				close(sub.ch)
				return
			}
		}
	}
	return initial, sub.ch, cancel
}

// tickLoop drives stable-state detection. Every idleTick it checks
// whether the session has been idle long enough to be flipped from
// running to waiting_input.
func (s *Session) tickLoop() {
	t := time.NewTicker(idleTick)
	defer t.Stop()
	for {
		select {
		case <-s.stopCh:
			return
		case <-t.C:
		}
		s.evaluate()
	}
}

func (s *Session) evaluate() {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.status != proto.StatusRunning {
		return
	}
	idle := time.Since(maxTime(s.lastOutputAt, s.lastInputAt))
	if idle < idleThreshold {
		return
	}
	scr := s.term.Snapshot()
	echoOff, canonOff := ptyTermios(s.ptmx)
	in := detector.Input{Screen: scr.Lines, Cursor: scr.Cursor, EchoOff: echoOff, CanonOff: canonOff}
	if s.detector != nil {
		if p := s.detector.Detect(in); p != nil {
			s.status = proto.StatusWaitingInput
			s.prompt = p
			s.bumpLocked()
			return
		}
	}
	if idle >= idleThresholdUnknown {
		s.status = proto.StatusWaitingInput
		s.prompt = &proto.Prompt{
			Type:       proto.PromptUnknown,
			Echo:       true,
			Confidence: 0.0,
			Question:   lastLine(scr.Lines),
		}
		s.bumpLocked()
	}
}

func lastLine(lines []string) string {
	for i := len(lines) - 1; i >= 0; i-- {
		if strings.TrimSpace(lines[i]) != "" {
			return lines[i]
		}
	}
	return ""
}

func (s *Session) bumpLocked() {
	s.version++
	s.cond.Broadcast()
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
	s.bumpLocked()
	s.mu.Unlock()
	return nil
}

func (s *Session) WriteRaw(data []byte) error {
	s.mu.Lock()
	st := s.status
	s.mu.Unlock()
	if st == proto.StatusExited {
		return proto.NewError(proto.EAlreadyExited, "session already exited")
	}
	if _, err := s.ptmx.Write(data); err != nil {
		return proto.NewError(proto.EInternal, err.Error())
	}
	s.mu.Lock()
	s.lastInputAt = time.Now()
	if s.status == proto.StatusWaitingInput {
		s.status = proto.StatusRunning
		s.prompt = nil
	}
	s.bumpLocked()
	s.mu.Unlock()
	return nil
}

func (s *Session) Resize(cols, rows int) error {
	if err := pty.Setsize(s.ptmx, &pty.Winsize{Cols: uint16(cols), Rows: uint16(rows)}); err != nil {
		return err
	}
	s.term.Resize(cols, rows)
	return nil
}

// WaitChange blocks until the session's version exceeds startVer or
// deadline passes. Returns true if a change happened.
func (s *Session) WaitChange(startVer uint64, deadline time.Time) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	for s.version == startVer {
		now := time.Now()
		if !now.Before(deadline) {
			return false
		}
		// sync.Cond has no timed wait; spawn a timer goroutine to broadcast.
		timer := time.AfterFunc(deadline.Sub(now), func() {
			s.mu.Lock()
			s.cond.Broadcast()
			s.mu.Unlock()
		})
		s.cond.Wait()
		timer.Stop()
	}
	return true
}

func (s *Session) Version() uint64 {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.version
}

func (s *Session) Snapshot(tailLines int) proto.Snapshot {
	scr := s.term.Snapshot()
	s.mu.Lock()
	defer s.mu.Unlock()
	s.lastAccessAt = time.Now()

	lines := scr.Lines
	truncated := false
	if tailLines > 0 && len(lines) > tailLines {
		lines = lines[len(lines)-tailLines:]
		truncated = true
	} else if len(lines) > screenMaxLines {
		out := make([]string, 0, 51)
		out = append(out, lines[:10]...)
		out = append(out, "... ["+itoa(len(lines)-50)+" lines truncated] ...")
		out = append(out, lines[len(lines)-40:]...)
		lines = out
		truncated = true
	}
	return proto.Snapshot{
		SessionID:       s.ID,
		Cmd:             strings.Join(s.FullCmd, " "),
		Status:          s.status,
		Screen:          lines,
		ScreenTruncated: truncated,
		Cursor:          scr.Cursor,
		Prompt:          s.prompt,
		ExitCode:        s.exitCode,
		Signal:          s.signal,
		StartedAt:       s.StartedAt,
		LastActivity:    maxTime(s.lastOutputAt, s.lastInputAt),
	}
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
