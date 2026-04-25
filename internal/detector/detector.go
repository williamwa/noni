// Package detector turns a stable screen into a structured Prompt.
//
// M2 ships a stub: it always returns nil, so sessions that go idle for
// idle_threshold_unknown end up tagged as PromptUnknown. M3 replaces this
// with the real regex/termios stack.
package detector

import "github.com/williamwang/noni/internal/proto"

type Input struct {
	Screen  []string
	Cursor  proto.Cursor
	EchoOff bool
}

type Detector interface {
	Detect(in Input) *proto.Prompt
}

type Stub struct{}

func (Stub) Detect(in Input) *proto.Prompt {
	if in.EchoOff {
		return &proto.Prompt{Type: proto.PromptPassword, Echo: false, Confidence: 0.99, Question: lastNonEmpty(in.Screen)}
	}
	return nil
}

func lastNonEmpty(lines []string) string {
	for i := len(lines) - 1; i >= 0; i-- {
		if lines[i] != "" {
			return lines[i]
		}
	}
	return ""
}
