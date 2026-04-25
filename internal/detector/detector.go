// Package detector turns a stable screen into a structured Prompt.
package detector

import "github.com/williamwa/noni/internal/proto"

type Input struct {
	Screen  []string
	Cursor  proto.Cursor
	EchoOff bool
}

type Detector interface {
	Detect(in Input) *proto.Prompt
}

// Default returns the production detector stack.
func Default() Detector { return Rules{} }
