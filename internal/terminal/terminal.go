// Package terminal wraps a virtual terminal emulator (vt10x) so the
// rest of noni sees a stable Feed/Snapshot API.
package terminal

import (
	"strings"
	"sync"

	"github.com/hinshun/vt10x"

	"github.com/williamwa/noni/internal/proto"
)

type Screen struct {
	Lines  []string
	Cursor proto.Cursor
	Cols   int
	Rows   int
}

type Terminal struct {
	mu sync.Mutex
	vt vt10x.Terminal
}

func New(cols, rows int) *Terminal {
	return &Terminal{vt: vt10x.New(vt10x.WithSize(cols, rows))}
}

func (t *Terminal) Feed(p []byte) {
	t.mu.Lock()
	defer t.mu.Unlock()
	_, _ = t.vt.Write(p)
}

func (t *Terminal) Resize(cols, rows int) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.vt.Resize(cols, rows)
}

// Snapshot renders the current grid. Trailing all-blank lines are
// trimmed so the output matches what the user "sees" rather than the
// full grid height.
func (t *Terminal) Snapshot() Screen {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.vt.Lock()
	defer t.vt.Unlock()
	cols, rows := t.vt.Size()
	lines := make([]string, rows)
	for y := 0; y < rows; y++ {
		var b strings.Builder
		b.Grow(cols)
		for x := 0; x < cols; x++ {
			ch := t.vt.Cell(x, y).Char
			if ch == 0 {
				ch = ' '
			}
			b.WriteRune(ch)
		}
		lines[y] = strings.TrimRight(b.String(), " ")
	}
	for len(lines) > 0 && lines[len(lines)-1] == "" {
		lines = lines[:len(lines)-1]
	}
	cur := t.vt.Cursor()
	return Screen{
		Lines:  lines,
		Cursor: proto.Cursor{Row: cur.Y, Col: cur.X},
		Cols:   cols,
		Rows:   rows,
	}
}
