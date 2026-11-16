//go:build windows

package win

import (
	"errors"
	"fmt"
	"runtime"
	"sync"
	"unsafe"

	"golang.org/x/sys/windows"
)

// 原生 UI 线程：一个锁定的 OS 线程，运行消息循环，承载隐藏的宿主窗口
// （全局热键、剪贴板监听、托盘图标）以及截图遮罩/录屏浮窗等原生窗口。

const (
	wmInvoke  = WM_APP + 1
	wmTray    = WM_APP + 2
	timerClip = 1001
	hostClass = "DesktopToolboxHost"
)

// Handlers 原生事件回调（均在新的 goroutine 中调用，不会阻塞 UI 线程）。
type Handlers struct {
	OnHotkey        func(id int)
	OnClipboardText func(text string)
	OnClipboardPNG  func(png []byte)
	OnTrayCommand   func(cmd int)
	OnTrayClick     func()
}

var (
	host        uintptr
	hostTID     uint32
	handlers    Handlers
	invokeMu    sync.Mutex
	invokeQueue []func()
	wndProcCB   = newWndProcCallback()
	windowProcs = map[uintptr]func(hwnd, msg, wp, lp uintptr) (uintptr, bool){}
	classesMu   sync.Mutex
	classes     = map[string]bool{}
	taskbarMsg  uint32
	trayAdded   bool
	trayTip     string
)

func newWndProcCallback() uintptr {
	return syscallNewCallback(func(hwnd, msg, wp, lp uintptr) uintptr {
		if h, ok := windowProcs[hwnd]; ok {
			if r, handled := h(hwnd, msg, wp, lp); handled {
				return r
			}
		}
		if hwnd == host {
			if r, handled := hostProc(hwnd, uint32(msg), wp, lp); handled {
				return r
			}
		}
		r, _, _ := pDefWindowProcW.Call(hwnd, msg, wp, lp)
		return r
	})
}

func registerClass(name string, cursor uintptr, style uint32) error {
	classesMu.Lock()
	defer classesMu.Unlock()
	if classes[name] {
		return nil
	}
	wc := WNDCLASSEXW{
		CbSize:        uint32(unsafe.Sizeof(WNDCLASSEXW{})),
		Style:         style,
		LpfnWndProc:   wndProcCB,
		HInstance:     moduleHandle(),
		HCursor:       cursor,
		LpszClassName: u16(name),
	}
	r, _, err := pRegisterClassExW.Call(uintptr(unsafe.Pointer(&wc)))
	if r == 0 {
		return fmt.Errorf("RegisterClassEx(%s): %v", name, err)
	}
	classes[name] = true
	return nil
}

// createWindow 在 UI 线程上创建窗口并登记消息处理函数。
func createWindow(class string, exStyle, style uint32, x, y, w, h int,
	proc func(hwnd, msg, wp, lp uintptr) (uintptr, bool)) (uintptr, error) {
	// 先登记一个临时处理（WM_CREATE 等早期消息走 DefWindowProc 即可）
	hwnd, _, err := pCreateWindowExW.Call(uintptr(exStyle), uintptr(unsafe.Pointer(u16(class))), uintptr(unsafe.Pointer(u16(""))),
		uintptr(style), uintptr(int32(x)), uintptr(int32(y)), uintptr(int32(w)), uintptr(int32(h)), 0, 0, moduleHandle(), 0)
	if hwnd == 0 {
		return 0, fmt.Errorf("CreateWindowEx: %v", err)
	}
	if proc != nil {
		windowProcs[hwnd] = proc
	}
	return hwnd, nil
}

func destroyWindow(hwnd uintptr) {
	pDestroyWindow.Call(hwnd)
	delete(windowProcs, hwnd)
}

