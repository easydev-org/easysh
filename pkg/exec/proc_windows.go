//go:build windows

package exec

import (
	"os/exec"
	"strings"
	"sync"
	"syscall"
	"unsafe"

	"golang.org/x/sys/windows"
)

// Windows has no process-group semantics, so tree-kill comes from a Job Object:
// each command is assigned to its own job with KILL_ON_JOB_CLOSE, and killing
// the job reaps the whole tree. syscall is kept for parity with proc_unix.go.
var _ = syscall.SIGKILL

var (
	jobMu    sync.Mutex
	jobByPID = map[int]windows.Handle{}
)

// setupProcessGroup is a no-op on Windows; isolation comes from the Job Object
// attached right after the process starts (attachJob).
func setupProcessGroup(cmd *exec.Cmd) {}

// attachJob creates a Job Object for the started process (KILL_ON_JOB_CLOSE) and
// assigns it, so killTree can terminate the whole tree. Best-effort: on failure
// killTree falls back to taskkill /T /F.
func attachJob(cmd *exec.Cmd) {
	if cmd.Process == nil {
		return
	}
	pid := cmd.Process.Pid
	job, err := windows.CreateJobObject(nil, nil)
	if err != nil {
		return
	}
	var info windows.JOBOBJECT_EXTENDED_LIMIT_INFORMATION
	info.BasicLimitInformation.LimitFlags = windows.JOB_OBJECT_LIMIT_KILL_ON_JOB_CLOSE
	if _, err := windows.SetInformationJobObject(job,
		windows.JobObjectExtendedLimitInformation,
		uintptr(unsafe.Pointer(&info)), uint32(unsafe.Sizeof(info))); err != nil {
		windows.CloseHandle(job)
		return
	}
	p, err := windows.OpenProcess(windows.PROCESS_SET_QUOTA|windows.PROCESS_TERMINATE, false, uint32(pid))
	if err != nil {
		windows.CloseHandle(job)
		return
	}
	defer windows.CloseHandle(p)
	if err := windows.AssignProcessToJobObject(job, p); err != nil {
		windows.CloseHandle(job)
		return
	}
	jobMu.Lock()
	jobByPID[pid] = job
	jobMu.Unlock()
}

// releaseJob closes the Job Object after a natural exit (KILL_ON_JOB_CLOSE has
// already reaped stragglers).
func releaseJob(pid int) {
	jobMu.Lock()
	h, ok := jobByPID[pid]
	delete(jobByPID, pid)
	jobMu.Unlock()
	if ok {
		windows.CloseHandle(h)
	}
}

// killTree terminates the whole tree: Job Object first, taskkill /T /F fallback.
func killTree(pid int) {
	if pid <= 0 {
		return
	}
	jobMu.Lock()
	h, ok := jobByPID[pid]
	jobMu.Unlock()
	if ok {
		if err := windows.TerminateJobObject(h, 1); err == nil {
			return
		}
	}
	_ = exec.Command("taskkill", "/T", "/F", "/PID", itoa(pid)).Run()
}

// killAllTree kills every tracked job (used on shutdown).
func killAllTree(pids []int) {
	for _, p := range pids {
		killTree(p)
	}
}

// prepareExec routes batch wrappers through cmd /c: exec.Command cannot run
// .cmd/.bat directly on Windows (they are not PE images). npm/pnpm and friends
// are batch wrappers, so this path is common.
func prepareExec(name string, args []string) (string, []string) {
	lower := strings.ToLower(name)
	if strings.HasSuffix(lower, ".cmd") || strings.HasSuffix(lower, ".bat") {
		return "cmd.exe", append([]string{"/c", name}, args...)
	}
	return name, args
}

func itoa(i int) string {
	if i == 0 {
		return "0"
	}
	neg := i < 0
	if neg {
		i = -i
	}
	var b [20]byte
	pos := len(b)
	for i > 0 {
		pos--
		b[pos] = byte('0' + i%10)
		i /= 10
	}
	if neg {
		pos--
		b[pos] = '-'
	}
	return string(b[pos:])
}
