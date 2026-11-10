//go:build !windows

package app

import (
	"os"
	"os/exec"
	"syscall"
	"time"
)

// spawnDetached starts a process that outlives us.
func spawnDetached(name string, args []string) error {
	cmd := exec.Command(name, args...)
	cmd.Stdout, cmd.Stderr = os.Stdout, os.Stderr
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	return cmd.Start()
}

// spawnHiddenConsole is spawnDetached on non-Windows platforms.
func spawnHiddenConsole(name string, args []string) error { return spawnDetached(name, args) }

// WaitProcessExit waits up to timeout for process pid to exit.
func WaitProcessExit(pid int, timeout time.Duration) bool {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if syscall.Kill(pid, 0) != nil {
			return true
		}
		time.Sleep(100 * time.Millisecond)
	}
	return false
}