// Start 启动原生 UI 线程（只调用一次）。
func Start(h Handlers, tooltip string) error {
	handlers = h
	trayTip = tooltip
	ready := make(chan error, 1)
	go func() {
		runtime.LockOSThread()
		windows.CoInitializeEx(0, windows.COINIT_APARTMENTTHREADED)
		hostTID = currentThreadID()
		if err := registerClass(hostClass, loadCursor(IDC_ARROW), 0); err != nil {
			ready <- err
			return
		}
		hwnd, err := createWindow(hostClass, WS_EX_TOOLWINDOW, WS_POPUP, 0, 0, 0, 0, nil)
		if err != nil {
			ready <- err
			return
		}
		host = hwnd
		r, _, _ := pRegisterWindowMessageW.Call(uintptr(unsafe.Pointer(u16("TaskbarCreated"))))
		taskbarMsg = uint32(r)
		ready <- nil
		var msg MSG
		for {
			r, _, _ := pGetMessageW.Call(uintptr(unsafe.Pointer(&msg)), 0, 0, 0)
			if int32(r) <= 0 {
				return
			}
			pTranslateMessage.Call(uintptr(unsafe.Pointer(&msg)))
			pDispatchMessageW.Call(uintptr(unsafe.Pointer(&msg)))
		}
	}()
	return <-ready
}

// Invoke 在 UI 线程上同步执行 fn。
func Invoke(fn func()) {
	if host == 0 {
		fn()
		return
	}
	if currentThreadID() == hostTID {
		fn()
		return
	}
	done := make(chan struct{})
	invokeMu.Lock()
	invokeQueue = append(invokeQueue, func() { defer close(done); fn() })
	invokeMu.Unlock()
	pPostMessageW.Call(host, wmInvoke, 0, 0)
	<-done
}

// InvokeAsync 在 UI 线程上异步执行 fn。
func InvokeAsync(fn func()) {
	invokeMu.Lock()
	invokeQueue = append(invokeQueue, fn)
	invokeMu.Unlock()
	pPostMessageW.Call(host, wmInvoke, 0, 0)
}

func runQueue() {
	invokeMu.Lock()
	q := invokeQueue
	invokeQueue = nil
	invokeMu.Unlock()
	for _, f := range q {
		func() {
			defer func() { recover() }()
			f()
		}()
	}
}

func hostProc(hwnd uintptr, msg uint32, wp, lp uintptr) (uintptr, bool) {
	switch msg {
	case wmInvoke:
		runQueue()
		return 0, true
	case WM_HOTKEY:
		id := int(wp)
		if handlers.OnHotkey != nil {
			go handlers.OnHotkey(id)
		}
		return 0, true
	case WM_CLIPBOARDUPDATE:
		// 稍等片刻再读取，避免与写入方争用/延迟渲染
		pSetTimer.Call(hwnd, timerClip, 120, 0)
		return 0, true
	case WM_TIMER:
		if wp == timerClip {
			pKillTimer.Call(hwnd, timerClip)
			readClipboardAndNotify()
			return 0, true
		}
	case wmTray:
		switch uint32(lp & 0xFFFF) {
		case WM_LBUTTONUP:
			if handlers.OnTrayClick != nil {
				go handlers.OnTrayClick()
			}
		case WM_RBUTTONUP, WM_CONTEXTMENU:
			showTrayMenu()
		}
		return 0, true
	}
	if taskbarMsg != 0 && msg == taskbarMsg && trayAdded {
		trayAdded = false
		addTray()
		return 0, true
	}
	return 0, false
}

// ---------------------------------------------------------------- hotkeys

// Hotkey 热键定义。
type Hotkey struct {
	ID   int
	Mods uint32
	VK   uint32
	Name string
}

// RegisterHotkeys 注册全局热键，返回注册失败的热键名称（通常被其他程序占用）。
func RegisterHotkeys(keys []Hotkey) []string {
	var failed []string
	Invoke(func() {
		for _, k := range keys {
			pUnregisterHotKey.Call(host, uintptr(k.ID))
			r, _, _ := pRegisterHotKey.Call(host, uintptr(k.ID), uintptr(k.Mods|MOD_NOREPEAT), uintptr(k.VK))
			if r == 0 {
				failed = append(failed, k.Name)
			}
		}
	})
	return failed
}

