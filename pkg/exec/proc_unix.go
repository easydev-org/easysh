//go:build !windows

package exec

import (
	"os/exec"
	"syscall"
)

// setupProcessGroup puts the child into its own process group, so a cancel can
// take out the whole tree (the command's own children and grandchildren), not
// just the direct child. Without it, `sh -c "a; b"` or `npm run build` leaves
// orphaned processes running after the job is killed.
func setupProcessGroup(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
}

// attachJob / releaseJob are Windows-only (Job Objects); no-ops on unix, where
// the process group set up above is the whole story.
func attachJob(cmd *exec.Cmd) {}
func releaseJob(pid int)      {}

// killTree kills the process group of pid. A negative pid targets the group
// (which equals pid because of Setpgid). Best-effort: a dead group returns
// ESRCH, which we ignore.
func killTree(pid int) {
	if pid <= 0 {
		return
	}
	_ = syscall.Kill(-pid, syscall.SIGKILL)
	_ = syscall.Kill(pid, syscall.SIGKILL)
}

// prepareExec is the identity on unix: exec runs the program directly.
func prepareExec(name string, args []string) (string, []string) { return name, args }

// killAllTree kills every process group a runner has started (used on shutdown).
func killAllTree(pids []int) {
	for _, p := range pids {
		killTree(p)
	}
}
