//go:build !windows

package exec

import (
	"os"
	"strconv"
	"strings"
	"syscall"
	"testing"
)

func readPID(t *testing.T, path string) int {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		return 0
	}
	n, _ := strconv.Atoi(strings.TrimSpace(string(b)))
	return n
}

// processAlive reports whether pid exists (signal 0 probes without killing).
func processAlive(pid int) bool {
	if pid <= 0 {
		return false
	}
	return syscall.Kill(pid, 0) == nil
}
