package session

import "bytes"

// stripANSI removes common ANSI escape sequences. This is a stop-gap
// for M1; M3 replaces it with a real virtual terminal.
func stripANSI(in []byte) []byte {
	var out bytes.Buffer
	out.Grow(len(in))
	for i := 0; i < len(in); i++ {
		b := in[i]
		if b == 0x1b && i+1 < len(in) {
			next := in[i+1]
			switch next {
			case '[':
				// CSI: ESC [ ... final byte in 0x40-0x7E
				j := i + 2
				for j < len(in) {
					c := in[j]
					if c >= 0x40 && c <= 0x7e {
						break
					}
					j++
				}
				i = j
				continue
			case ']':
				// OSC: ESC ] ... BEL or ESC \
				j := i + 2
				for j < len(in) {
					if in[j] == 0x07 {
						break
					}
					if in[j] == 0x1b && j+1 < len(in) && in[j+1] == '\\' {
						j++
						break
					}
					j++
				}
				i = j
				continue
			default:
				// Two-byte ESC sequences (e.g. ESC =, ESC >)
				i++
				continue
			}
		}
		if b == 0x07 || b == 0x08 {
			continue
		}
		out.WriteByte(b)
	}
	return out.Bytes()
}
