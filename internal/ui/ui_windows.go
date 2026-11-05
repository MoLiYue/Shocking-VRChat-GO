//go:build windows

// Package ui provides the native window (WebView2 on Windows) and helpers.
package ui

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"sync"
	"syscall"
	"unsafe"

	webview2 "github.com/jchv/go-webview2"
	"golang.org/x/sys/windows"
)

// IconResourceID is the icon ordinal embedded by go-winres (see winres/).
const IconResourceID = 1

// Window/title-bar backgrounds per theme (match --bg-base in global.css:
// Catppuccin Mocha base / Rosé Pine Dawn base), so nothing flashes before paint.
const (
	BackgroundDark  = 0x1e1e2e
	BackgroundLight = 0xfaf4ed
)

// Options configures the window.
type Options struct {
	URL       string
	Title     string
	Width     uint // logical (96 DPI) size; scaled by the system DPI
	Height    uint
	MinWidth  uint
	MinHeight uint
	Debug     bool
	// StateFile persists window position/size/maximized between runs ("" = off).
	StateFile string
	// Dark selects the initial window/title-bar theme.
	Dark bool
	// HideOnClose, when it returns true, makes the close button hide the
	// window (to the tray) instead of exiting. OnHide runs after hiding.
	HideOnClose func() bool
	OnHide      func()
}

var (
	user32   = windows.NewLazySystemDLL("user32.dll")
	gdi32    = windows.NewLazySystemDLL("gdi32.dll")
	dwmapi   = windows.NewLazySystemDLL("dwmapi.dll")
	kernel32 = windows.NewLazySystemDLL("kernel32.dll")

	pSetWindowsHookExW    = user32.NewProc("SetWindowsHookExW")
	pUnhookWindowsHookEx  = user32.NewProc("UnhookWindowsHookEx")
	pCallNextHookEx       = user32.NewProc("CallNextHookEx")
	pGetClassNameW        = user32.NewProc("GetClassNameW")
	pSetClassLongPtrW     = user32.NewProc("SetClassLongPtrW")
	pGetWindowPlacement   = user32.NewProc("GetWindowPlacement")
	pIsWindow             = user32.NewProc("IsWindow")
	pMonitorFromRect      = user32.NewProc("MonitorFromRect")
	pGetDpiForSystem      = user32.NewProc("GetDpiForSystem")
	pShowWindow           = user32.NewProc("ShowWindow")
	pSystemParametersInfo = user32.NewProc("SystemParametersInfoW")
	pMessageBoxW          = user32.NewProc("MessageBoxW")
	pSetWindowLongPtrW    = user32.NewProc("SetWindowLongPtrW")
	pCallWindowProcW      = user32.NewProc("CallWindowProcW")
	pIsIconic             = user32.NewProc("IsIconic")
	pIsWindowVisible      = user32.NewProc("IsWindowVisible")
	pAllowSetForeground   = user32.NewProc("AllowSetForegroundWindow")
	pCreateSolidBrush     = gdi32.NewProc("CreateSolidBrush")
	pDeleteObject         = gdi32.NewProc("DeleteObject")
	pDwmSetWindowAttr     = dwmapi.NewProc("DwmSetWindowAttribute")
	pGetCurrentThreadId   = kernel32.NewProc("GetCurrentThreadId")
)

const (
	whCBT             = 5
	whCallWndProc     = 4
	hcbtCreateWnd     = 3
	wmCreate          = 0x0001
	wmClose           = 0x0010
	gclpHbrBackground = ^uintptr(9) // -10
	swShowMaximized   = 3
	swHide            = 0
	swShow            = 5
	swRestore         = 9
	gwlpWndProc       = ^uintptr(3) // -4
	spiGetWorkArea    = 0x0030
	monitorNull       = 0

	dwmwaUseImmersiveDarkModeOld = 19
	dwmwaUseImmersiveDarkMode    = 20
	dwmwaCaptionColor            = 35
)

type rect struct{ Left, Top, Right, Bottom int32 }
type point struct{ X, Y int32 }

type windowPlacement struct {
	Length    uint32
	Flags     uint32
	ShowCmd   uint32
	MinPos    point
	MaxPos    point
	NormalPos rect
}

