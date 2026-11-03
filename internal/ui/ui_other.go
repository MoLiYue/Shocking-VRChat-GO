//go:build !windows

// Package ui provides the native window (WebView2 on Windows) and helpers.
// On other platforms there is no native window; the system browser is used.
package ui

import (
	"fmt"
	"os"
	"os/exec"
	"runtime"
)

// Options configures the window.
type Options struct {
	URL         string
	Title       string
	Width       uint
	Height      uint
	MinWidth    uint
	MinHeight   uint
	Debug       bool
	StateFile   string
	HideOnClose func() bool
	OnHide      func()
}

// TrayOptions configures the notification-area icon (Windows only).
type TrayOptions struct {
	Tooltip   string
	OpenLabel func() string
	QuitLabel func() string
	OnOpen    func()
	OnQuit    func()
}

// TrayAvailable reports whether a notification-area icon is supported.
func TrayAvailable() bool { return false }

// StartTray is a no-op on this platform.
func StartTray(TrayOptions) {}

// StopTray is a no-op on this platform.
func StopTray() {}

// TrayNotify is a no-op on this platform.
func TrayNotify(string, string) {}

// ShowWindow is unsupported here.
func ShowWindow() bool { return false }

// AllowForeground is a no-op on this platform.
func AllowForeground() {}

// Available reports whether a native window can be created on this platform.
func Available() bool { return false }

// RunWindow is unsupported here and returns false.
func RunWindow(Options) bool { return false }

// Close is a no-op.
func Close() {}

// OpenBrowser opens url in the default browser.
func OpenBrowser(url string) error {
	name := "xdg-open"
	if runtime.GOOS == "darwin" {
		name = "open"
	}
	return exec.Command(name, url).Start()
}

// ErrorBox prints the error to stderr.
func ErrorBox(title, msg string) { fmt.Fprintf(os.Stderr, "%s: %s\n", title, msg) }
