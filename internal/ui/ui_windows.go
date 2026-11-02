//go:build windows

// Package ui provides the native window (WebView2 on Windows) and helpers.
package ui

import (
	"os"
	"os/exec"
	"path/filepath"
	"sync"
	"syscall"
	"unsafe"

	webview2 "github.com/jchv/go-webview2"
)

var (
	mu      sync.Mutex
	current webview2.WebView
	closed  bool
)

// IconResourceID is the icon ordinal embedded by go-winres (see winres/).
const IconResourceID = 1

// Options configures the window.
type Options struct {
	URL    string
	Title  string
	Width  uint
	Height uint
	Debug  bool
}

// Available reports whether a native window can be created on this platform.
func Available() bool { return true }

func dataDir() string {
	base := os.Getenv("LOCALAPPDATA")
	if base == "" {
		base = os.TempDir()
	}
	return filepath.Join(base, "ShockingVRChat", "WebView2")
}

// RunWindow opens the WebView2 window and blocks until it is closed.
// Returns false immediately if WebView2 is unavailable (runtime missing).
// Must be called from the main goroutine (locked OS thread).
func RunWindow(o Options) bool {
	_ = os.MkdirAll(dataDir(), 0o755)
	w := webview2.NewWithOptions(webview2.WebViewOptions{
		Debug:     o.Debug,
		AutoFocus: true,
		DataPath:  dataDir(),
		WindowOptions: webview2.WindowOptions{
			Title:  o.Title,
			Width:  o.Width,
			Height: o.Height,
			IconId: IconResourceID,
			Center: true,
		},
	})
	if w == nil {
		return false
	}
	mu.Lock()
	if closed {
		mu.Unlock()
		w.Destroy()
		return true
	}
	current = w
	mu.Unlock()
	w.SetSize(int(o.Width), int(o.Height), webview2.HintNone)
	w.Navigate(o.URL)
	w.Run()
	mu.Lock()
	current = nil
	closed = true
	mu.Unlock()
	w.Destroy()
	return true
}

// Close terminates the window from any goroutine.
func Close() {
	mu.Lock()
	defer mu.Unlock()
	closed = true
	if current != nil {
		w := current
		w.Dispatch(func() { w.Terminate() })
	}
}

// OpenBrowser opens url in the default browser.
func OpenBrowser(url string) error {
	cmd := exec.Command("rundll32", "url.dll,FileProtocolHandler", url)
	cmd.SysProcAttr = &syscall.SysProcAttr{HideWindow: true}
	return cmd.Start()
}

// ErrorBox shows a modal error dialog.
func ErrorBox(title, msg string) {
	user32 := syscall.NewLazyDLL("user32.dll")
	box := user32.NewProc("MessageBoxW")
	t, _ := syscall.UTF16PtrFromString(title)
	m, _ := syscall.UTF16PtrFromString(msg)
	_, _, _ = box.Call(0, uintptr(unsafe.Pointer(m)), uintptr(unsafe.Pointer(t)), 0x10)
}
