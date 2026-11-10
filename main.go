// Shocking VRChat (Go + WebView2 edition).
//
// Receives VRChat avatar OSC parameters and drives a DG-LAB Coyote 3.0 via the
// DG-LAB APP WebSocket protocols. The management UI is the Vue SPA served on
// the local web server and shown in a native WebView2 window on Windows.
package main

import (
	"context"
	"encoding/json"
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
	"sync/atomic"
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
	setupCrashLog(base)
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

	// Single instance: if we are already running, bring that window forward
	// and exit instead of failing on the busy port.
	if restartOf := os.Getenv("SVRC_RESTART"); restartOf != "" {
		// We replace a running instance (settings restart): let it exit
		// first so its ports and WebView2 data folder are free.
		if pid, err := strconv.Atoi(restartOf); err == nil {
			if !app.WaitProcessExit(pid, 15*time.Second) {
				logx.Warn("[init] Previous instance (pid %d) still running after 15s", pid)
			}
		}
	} else if activateRunningInstance(port) {
		logx.Info("[init] Already running; activated the existing window.")
		logx.Close()
		return
	}
	_ = os.Unsetenv("SVRC_RESTART")

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
		ui.StopTray()
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

	windowMode := !*headless && !*browser && ui.Available()
	show := func() {
		if !windowMode || !ui.ShowWindow() {
			_ = ui.OpenBrowser(url)
		}
	}
	a.OnShowWindow = show
	a.OnTheme = ui.SetDarkTheme
	if !*headless && ui.TrayAvailable() {
		a.TrayAvailable = true
		ui.StartTray(ui.TrayOptions{
			Tooltip:   "Shocking VRChat",
			OpenLabel: func() string { return pick(a, "打开管理界面", "Open Shocking VRChat") },
			QuitLabel: func() string { return pick(a, "退出", "Quit") },
			OnOpen:    show,
			OnQuit:    a.Shutdown,
		})
	}
	var hintShown atomic.Bool

	switch {
	case *headless:
		logx.Success("[init] Open browser: %s", displayURL(a, host, port))
		<-quit
	case !*browser && ui.Available():
		logx.Info("[ui] Opening WebView2 window: %s", url)
		if ui.RunWindow(ui.Options{
			URL: url, Title: "Shocking VRChat",
			Width: 1280, Height: 860, MinWidth: 960, MinHeight: 640,
			Debug: *devtools, StateFile: filepath.Join(base, "window_state.json"),
			Dark: a.UIThemeDark(),
			HideOnClose: func() bool {
				return a.TrayAvailable && config.Bool(a.Cfg.Get("general", "minimize_to_tray"), false)
			},
			OnHide: func() {
				if hintShown.CompareAndSwap(false, true) {
					ui.TrayNotify("Shocking VRChat", pick(a,
						"程序仍在后台运行，单击托盘图标重新打开。",
						"Still running in the background. Click the tray icon to reopen."))
				}
			},
		}) {
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

// activateRunningInstance asks an already running copy (same web port) to
// show its window. Returns true if one answered.
func activateRunningInstance(port int) bool {
	base := fmt.Sprintf("http://127.0.0.1:%d", port)
	c := &http.Client{Timeout: 1500 * time.Millisecond}
	resp, err := c.Get(base + "/api/v1/status")
	if err != nil {
		return false
	}
	defer resp.Body.Close()
	var st struct {
		Healthy string `json:"healthy"`
	}
	if json.NewDecoder(resp.Body).Decode(&st) != nil || st.Healthy != "ok" {
		return false
	}
	ui.AllowForeground()
	if r, err := c.Post(base+"/api/v1/ui/show", "application/json", nil); err == nil {
		r.Body.Close()
	}
	return true
}

// pick returns the string for the UI language last chosen by the user.
func pick(a *app.App, zh, en string) string {
	if a.UILocale() == "en" {
		return en
	}
	return zh
}

// setupCrashLog sends Go runtime crash reports (unrecovered panics and
// fatal errors such as concurrent map writes, with all goroutine stacks)
// to crash.log. A windowsgui exe has no console, so without this a crash
// just makes the window vanish with no trace.
func setupCrashLog(base string) {
	path := filepath.Join(base, "crash.log")
	f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		logx.Warn("[init] Cannot open crash.log: %v", err)
		return
	}
	fmt.Fprintf(f, "\n=== %s started %s (pid %d) ===\n", app.Version, time.Now().Format(time.RFC3339), os.Getpid())
	if err := debug.SetCrashOutput(f, debug.CrashOptions{}); err != nil {
		logx.Warn("[init] SetCrashOutput: %v", err)
	}
	_ = f.Close() // SetCrashOutput keeps its own duplicate
}
