//go:build !windows

package main

import "syscall"

// detachAttrs makes the spawned daemon a new session leader with no controlling
// terminal, so it survives the client's exit.
func detachAttrs() *syscall.SysProcAttr {
	return &syscall.SysProcAttr{Setsid: true}
}
