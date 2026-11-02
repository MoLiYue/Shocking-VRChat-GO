// Shocking VRChat (Go + WebView2 edition).
//
// Receives VRChat avatar OSC parameters and drives a DG-LAB Coyote 3.0 via the
// DG-LAB APP WebSocket protocols. The management UI is the Vue SPA served on
// the local web server and shown in a native WebView2 window on Windows.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"net"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"runtime"
	"runtime/debug"
	"strconv"
	"strings"
	"syscall"
	"time"

	"shockingvrc/assets"
	"shockingvrc/internal/app"
	"shockingvrc/internal/config"
	"shockingvrc/internal/logx"
	"shockingvrc/internal/ui"
)

func init() {
	// WebView2 / Win32 message loop must stay on the main OS thread.
	runtime.LockOSThread()
}

// baseDir returns the directory for configs, presets and logs: next to the
// executable, or the working directory when run through `go run`.
func baseDir(override string) string {
	if override != "" {
		abs, _ := filepath.Abs(override)
		return abs
	}
	exe, err := os.Executable()
	if err == nil {
		exe, _ = filepath.EvalSymlinks(exe)
		dir := filepath.Dir(exe)
		tmp := os.TempDir()
		if !strings.Contains(exe, "go-build") && !strings.HasPrefix(dir, tmp) {
			return dir
		}
	}
	wd, _ := os.Getwd()
	return wd
}

func isPackaged() bool {
	exe, err := os.Executable()
	return err == nil && !strings.Contains(exe, "go-build")
}

func listenWithRetry(addr string, wait time.Duration) (net.Listener, error) {
	deadline := time.Now().Add(wait)
	for {
		ln, err := net.Listen("tcp", addr)
		if err == nil || time.Now().After(deadline) {
			return ln, err
		}
		time.Sleep(250 * time.Millisecond)
	}
}

func fatal(base string, err error) {
	msg := fmt.Sprintf("Shocking VRChat crashed at startup:\n\n%v\n", err)
	_ = os.WriteFile(filepath.Join(base, "error.log"), []byte(msg+"\n"+string(debug.Stack())), 0o644)
	logx.Error("%v", err)
	logx.Error("[init] Unexpected Error. See error.log for details.")
	ui.ErrorBox("Shocking VRChat - Error", "程序启动失败，详细信息已写入 error.log:\n\n"+truncate(err.Error(), 500))
	logx.Close()
	os.Exit(1)
}

func truncate(s string, n int) string {
	if len(s) > n {
		return s[:n]
	}
	return s
}

func main() {
	dirFlag := flag.String("dir", "", "data directory (configs, presets, logs); default: next to the executable")
	browser := flag.Bool("browser", false, "open the system browser instead of the WebView2 window")
	headless := flag.Bool("headless", false, "do not open any UI window/browser")
	devtools := flag.Bool("devtools", false, "enable WebView2 developer tools")
	_ = flag.Bool("no-tray", false, "accepted for compatibility (no effect)")
	flag.Parse()

	base := baseDir(*dirFlag)
	_ = os.MkdirAll(base, 0o755)
	_ = logx.OpenFile(filepath.Join(base, "shocking_vrchat.log"))
	defer logx.Close()
	logx.Info("[init] Shocking VRChat %s (Go %s, %s/%s) dir=%s", app.Version, runtime.Version(), runtime.GOOS, runtime.GOARCH, base)

	if n, err := assets.ExtractDefaults(base); err != nil {
		logx.Warn("[init] Could not extract default presets: %v", err)
	} else if n > 0 {
		logx.Info("[init] Extracted %d default preset files", n)
	}

	a := app.New(base)
	a.Packaged = isPackaged()
	if err := a.Init(); err != nil {
		fatal(base, err)
	}

	host := config.Str(a.Cfg.Get("web_server", "listen_host"), "0.0.0.0")
	port := config.Int(a.Cfg.Get("web_server", "listen_port"), 8800)
	ln, err := listenWithRetry(net.JoinHostPort(host, strconv.Itoa(port)), 8*time.Second)
	if err != nil {
		fatal(base, fmt.Errorf("web server listen on %s:%d failed (port in use?): %w", host, port, err))
	}
	srv := &http.Server{Handler: a.Handler(), ReadHeaderTimeout: 10 * time.Second}
	go func() {
		if err := srv.Serve(ln); err != nil && !errors.Is(err, http.ErrServerClosed) {
			logx.Error("[web] %v", err)
		}
	}()

	a.Engine.Start()
	ctx, cancel := context.WithCancel(context.Background())
	go a.HotReloadLoop(ctx)
	a.StartChatboxIfEnabled()

	countEnabled := func(ch string) int {
		n := 0
		for _, p := range config.AsList(a.Cfg.Get("dglab3", ch, "avatar_params")) {
			if config.Bool(config.AsMap(p)["enabled"], true) {
				n++
			}
		}
		return n
	}
	logx.Info("[init] Channel A: %d params | Channel B: %d params", countEnabled("channel_a"), countEnabled("channel_b"))
	logx.Info("[init] Web: :%d | WS: :%d | OSC: :%d", port,
		config.Int(a.Cfg.Get("ws", "listen_port"), 0), config.Int(a.Cfg.Get("osc", "listen_port"), 0))

	quit := make(chan struct{})
	a.OnShutdown = func() {
		ui.Close()
		select {
		case <-quit:
		default:
			close(quit)
		}
	}
	sig := make(chan os.Signal, 1)
	signal.Notify(sig, os.Interrupt, syscall.SIGTERM)
	go func() {
		<-sig
		logx.Info("[shutdown] Signal received.")
		a.Shutdown()
	}()

	url := a.WebURL()
	autoOpen := config.Bool(a.Cfg.Get("general", "auto_open_qr_web_page"), true)
	switch {
	case *headless:
		logx.Success("[init] Open browser: %s", displayURL(a, host, port))
		<-quit
	case !*browser && ui.Available():
		logx.Info("[ui] Opening WebView2 window: %s", url)
		if ui.RunWindow(ui.Options{URL: url, Title: "Shocking VRChat", Width: 1280, Height: 860, Debug: *devtools}) {
			logx.Info("[ui] Window closed.")
			a.Shutdown()
			break
		}
		logx.Warn("[ui] WebView2 runtime unavailable, falling back to system browser. " +
			"Install it from https://go.microsoft.com/fwlink/p/?LinkId=2124703")
		_ = ui.OpenBrowser(url)
		<-quit
	default:
		if autoOpen && os.Getenv("SHOCKING_SKIP_OPEN") == "" {
			_ = ui.OpenBrowser(url)
		}
		logx.Success("[init] Open browser: %s", displayURL(a, host, port))
		<-quit
	}

	cancel()
	sctx, scancel := context.WithTimeout(context.Background(), 2*time.Second)
	_ = srv.Shutdown(sctx)
	scancel()
	logx.Info("[shutdown] Exiting...")
}

func displayURL(a *app.App, host string, port int) string {
	if host == "0.0.0.0" {
		host = a.CurrentIP()
	}
	return fmt.Sprintf("http://%s:%d", host, port)
}