type createStruct struct {
	CreateParams uintptr
	Instance     uintptr
	Menu         uintptr
	Parent       uintptr
	Cy, Cx, Y, X int32
	Style        int32
	Name         *uint16
	Class        *uint16
	ExStyle      uint32
}

type cbtCreateWnd struct {
	Cs          *createStruct
	InsertAfter uintptr
}

type cwpStruct struct {
	LParam  uintptr
	WParam  uintptr
	Message uint32
	Hwnd    uintptr
}

// windowState is persisted to Options.StateFile.
type windowState struct {
	X, Y, W, H int32
	Maximized  bool
}

var (
	mu       sync.Mutex
	current  webview2.WebView
	closed   bool
	curHwnd  uintptr
	quitting bool
	oldProc  uintptr
	subProc  uintptr
)

// Available reports whether a native window can be created on this platform.
func Available() bool { return true }

func dataDir() string {
	base := os.Getenv("LOCALAPPDATA")
	if base == "" {
		base = os.TempDir()
	}
	return filepath.Join(base, "ShockingVRChat", "WebView2")
}

func colorref(rgb uint32) uint32 { // 0xRRGGBB -> COLORREF 0x00BBGGRR
	return (rgb&0xff)<<16 | rgb&0xff00 | (rgb>>16)&0xff
}

func systemDPI() int32 {
	if pGetDpiForSystem.Find() == nil {
		if d, _, _ := pGetDpiForSystem.Call(); d > 0 {
			return int32(d)
		}
	}
	return 96
}

func workArea() rect {
	var r rect
	_, _, _ = pSystemParametersInfo.Call(spiGetWorkArea, 0, uintptr(unsafe.Pointer(&r)), 0)
	return r
}

func isOnScreen(r rect) bool {
	h, _, _ := pMonitorFromRect.Call(uintptr(unsafe.Pointer(&r)), monitorNull)
	return h != 0
}

func loadState(path string) *windowState {
	if path == "" {
		return nil
	}
	b, err := os.ReadFile(path)
	if err != nil {
		return nil
	}
	var s windowState
	if json.Unmarshal(b, &s) != nil || s.W < 200 || s.H < 200 {
		return nil
	}
	if !isOnScreen(rect{s.X, s.Y, s.X + s.W, s.Y + s.H}) {
		return nil // monitor was removed: fall back to centered default
	}
	return &s
}

func saveState(path string, s *windowState) {
	if path == "" || s == nil {
		return
	}
	b, _ := json.Marshal(s)
	_ = os.WriteFile(path, b, 0o644)
}

func placementOf(hwnd uintptr) *windowState {
	if ok, _, _ := pIsWindow.Call(hwnd); ok == 0 {
		return nil
	}
	wp := windowPlacement{Length: uint32(unsafe.Sizeof(windowPlacement{}))}
	if r, _, _ := pGetWindowPlacement.Call(hwnd, uintptr(unsafe.Pointer(&wp))); r == 0 {
		return nil
	}
	n := wp.NormalPos
	return &windowState{X: n.Left, Y: n.Top, W: n.Right - n.Left, H: n.Bottom - n.Top, Maximized: wp.ShowCmd == swShowMaximized}
}

func className(hwnd uintptr) string {
	buf := make([]uint16, 64)
	n, _, _ := pGetClassNameW.Call(hwnd, uintptr(unsafe.Pointer(&buf[0])), uintptr(len(buf)))
	return windows.UTF16ToString(buf[:n])
}

func themeBG(dark bool) uint32 {
	if dark {
		return BackgroundDark
	}
	return BackgroundLight
}

// themeCaption applies the DWM title bar theme. Only effective once the
// window has been created (WM_CREATE or later); ignored during HCBT_CREATEWND.
func themeCaption(hwnd uintptr, dark bool) {
	bg := themeBG(dark)
	if pDwmSetWindowAttr.Find() == nil {
		on := int32(0)
		if dark {
			on = 1
		}
		if r, _, _ := pDwmSetWindowAttr.Call(hwnd, dwmwaUseImmersiveDarkMode, uintptr(unsafe.Pointer(&on)), 4); r != 0 {
			_, _, _ = pDwmSetWindowAttr.Call(hwnd, dwmwaUseImmersiveDarkModeOld, uintptr(unsafe.Pointer(&on)), 4)
		}
		// Windows 11: tint the caption to the app background (ignored on Win10).
		c := colorref(bg)
		_, _, _ = pDwmSetWindowAttr.Call(hwnd, dwmwaCaptionColor, uintptr(unsafe.Pointer(&c)), 4)
	}
}

