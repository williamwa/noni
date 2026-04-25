//go:build unix

package session

import (
	"os"
	"syscall"
	"unsafe"
)

// ptyEchoOff returns true if the slave-side termios has ECHO cleared,
// which is the canonical signal for password prompts.
func ptyEchoOff(ptmx *os.File) bool {
	var t syscall.Termios
	_, _, errno := syscall.Syscall6(
		syscall.SYS_IOCTL,
		ptmx.Fd(),
		uintptr(ioctlGetTermios),
		uintptr(unsafe.Pointer(&t)),
		0, 0, 0,
	)
	if errno != 0 {
		return false
	}
	return t.Lflag&syscall.ECHO == 0
}
