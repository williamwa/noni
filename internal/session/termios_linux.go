//go:build linux

package session

import "syscall"

const ioctlGetTermios = syscall.TCGETS
