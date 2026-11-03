//go:build windows

package ui

import (
	"runtime"
	"sync"
	"unsafe"

	"golang.org/x/sys/windows"
)

// TrayOptions configures the notification-area icon.
type TrayOptions struct {
	Tooltip string
	// Labels are evaluated each time the menu opens so they follow the UI language.
	OpenLabel func() string
	QuitLabel func() string
	OnOpen    func()
	OnQuit    func()
}

var (
	shell32 = windows.NewLazySystemDLL("shell32.dll")

	pShellNotifyIconW       = shell32.NewProc("Shell_NotifyIconW")
	pRegisterClassExW       = user32.NewProc("RegisterClassExW")
	pCreateWindowExW        = user32.NewProc("CreateWindowExW")
	pDefWindowProcW         = user32.NewProc("DefWindowProcW")
	pGetMessageW            = user32.NewProc("GetMessageW")
	pTranslateMessage       = user32.NewProc("TranslateMessage")
	pDispatchMessageW       = user32.NewProc("DispatchMessageW")
	pPostMessageW           = user32.NewProc("PostMessageW")
	pPostQuitMessage        = user32.NewProc("PostQuitMessage")
	pRegisterWindowMessageW = user32.NewProc("RegisterWindowMessageW")
	pCreatePopupMenu        = user32.NewProc("CreatePopupMenu")
	pAppendMenuW            = user32.NewProc("AppendMenuW")
	pSetMenuDefaultItem     = user32.NewProc("SetMenuDefaultItem")
	pTrackPopupMenu         = user32.NewProc("TrackPopupMenu")
	pDestroyMenu            = user32.NewProc("DestroyMenu")
	pGetCursorPos           = user32.NewProc("GetCursorPos")
	pSetForegroundWindow    = user32.NewProc("SetForegroundWindow")
	pLoadImageW             = user32.NewProc("LoadImageW")
	pGetSystemMetrics       = user32.NewProc("GetSystemMetrics")
	pGetModuleHandleW       = kernel32.NewProc("GetModuleHandleW")
)

const (
	nimAdd    = 0
	nimModify = 1
	nimDelete = 2
	nifMsg    = 0x1
	nifIcon   = 0x2
	nifTip    = 0x4
	nifInfo   = 0x10
	niifInfo  = 0x1

	wmDestroy       = 0x0002
	wmNull          = 0x0000
	wmApp           = 0x8000
	wmTray          = wmApp + 1
	wmLButtonUp     = 0x0202
	wmLButtonDblClk = 0x0203
	wmRButtonUp     = 0x0205
	wmContextMenu   = 0x007B

	mfString      = 0x0
	mfSeparator   = 0x800
	tpmReturnCmd  = 0x100
	tpmNoNotify   = 0x80
	tpmRightAlign = 0x8
	tpmBottomAlgn = 0x20
	imageIcon     = 1
	smCxSmIcon    = 49
	smCySmIcon    = 50
	hwndMessage   = ^uintptr(2) // (HWND)-3

	cmdOpen = 1
	cmdQuit = 2
)

type notifyIconData struct {
	CbSize           uint32
	HWnd             uintptr
	UID              uint32
	UFlags           uint32
	UCallbackMessage uint32
	HIcon            uintptr
	SzTip            [128]uint16
	DwState          uint32
	DwStateMask      uint32
	SzInfo           [256]uint16
	UVersion         uint32
	SzInfoTitle      [64]uint16
	DwInfoFlags      uint32
	GuidItem         windows.GUID
	HBalloonIcon     uintptr
}

type wndClassEx struct {
	Size       uint32
	Style      uint32
	WndProc    uintptr
	ClsExtra   int32
	WndExtra   int32
	Instance   uintptr
	Icon       uintptr
	Cursor     uintptr
	Background uintptr
	MenuName   *uint16
	ClassName  *uint16
	IconSm     uintptr
}

type msgT struct {
	Hwnd    uintptr
	Message uint32
	WParam  uintptr
	LParam  uintptr
	Time    uint32
	Pt      point
	_       uint32
}

type tray struct {
	opts           TrayOptions
	hwnd           uintptr
	nid            notifyIconData
	taskbarCreated uint32
}

var (
	trayMu  sync.Mutex
	trayCur *tray
)

// TrayAvailable reports whether a notification-area icon is supported.
func TrayAvailable() bool { return true }

func copyUTF16(dst []uint16, s string) {
	u, _ := windows.UTF16FromString(s)
	if len(u) > len(dst) {
		u = u[:len(dst)]
		u[len(u)-1] = 0
	}
	copy(dst, u)
}

func (t *tray) add() {
	_, _, _ = pShellNotifyIconW.Call(nimAdd, uintptr(unsafe.Pointer(&t.nid)))
}

