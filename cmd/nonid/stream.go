package main

import (
	"encoding/base64"
	"time"

	"github.com/williamwa/noni/internal/proto"
	"github.com/williamwa/noni/internal/session"
)

// streamHandle implements ipc.Streamer. It pumps PTY chunks plus
// status/prompt transitions from a Session to the wire, and ends with
// a terminal frame when the session exits.
type streamHandle struct {
	s           *session.Session
	skipBacklog bool
}

func (h *streamHandle) Stream(send func(any) error) error {
	initial, ch, cancel := h.s.Subscribe(h.skipBacklog)
	defer cancel()

	if len(initial) > 0 {
		if err := send(proto.StreamFrame{
			Kind:   "initial",
			Bytes:  base64.StdEncoding.EncodeToString(initial),
			Status: h.s.Status(),
		}); err != nil {
			return err
		}
	}

	lastStatus := h.s.Status()
	stateTicker := time.NewTicker(150 * time.Millisecond)
	defer stateTicker.Stop()

	flushEnd := func() error {
		snap := h.s.Snapshot(0)
		return send(proto.StreamFrame{
			Kind:     "end",
			Status:   snap.Status,
			ExitCode: snap.ExitCode,
			Signal:   snap.Signal,
		})
	}

	for {
		select {
		case data, ok := <-ch:
			if !ok {
				// Session ended; drain any leftover state and emit end.
				return flushEnd()
			}
			if err := send(proto.StreamFrame{
				Kind:   "chunk",
				Bytes:  base64.StdEncoding.EncodeToString(data),
				Status: h.s.Status(),
			}); err != nil {
				return err
			}
		case <-stateTicker.C:
			snap := h.s.Snapshot(0)
			if snap.Status != lastStatus {
				lastStatus = snap.Status
				if err := send(proto.StreamFrame{
					Kind:   "state",
					Status: snap.Status,
					Prompt: snap.Prompt,
				}); err != nil {
					return err
				}
			}
			if snap.Status == proto.StatusExited {
				return flushEnd()
			}
		}
	}
}
