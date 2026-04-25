package session

// ringBuffer is a fixed-size byte ring; oldest bytes drop off when full.
type ringBuffer struct {
	buf  []byte
	size int
	w    int
	full bool
}

func newRingBuffer(size int) *ringBuffer {
	return &ringBuffer{buf: make([]byte, size), size: size}
}

func (r *ringBuffer) Write(p []byte) (int, error) {
	for _, b := range p {
		r.buf[r.w] = b
		r.w++
		if r.w == r.size {
			r.w = 0
			r.full = true
		}
	}
	return len(p), nil
}

// Bytes returns a copy of the buffer contents in chronological order.
func (r *ringBuffer) Bytes() []byte {
	if !r.full {
		out := make([]byte, r.w)
		copy(out, r.buf[:r.w])
		return out
	}
	out := make([]byte, r.size)
	copy(out, r.buf[r.w:])
	copy(out[r.size-r.w:], r.buf[:r.w])
	return out
}