func (t *tray) showMenu() {
	menu, _, _ := pCreatePopupMenu.Call()
	if menu == 0 {
		return
	}
	defer pDestroyMenu.Call(menu)
	open, _ := windows.UTF16PtrFromString(t.opts.OpenLabel())
	quit, _ := windows.UTF16PtrFromString(t.opts.QuitLabel())
	_, _, _ = pAppendMenuW.Call(menu, mfString, cmdOpen, uintptr(unsafe.Pointer(open)))
	_, _, _ = pAppendMenuW.Call(menu, mfSeparator, 0, 0)
	_, _, _ = pAppendMenuW.Call(menu, mfString, cmdQuit, uintptr(unsafe.Pointer(quit)))
	_, _, _ = pSetMenuDefaultItem.Call(menu, cmdOpen, 0)
	var pt point
	_, _, _ = pGetCursorPos.Call(uintptr(unsafe.Pointer(&pt)))
	// Required so the menu closes when clicking elsewhere (KB135788).
	_, _, _ = pSetForegroundWindow.Call(t.hwnd)
	cmd, _, _ := pTrackPopupMenu.Call(menu, tpmReturnCmd|tpmNoNotify|tpmRightAlign|tpmBottomAlgn,
		uintptr(pt.X), uintptr(pt.Y), 0, t.hwnd, 0)
	_, _, _ = pPostMessageW.Call(t.hwnd, wmNull, 0, 0)
	switch cmd {
	case cmdOpen:
		if t.opts.OnOpen != nil {
			go t.opts.OnOpen()
		}
	case cmdQuit:
		if t.opts.OnQuit != nil {
			go t.opts.OnQuit()
		}
	}
}

func (t *tray) wndProc(hwnd uintptr, msg uint32, wp, lp uintptr) uintptr {
	switch {
	case msg == wmTray:
		switch uint32(lp & 0xffff) {
		case wmLButtonUp, wmLButtonDblClk:
			if t.opts.OnOpen != nil {
				go t.opts.OnOpen()
			}
		case wmRButtonUp, wmContextMenu:
			t.showMenu()
		}
		return 0
	case t.taskbarCreated != 0 && msg == t.taskbarCreated:
		t.add() // Explorer restarted: the icon must be re-added
		return 0
	case msg == wmDestroy:
		_, _, _ = pShellNotifyIconW.Call(nimDelete, uintptr(unsafe.Pointer(&t.nid)))
		_, _, _ = pPostQuitMessage.Call(0)
		return 0
	}
	r, _, _ := pDefWindowProcW.Call(hwnd, uintptr(msg), wp, lp)
	return r
}

// StartTray shows the tray icon; it runs its own message loop on a
// dedicated OS thread so it is independent of the WebView window.
func StartTray(o TrayOptions) {
	trayMu.Lock()
	if trayCur != nil {
		trayMu.Unlock()
		return
	}
	t := &tray{opts: o}
	trayCur = t
	trayMu.Unlock()

	ready := make(chan struct{})
	go func() {
		runtime.LockOSThread()
		defer runtime.UnlockOSThread()
		hinst, _, _ := pGetModuleHandleW.Call(0)
		cls, _ := windows.UTF16PtrFromString("ShockingVRChatTray")
		wc := wndClassEx{
			Size:      uint32(unsafe.Sizeof(wndClassEx{})),
			WndProc:   windows.NewCallback(func(h, m, w, l uintptr) uintptr { return t.wndProc(h, uint32(m), w, l) }),
			Instance:  hinst,
			ClassName: cls,
		}
		_, _, _ = pRegisterClassExW.Call(uintptr(unsafe.Pointer(&wc)))
		t.hwnd, _, _ = pCreateWindowExW.Call(0, uintptr(unsafe.Pointer(cls)), 0, 0, 0, 0, 0, 0, hwndMessage, 0, hinst, 0)
		if tc, err := windows.UTF16PtrFromString("TaskbarCreated"); err == nil {
			r, _, _ := pRegisterWindowMessageW.Call(uintptr(unsafe.Pointer(tc)))
			t.taskbarCreated = uint32(r)
		}
		cx, _, _ := pGetSystemMetrics.Call(smCxSmIcon)
		cy, _, _ := pGetSystemMetrics.Call(smCySmIcon)
		icon, _, _ := pLoadImageW.Call(hinst, IconResourceID, imageIcon, cx, cy, 0)
		t.nid = notifyIconData{
			HWnd: t.hwnd, UID: 1, UFlags: nifMsg | nifIcon | nifTip,
			UCallbackMessage: wmTray, HIcon: icon,
		}
		t.nid.CbSize = uint32(unsafe.Sizeof(t.nid))
		copyUTF16(t.nid.SzTip[:], o.Tooltip)
		t.add()
		close(ready)

		var m msgT
		for {
			r, _, _ := pGetMessageW.Call(uintptr(unsafe.Pointer(&m)), 0, 0, 0)
			if int32(r) <= 0 {
				return
			}
			_, _, _ = pTranslateMessage.Call(uintptr(unsafe.Pointer(&m)))
			_, _, _ = pDispatchMessageW.Call(uintptr(unsafe.Pointer(&m)))
		}
	}()
	<-ready
}

// TrayNotify shows a balloon/toast from the tray icon.
func TrayNotify(title, msg string) {
	trayMu.Lock()
	t := trayCur
	trayMu.Unlock()
	if t == nil || t.hwnd == 0 {
		return
	}
	n := t.nid
	n.UFlags = nifInfo
	n.DwInfoFlags = niifInfo
	copyUTF16(n.SzInfo[:], msg)
	copyUTF16(n.SzInfoTitle[:], title)
	_, _, _ = pShellNotifyIconW.Call(nimModify, uintptr(unsafe.Pointer(&n)))
}

// StopTray removes the icon and ends its message loop.
func StopTray() {
	trayMu.Lock()
	t := trayCur
	trayCur = nil
	trayMu.Unlock()
	if t != nil && t.hwnd != 0 {
		// DestroyWindow must run on the owning thread: post WM_CLOSE.
		_, _, _ = pPostMessageW.Call(t.hwnd, wmClose, 0, 0)
	}
}