// themeBrush sets the window class background brush.
func themeBrush(hwnd uintptr, dark bool) {
	if brush, _, _ := pCreateSolidBrush.Call(uintptr(colorref(themeBG(dark)))); brush != 0 {
		// SetClassLongPtr returns the previous brush; free it unless it is
		// a stock/system brush (we only ever replace our own after the first).
		old, _, _ := pSetClassLongPtrW.Call(hwnd, gclpHbrBackground, brush)
		brushMu.Lock()
		if old != 0 && old == ownBrush {
			_, _, _ = pDeleteObject.Call(old)
		}
		ownBrush = brush
		brushMu.Unlock()
	}
}

var (
	brushMu  sync.Mutex
	ownBrush uintptr // background brush we created (freed on replace)
)

// themeWindow applies the title bar theme and background brush.
func themeWindow(hwnd uintptr, dark bool) {
	themeCaption(hwnd, dark)
	themeBrush(hwnd, dark)
}

// SetDarkTheme switches the title bar of the open window (any goroutine).
func SetDarkTheme(dark bool) {
	mu.Lock()
	hw := curHwnd
	mu.Unlock()
	if hw != 0 {
		themeWindow(hw, dark)
	}
}

// RunWindow opens the WebView2 window and blocks until it is closed.
// Returns false immediately if WebView2 is unavailable (runtime missing).
// Must be called from the main goroutine (locked OS thread).
func RunWindow(o Options) bool {
	_ = os.MkdirAll(dataDir(), 0o755)
	// WebView2 paints this before the first page renders (ARGB hex).
	if o.Dark {
		_ = os.Setenv("WEBVIEW2_DEFAULT_BACKGROUND_COLOR", "FF1E1E2E")
	} else {
		_ = os.Setenv("WEBVIEW2_DEFAULT_BACKGROUND_COLOR", "FFFAF4ED")
	}

	dpi := systemDPI()
	scale := func(v uint) int32 { return int32(v) * dpi / 96 }
	wa := workArea()
	w, h := scale(o.Width), scale(o.Height)
	if aw := (wa.Right - wa.Left) * 95 / 100; w > aw && aw > 0 {
		w = aw
	}
	if ah := (wa.Bottom - wa.Top) * 95 / 100; h > ah && ah > 0 {
		h = ah
	}
	st := loadState(o.StateFile)
	if st == nil {
		st = &windowState{W: w, H: h, X: wa.Left + (wa.Right-wa.Left-w)/2, Y: wa.Top + (wa.Bottom-wa.Top-h)/2}
	}

	// Thread-local hooks: theme + position the window before it is shown,
	// and remember its placement when the user closes it.
	var hwnd uintptr
	var lastState *windowState
	tid, _, _ := pGetCurrentThreadId.Call()
	cbt := windows.NewCallback(func(code, wp, lp uintptr) uintptr {
		if int32(code) == hcbtCreateWnd && hwnd == 0 && className(wp) == "webview" {
			hwnd = wp
			cs := (*(**cbtCreateWnd)(unsafe.Pointer(&lp))).Cs // lp is a CBT_CREATEWND*
			cs.X, cs.Y, cs.Cx, cs.Cy = st.X, st.Y, st.W, st.H
			themeBrush(wp, o.Dark)
		}
		r, _, _ := pCallNextHookEx.Call(0, code, wp, lp)
		return r
	})
	cwp := windows.NewCallback(func(code, wp, lp uintptr) uintptr {
		if int32(code) >= 0 {
			m := *(**cwpStruct)(unsafe.Pointer(&lp)) // lp is a CWPSTRUCT*
			if m.Message == wmCreate && m.Hwnd == hwnd && hwnd != 0 {
				// DWM attributes set during HCBT_CREATEWND are dropped; apply
				// them here, before the window is first shown.
				themeCaption(hwnd, o.Dark)
			}
			if m.Message == wmClose && m.Hwnd == hwnd && hwnd != 0 {
				lastState = placementOf(hwnd)
			}
		}
		r, _, _ := pCallNextHookEx.Call(0, code, wp, lp)
		return r
	})
	hCBT, _, _ := pSetWindowsHookExW.Call(whCBT, cbt, 0, tid)
	hCWP, _, _ := pSetWindowsHookExW.Call(whCallWndProc, cwp, 0, tid)
	defer func() {
		if hCWP != 0 {
			_, _, _ = pUnhookWindowsHookEx.Call(hCWP)
		}
	}()

	wv := webview2.NewWithOptions(webview2.WebViewOptions{
		Debug:     o.Debug,
		AutoFocus: true,
		DataPath:  dataDir(),
		WindowOptions: webview2.WindowOptions{
			Title:  o.Title,
			Width:  uint(st.W),
			Height: uint(st.H),
			IconId: IconResourceID,
		},
	})
	if hCBT != 0 {
		_, _, _ = pUnhookWindowsHookEx.Call(hCBT)
	}
	if wv == nil {
		return false
	}
	mu.Lock()
	if closed {
		mu.Unlock()
		wv.Destroy()
		return true
	}
	current = wv
	mu.Unlock()
	if o.MinWidth > 0 && o.MinHeight > 0 {
		wv.SetSize(int(scale(o.MinWidth)), int(scale(o.MinHeight)), webview2.HintMin)
	}
	if st.Maximized {
		_, _, _ = pShowWindow.Call(uintptr(wv.Window()), swShowMaximized)
	}
	hw := uintptr(wv.Window())
	themeCaption(hw, o.Dark) // in case WM_CREATE was missed
	mu.Lock()
	curHwnd = hw
	mu.Unlock()
	if o.HideOnClose != nil {
		// Subclass the window so the close button can hide instead of exit.
		subProc = windows.NewCallback(func(h, m, w, l uintptr) uintptr {
			if uint32(m) == wmClose {
				mu.Lock()
				q := quitting
				mu.Unlock()
				if !q && o.HideOnClose() {
					_, _, _ = pShowWindow.Call(h, swHide)
					if o.OnHide != nil {
						go o.OnHide()
					}
					return 0
				}
			}
			r, _, _ := pCallWindowProcW.Call(oldProc, h, m, w, l)
			return r
		})
		oldProc, _, _ = pSetWindowLongPtrW.Call(hw, gwlpWndProc, subProc)
	}
	wv.Navigate(o.URL)
	wv.Run()
	// Closed by the user (state captured on WM_CLOSE) or programmatically
	// via Close() (window still alive here).
	// A window hidden to the tray reports no useful show state; keep the
	// placement captured when it was closed/hidden in that case.
	if vis, _, _ := pIsWindowVisible.Call(uintptr(wv.Window())); vis != 0 || lastState == nil {
		if s := placementOf(uintptr(wv.Window())); s != nil {
			lastState = s
		}
	}
	saveState(o.StateFile, lastState)
	mu.Lock()
	current = nil
	curHwnd = 0
	closed = true
	mu.Unlock()
	wv.Destroy()
	return true
}

// ShowWindow brings the (possibly hidden or minimized) window to the front.
// Returns false if there is no window.
func ShowWindow() bool {
	mu.Lock()
	hw := curHwnd
	mu.Unlock()
	if hw == 0 {
		return false
	}
	if ic, _, _ := pIsIconic.Call(hw); ic != 0 {
		_, _, _ = pShowWindow.Call(hw, swRestore)
	} else {
		_, _, _ = pShowWindow.Call(hw, swShow)
	}
	_, _, _ = pSetForegroundWindow.Call(hw)
	return true
}

// AllowForeground lets another process (the running instance) take focus.
func AllowForeground() { _, _, _ = pAllowSetForeground.Call(^uintptr(0)) }

// Close terminates the window from any goroutine.
func Close() {
	mu.Lock()
	defer mu.Unlock()
	closed = true
	quitting = true
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
	t, _ := syscall.UTF16PtrFromString(title)
	m, _ := syscall.UTF16PtrFromString(msg)
	_, _, _ = pMessageBoxW.Call(0, uintptr(unsafe.Pointer(m)), uintptr(unsafe.Pointer(t)), 0x10)
}
