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
	URL    string
	Title  string
	Width  uint
	Height uint
	Debug  bool
}

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
