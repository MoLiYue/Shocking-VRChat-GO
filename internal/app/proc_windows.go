//go:build windows

package app

import (
	"os/exec"
	"syscall"
)

const (
	detachedProcess       = 0x00000008
	createNewProcessGroup = 0x00000200
	createNoWindow        = 0x08000000
)

// spawnDetached starts a GUI process (our own exe) that outlives us.
func spawnDetached(name string, args []string) error {
	cmd := exec.Command(name, args...)
	cmd.SysProcAttr = &syscall.SysProcAttr{
		CreationFlags: detachedProcess | createNewProcessGroup,
		HideWindow:    true,
	}
	return cmd.Start()
}

// spawnHiddenConsole starts a console program (cmd.exe running the update
// script) with a hidden console that its children share.
//
// DETACHED_PROCESS must not be used here: a detached cmd.exe has no
// console, so every console tool it runs (tasklist, find, robocopy...)
// allocates a new, visible console window — a window popping up every
// second — and `timeout` fails instantly without a console, turning the
// wait loop into a busy loop that gives up before our process has exited.
func spawnHiddenConsole(name string, args []string) error {
	cmd := exec.Command(name, args...)
	cmd.SysProcAttr = &syscall.SysProcAttr{
		CreationFlags: createNoWindow | createNewProcessGroup,
		HideWindow:    true,
	}
	return cmd.Start()
}
