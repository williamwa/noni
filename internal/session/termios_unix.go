//go:build unix

package session

import (
	"os"
	"syscall"
	"unsafe"
)

// ptyTermios reports the slave-side ECHO and ICANON flags.
//
//   - ECHO off + ICANON on  → password prompt (line-buffered, hidden input)
//   - ECHO off + ICANON off → TUI in raw / cbreak mode (arrow-key menus etc.);
//     the screen content alone decides what kind of prompt this is.
func ptyTermios(ptmx *os.File) (echoOff, canonOff bool) {
	var t syscall.Termios
	_, _, errno := syscall.Syscall6(
		syscall.SYS_IOCTL,
		ptmx.Fd(),
		uintptr(ioctlGetTermios),
		uintptr(unsafe.Pointer(&t)),
		0, 0, 0,
	)
	if errno != 0 {
		return false, false
	}
	return t.Lflag&syscall.ECHO == 0, t.Lflag&syscall.ICANON == 0
}

func ptyEchoOff(ptmx *os.File) bool {
	echo, _ := ptyTermios(ptmx)
	return echo
}
