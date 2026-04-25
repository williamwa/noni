package session

import "strings"

// KeyBytes maps a friendly key name to bytes to write to the PTY.
// Returns nil for unknown names (caller should error).
func KeyBytes(name string) []byte {
	switch strings.ToLower(name) {
	case "enter", "return":
		return []byte{'\r'}
	case "tab":
		return []byte{'\t'}
	case "esc", "escape":
		return []byte{0x1b}
	case "backspace":
		return []byte{0x7f}
	case "space":
		return []byte{' '}
	case "up":
		return []byte("\x1b[A")
	case "down":
		return []byte("\x1b[B")
	case "right":
		return []byte("\x1b[C")
	case "left":
		return []byte("\x1b[D")
	case "home":
		return []byte("\x1b[H")
	case "end":
		return []byte("\x1b[F")
	case "pgup":
		return []byte("\x1b[5~")
	case "pgdn":
		return []byte("\x1b[6~")
	case "ctrl-c":
		return []byte{0x03}
	case "ctrl-d":
		return []byte{0x04}
	case "ctrl-z":
		return []byte{0x1a}
	case "ctrl-l":
		return []byte{0x0c}
	case "ctrl-u":
		return []byte{0x15}
	case "ctrl-w":
		return []byte{0x17}
	}
	if len(name) == 2 && (name[0] == 'f' || name[0] == 'F') && name[1] >= '1' && name[1] <= '9' {
		// f1..f9 (rough VT codes)
		switch name[1] {
		case '1':
			return []byte("\x1bOP")
		case '2':
			return []byte("\x1bOQ")
		case '3':
			return []byte("\x1bOR")
		case '4':
			return []byte("\x1bOS")
		case '5':
			return []byte("\x1b[15~")
		case '6':
			return []byte("\x1b[17~")
		case '7':
			return []byte("\x1b[18~")
		case '8':
			return []byte("\x1b[19~")
		case '9':
			return []byte("\x1b[20~")
		}
	}
	return nil
}
