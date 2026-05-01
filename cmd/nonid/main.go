package main

import (
	"encoding/base64"
	"encoding/json"
	"log"
	"net"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"

	"github.com/williamwa/noni/internal/ipc"
	"github.com/williamwa/noni/internal/proto"
	"github.com/williamwa/noni/internal/session"
)

var Version = "0.1.1-dev"

func main() {
	socketPath := SocketPath()
	if err := os.MkdirAll(filepath.Dir(socketPath), 0o700); err != nil {
		log.Fatalf("mkdir socket dir: %v", err)
	}
	if _, err := os.Stat(socketPath); err == nil {
		if c, dErr := net.Dial("unix", socketPath); dErr == nil {
			c.Close()
			log.Fatalf("daemon already running at %s", socketPath)
		}
		_ = os.Remove(socketPath)
	}

	logPath := filepath.Join(homeDir(), ".noni", "log")
	_ = os.MkdirAll(filepath.Dir(logPath), 0o700)
	if f, err := os.OpenFile(logPath, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600); err == nil {
		log.SetOutput(f)
	}
	log.Printf("nonid %s starting at %s", Version, socketPath)

	ln, err := net.Listen("unix", socketPath)
	if err != nil {
		log.Fatalf("listen: %v", err)
	}
	if err := os.Chmod(socketPath, 0o600); err != nil {
		log.Printf("chmod socket: %v", err)
	}

	mgr := session.NewManager()
	startedAt := time.Now()
	h := newHandler(mgr, startedAt)
	srv := ipc.NewServer(ln, h.Dispatch)

	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGTERM, syscall.SIGINT)
	go func() {
		<-sigCh
		log.Printf("nonid: shutting down")
		_ = srv.Close()
		mgr.Stop()
		_ = os.Remove(socketPath)
		os.Exit(0)
	}()

	if err := srv.Serve(); err != nil {
		log.Printf("serve: %v", err)
	}
}

func homeDir() string {
	if h, err := os.UserHomeDir(); err == nil {
		return h
	}
	return os.Getenv("HOME")
}

func SocketPath() string {
	if p := os.Getenv("NONI_SOCKET"); p != "" {
		return p
	}
	if r := os.Getenv("XDG_RUNTIME_DIR"); r != "" {
		return filepath.Join(r, "noni", "sock")
	}
	return filepath.Join(homeDir(), ".noni", "sock")
}

// --- handler ---

type handler struct {
	mgr       *session.Manager
	startedAt time.Time
}

func newHandler(mgr *session.Manager, startedAt time.Time) *handler {
	return &handler{mgr: mgr, startedAt: startedAt}
}

