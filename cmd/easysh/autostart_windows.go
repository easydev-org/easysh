//go:build windows

package main

import "syscall"

// detachAttrs on Windows: create a new process group so the daemon is not tied
// to the client's console.
func detachAttrs() *syscall.SysProcAttr {
	return &syscall.SysProcAttr{CreationFlags: syscall.CREATE_NEW_PROCESS_GROUP}
}
