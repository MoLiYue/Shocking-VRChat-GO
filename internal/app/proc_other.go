//go:build !windows

package app

import (
	"os"
	"os/exec"
	"syscall"
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