func (h *handler) Dispatch(method string, params json.RawMessage) (any, error) {
	switch method {
	case "Run":
		var req proto.RunReq
		if err := unmarshal(params, &req); err != nil {
			return nil, err
		}
		s, err := h.mgr.Run(req)
		if err != nil {
			return nil, err
		}
		settle(s, req.WaitMs, 500)
		return s.Snapshot(0), nil

	case "Status":
		var req proto.IDReq
		if err := unmarshal(params, &req); err != nil {
			return nil, err
		}
		s, err := h.mgr.Get(req.SessionID)
		if err != nil {
			return nil, err
		}
		return s.Snapshot(0), nil

	case "Input":
		var req proto.InputReq
		if err := unmarshal(params, &req); err != nil {
			return nil, err
		}
		s, err := h.mgr.Get(req.SessionID)
		if err != nil {
			return nil, err
		}
		if !canAcceptInput(s) {
			return nil, proto.NewError(proto.ENotWaiting, "session not waiting for input")
		}
		if err := s.WriteInput(req.Text, req.Newline); err != nil {
			return nil, err
		}
		settle(s, 0, 300)
		return s.Snapshot(0), nil

	case "Key":
		var req proto.KeyReq
		if err := unmarshal(params, &req); err != nil {
			return nil, err
		}
		s, err := h.mgr.Get(req.SessionID)
		if err != nil {
			return nil, err
		}
		if !canAcceptInput(s) {
			return nil, proto.NewError(proto.ENotWaiting, "session not waiting for input")
		}
		var data []byte
		for _, k := range req.Keys {
			b := session.KeyBytes(k)
			if b == nil {
				return nil, proto.NewError(proto.EBadRequest, "unknown key: "+k)
			}
			data = append(data, b...)
		}
		if err := s.WriteRaw(data); err != nil {
			return nil, err
		}
		settle(s, 0, 300)
		return s.Snapshot(0), nil

	case "Secret":
		var req proto.SecretReq
		if err := unmarshal(params, &req); err != nil {
			return nil, err
		}
		if req.EnvVar == "" {
			return nil, proto.NewError(proto.EBadRequest, "env_var required")
		}
		secret, ok := os.LookupEnv(req.EnvVar)
		if !ok {
			return nil, proto.NewError(proto.EBadRequest, "env var not set in daemon: "+req.EnvVar)
		}
		s, err := h.mgr.Get(req.SessionID)
		if err != nil {
			return nil, err
		}
		if !canAcceptInput(s) {
			return nil, proto.NewError(proto.ENotWaiting, "session not waiting for input")
		}
		if err := s.WriteInput(secret, req.Newline); err != nil {
			return nil, err
		}
		settle(s, 0, 300)
		return s.Snapshot(0), nil

	case "Read":
		var req proto.ReadReq
		if err := unmarshal(params, &req); err != nil {
			return nil, err
		}
		s, err := h.mgr.Get(req.SessionID)
		if err != nil {
			return nil, err
		}
		snap := s.Snapshot(req.TailLines)
		resp := proto.ReadResp{Snapshot: snap}
		if req.Raw {
			resp.RawBytes = base64.StdEncoding.EncodeToString(s.RawBytes())
		}
		return resp, nil

	case "Wait":
		var req proto.WaitReq
		if err := unmarshal(params, &req); err != nil {
			return nil, err
		}
		s, err := h.mgr.Get(req.SessionID)
		if err != nil {
			return nil, err
		}
		timeout := time.Duration(req.TimeoutMs) * time.Millisecond
		if timeout <= 0 {
			timeout = 10 * time.Second
		}
		until := req.Until
		if until == "" {
			until = "state_change"
		}
		deadline := time.Now().Add(timeout)
		startVer := s.Version()
		startStatus := s.Status()
		for {
			st := s.Status()
			done := false
			switch until {
			case "exit":
				done = st == proto.StatusExited
			case "state_change":
				done = st != startStatus
			case "prompt":
				done = st == proto.StatusWaitingInput || st == proto.StatusExited
			case "idle":
				done = st == proto.StatusWaitingInput || st == proto.StatusExited
			default:
				return nil, proto.NewError(proto.EBadRequest, "unknown until: "+until)
			}
			if done {
				return s.Snapshot(0), nil
			}
			if !s.WaitChange(startVer, deadline) {
				return nil, proto.NewError(proto.ETimeout, "wait timed out")
			}
			startVer = s.Version()
		}

	case "List":
		out := make([]proto.Snapshot, 0)
		for _, s := range h.mgr.List() {
			out = append(out, s.Snapshot(5))
		}
		return proto.ListResp{Sessions: out}, nil

	case "Kill":
		var req proto.KillReq
		if err := unmarshal(params, &req); err != nil {
			return nil, err
		}
		if err := h.mgr.Kill(req.SessionID, req.Signal); err != nil {
			return nil, err
		}
		return proto.OKResp{OK: true, SessionID: req.SessionID}, nil

	case "Resize":
		var req proto.ResizeReq
		if err := unmarshal(params, &req); err != nil {
			return nil, err
		}
		s, err := h.mgr.Get(req.SessionID)
		if err != nil {
			return nil, err
		}
		if err := s.Resize(req.Cols, req.Rows); err != nil {
			return nil, proto.NewError(proto.EInternal, err.Error())
		}
		return s.Snapshot(0), nil

	case "Stream":
		var req proto.StreamReq
		if err := unmarshal(params, &req); err != nil {
			return nil, err
		}
		s, err := h.mgr.Get(req.SessionID)
		if err != nil {
			return nil, err
		}
		return &streamHandle{s: s, skipBacklog: req.SkipBacklog}, nil

	case "Ping":
		return proto.PingResp{Version: Version, UptimeS: int64(time.Since(h.startedAt).Seconds())}, nil

	default:
		return nil, proto.NewError(proto.EBadRequest, "unknown method: "+method)
	}
}

func canAcceptInput(s *session.Session) bool {
	st := s.Status()
	return st == proto.StatusRunning || st == proto.StatusWaitingInput || st == proto.StatusStalled
}

// settle waits for either an exit or a state change to waiting_input
// after a Run/Input/Key/Secret call. waitMs<=0 uses the default.
func settle(s *session.Session, waitMs, defaultMs int) {
	d := time.Duration(waitMs) * time.Millisecond
	if waitMs <= 0 {
		d = time.Duration(defaultMs) * time.Millisecond
	}
	deadline := time.Now().Add(d)
	startVer := s.Version()
	for {
		st := s.Status()
		if st == proto.StatusExited || st == proto.StatusWaitingInput {
			return
		}
		if !s.WaitChange(startVer, deadline) {
			return
		}
		startVer = s.Version()
	}
}

func unmarshal(raw json.RawMessage, v any) error {
	if len(raw) == 0 {
		return nil
	}
	if err := json.Unmarshal(raw, v); err != nil {
		return proto.NewError(proto.EBadRequest, err.Error())
	}
	return nil
}
