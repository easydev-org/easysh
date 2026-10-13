package main

import (
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"time"
)

// ensureDaemon connects to the daemon, starting one if none is reachable.
//
// The daemon is what makes async detach work: a foreground job past the
// threshold keeps running after the client gets its prompt back, and its output
// keeps being captured — which is impossible if the shell exits with the client.
// So rather than make the user remember to start `easyshd`, the client starts it
// on demand (the same way docker, jupyter, and language servers do).
func ensureDaemon(network, addr string) (*Client, error) {
	if c, err := Dial(network, addr); err == nil {
		return c, nil
	}
	if network != "unix" {
		return nil, fmt.Errorf("no daemon at %s (auto-start only works for unix sockets)", addr)
	}
	if err := spawnDaemon(addr); err != nil {
		return nil, err
	}
	// Wait for the socket to accept.
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if c, err := Dial(network, addr); err == nil {
			return c, nil
		}
		time.Sleep(20 * time.Millisecond)
	}
	return nil, fmt.Errorf("started easyshd but could not connect at %s", addr)
}

// spawnDaemon starts easyshd detached from this process.
func spawnDaemon(sock string) error {
	exe, err := findDaemon()
	if err != nil {
		return err
	}
	cmd := exec.Command(exe, "-socket", sock)
	cmd.Stderr = nil
	cmd.Stdout = nil
	cmd.Stdin = nil
	// Detach: new session, so the daemon outlives this client.
	cmd.SysProcAttr = detachAttrs()
	if err := cmd.Start(); err != nil {
		return fmt.Errorf("spawn %s: %w", exe, err)
	}
	// Release our handle; the daemon keeps running.
	_ = cmd.Process.Release()
	return nil
}

// findDaemon looks for easyshd next to this executable, then on PATH.
func findDaemon() (string, error) {
	if self, err := os.Executable(); err == nil {
		cand := filepath.Join(filepath.Dir(self), "easyshd")
		if st, err := os.Stat(cand); err == nil && !st.IsDir() {
			return cand, nil
		}
	}
	if p, err := exec.LookPath("easyshd"); err == nil {
		return p, nil
	}
	return "", fmt.Errorf("easyshd not found (looked next to this binary and on PATH)")
}

// dialTimeout is a small helper kept for future use.
func dialTimeout(network, addr string, d time.Duration) (net.Conn, error) {
	return net.DialTimeout(network, addr, d)
}