// UnregisterHotkeys 注销热键。
func UnregisterHotkeys(keys []Hotkey) {
	Invoke(func() {
		for _, k := range keys {
			pUnregisterHotKey.Call(host, uintptr(k.ID))
		}
	})
}

// ---------------------------------------------------------------- clipboard watch

// WatchClipboard 开始/停止监听剪贴板变化。
func WatchClipboard(on bool) error {
	var err error
	Invoke(func() {
		if on {
			r, _, e := pAddClipboardFormatListener.Call(host)
			if r == 0 {
				err = fmt.Errorf("无法监听剪贴板: %v", e)
			}
		} else {
			pRemoveClipboardFormatListener.Call(host)
		}
	})
	return err
}

// ---------------------------------------------------------------- tray

var trayMenu []TrayItem

// TrayItem 托盘菜单项；ID 为 0 表示分隔线。
type TrayItem struct {
	ID    int
	Label string
}

// ShowTray 显示托盘图标。
func ShowTray(items []TrayItem) error {
	trayMenu = items
	var err error
	Invoke(func() { err = addTray() })
	return err
}

// RemoveTray 移除托盘图标。
func RemoveTray() {
	if host == 0 {
		return
	}
	Invoke(func() {
		if !trayAdded {
			return
		}
		nid := NOTIFYICONDATAW{CbSize: uint32(unsafe.Sizeof(NOTIFYICONDATAW{})), HWnd: host, UID: 1}
		pShellNotifyIconW.Call(NIM_DELETE, uintptr(unsafe.Pointer(&nid)))
		trayAdded = false
	})
}

var trayIcon uintptr

func addTray() error {
	if trayIcon == 0 {
		trayIcon = appIcon(true)
	}
	nid := NOTIFYICONDATAW{
		CbSize:           uint32(unsafe.Sizeof(NOTIFYICONDATAW{})),
		HWnd:             host,
		UID:              1,
		UFlags:           NIF_MESSAGE | NIF_ICON | NIF_TIP,
		UCallbackMessage: wmTray,
		HIcon:            trayIcon,
	}
	copyUTF16(nid.SzTip[:], trayTip)
	r, _, e := pShellNotifyIconW.Call(NIM_ADD, uintptr(unsafe.Pointer(&nid)))
	if r == 0 {
		return fmt.Errorf("托盘图标创建失败: %v", e)
	}
	trayAdded = true
	return nil
}

func showTrayMenu() {
	m, _, _ := pCreatePopupMenu.Call()
	if m == 0 {
		return
	}
	defer pDestroyMenu.Call(m)
	for _, it := range trayMenu {
		if it.ID == 0 {
			pAppendMenuW.Call(m, MF_SEPARATOR, 0, 0)
		} else {
			pAppendMenuW.Call(m, MF_STRING, uintptr(it.ID), uintptr(unsafe.Pointer(u16(it.Label))))
		}
	}
	var pt POINT
	pGetCursorPos.Call(uintptr(unsafe.Pointer(&pt)))
	pSetForegroundWindow.Call(host) // 菜单点外部能正确关闭
	cmd, _, _ := pTrackPopupMenu.Call(m, TPM_RIGHTBUTTON|TPM_RETURNCMD|TPM_NONOTIFY, uintptr(pt.X), uintptr(pt.Y), 0, host, 0)
	pPostMessageW.Call(host, WM_NULL, 0, 0)
	if cmd != 0 && handlers.OnTrayCommand != nil {
		go handlers.OnTrayCommand(int(cmd))
	}
}

func copyUTF16(dst []uint16, s string) {
	src, _ := utf16FromString(s)
	n := copy(dst[:len(dst)-1], src)
	dst[n] = 0
}

// ErrNotStarted UI 线程未启动。
var ErrNotStarted = errors.New("原生 UI 线程未启动")
