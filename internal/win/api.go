//go:build windows

// Package win 封装本程序用到的 Win32 API（纯 syscall，无 cgo）。
package win

import (
	"syscall"
	"unsafe"

	"golang.org/x/sys/windows"
)

var (
	user32   = windows.NewLazySystemDLL("user32.dll")
	gdi32    = windows.NewLazySystemDLL("gdi32.dll")
	kernel32 = windows.NewLazySystemDLL("kernel32.dll")
	shell32  = windows.NewLazySystemDLL("shell32.dll")
	comdlg32 = windows.NewLazySystemDLL("comdlg32.dll")

	pRegisterClassExW              = user32.NewProc("RegisterClassExW")
	pCreateWindowExW               = user32.NewProc("CreateWindowExW")
	pDefWindowProcW                = user32.NewProc("DefWindowProcW")
	pDestroyWindow                 = user32.NewProc("DestroyWindow")
	pGetMessageW                   = user32.NewProc("GetMessageW")
	pTranslateMessage              = user32.NewProc("TranslateMessage")
	pDispatchMessageW              = user32.NewProc("DispatchMessageW")
	pPostMessageW                  = user32.NewProc("PostMessageW")
	pPostQuitMessage               = user32.NewProc("PostQuitMessage")
	pRegisterHotKey                = user32.NewProc("RegisterHotKey")
	pUnregisterHotKey              = user32.NewProc("UnregisterHotKey")
	pAddClipboardFormatListener    = user32.NewProc("AddClipboardFormatListener")
	pRemoveClipboardFormatListener = user32.NewProc("RemoveClipboardFormatListener")
	pOpenClipboard                 = user32.NewProc("OpenClipboard")
	pCloseClipboard                = user32.NewProc("CloseClipboard")
	pEmptyClipboard                = user32.NewProc("EmptyClipboard")
	pGetClipboardData              = user32.NewProc("GetClipboardData")
	pSetClipboardData              = user32.NewProc("SetClipboardData")
	pIsClipboardFormatAvailable    = user32.NewProc("IsClipboardFormatAvailable")
	pRegisterClipboardFormatW      = user32.NewProc("RegisterClipboardFormatW")
	pGetClipboardSequenceNumber    = user32.NewProc("GetClipboardSequenceNumber")
	pGetSystemMetrics              = user32.NewProc("GetSystemMetrics")
	pGetDC                         = user32.NewProc("GetDC")
	pReleaseDC                     = user32.NewProc("ReleaseDC")
	pShowWindow                    = user32.NewProc("ShowWindow")
	pSetWindowPos                  = user32.NewProc("SetWindowPos")
	pSetForegroundWindow           = user32.NewProc("SetForegroundWindow")
	pGetForegroundWindow           = user32.NewProc("GetForegroundWindow")
	pBringWindowToTop              = user32.NewProc("BringWindowToTop")
	pSetFocus                      = user32.NewProc("SetFocus")
	pBeginPaint                    = user32.NewProc("BeginPaint")
	pEndPaint                      = user32.NewProc("EndPaint")
	pInvalidateRect                = user32.NewProc("InvalidateRect")
	pUpdateWindow                  = user32.NewProc("UpdateWindow")
	pSetCapture                    = user32.NewProc("SetCapture")
	pReleaseCapture                = user32.NewProc("ReleaseCapture")
	pLoadCursorW                   = user32.NewProc("LoadCursorW")
	pSetCursor                     = user32.NewProc("SetCursor")
	pFindWindowW                   = user32.NewProc("FindWindowW")
	pCreatePopupMenu               = user32.NewProc("CreatePopupMenu")
	pAppendMenuW                   = user32.NewProc("AppendMenuW")
	pTrackPopupMenu                = user32.NewProc("TrackPopupMenu")
	pDestroyMenu                   = user32.NewProc("DestroyMenu")
	pSetTimer                      = user32.NewProc("SetTimer")
	pKillTimer                     = user32.NewProc("KillTimer")
	pFillRect                      = user32.NewProc("FillRect")
	pDrawTextW                     = user32.NewProc("DrawTextW")
	pGetCursorPos                  = user32.NewProc("GetCursorPos")
	pMonitorFromPoint              = user32.NewProc("MonitorFromPoint")
	pGetMonitorInfoW               = user32.NewProc("GetMonitorInfoW")
	pSetWindowDisplayAffinity      = user32.NewProc("SetWindowDisplayAffinity")
	pSetLayeredWindowAttributes    = user32.NewProc("SetLayeredWindowAttributes")
	pGetDpiForWindow               = user32.NewProc("GetDpiForWindow")
	pSetProcessDpiAwarenessContext = user32.NewProc("SetProcessDpiAwarenessContext")
	pIsIconic                      = user32.NewProc("IsIconic")
	pIsWindowVisible               = user32.NewProc("IsWindowVisible")
	pKeybdEvent                    = user32.NewProc("keybd_event")
	pMessageBoxW                   = user32.NewProc("MessageBoxW")
	pDestroyIcon                   = user32.NewProc("DestroyIcon")
	pRegisterWindowMessageW        = user32.NewProc("RegisterWindowMessageW")
	pGetKeyState                   = user32.NewProc("GetKeyState")
	pGetWindowThreadProcessId      = user32.NewProc("GetWindowThreadProcessId")
	pAttachThreadInput             = user32.NewProc("AttachThreadInput")
	pGetWindowRect                 = user32.NewProc("GetWindowRect")
	pEnumDisplayMonitors           = user32.NewProc("EnumDisplayMonitors")
	pLoadImageW                    = user32.NewProc("LoadImageW")
	pSetWindowLongPtrW             = user32.NewProc("SetWindowLongPtrW")

	pCreateCompatibleDC     = gdi32.NewProc("CreateCompatibleDC")
	pCreateCompatibleBitmap = gdi32.NewProc("CreateCompatibleBitmap")
	pCreateDIBSection       = gdi32.NewProc("CreateDIBSection")
	pSelectObject           = gdi32.NewProc("SelectObject")
	pDeleteObject           = gdi32.NewProc("DeleteObject")
	pDeleteDC               = gdi32.NewProc("DeleteDC")
	pBitBlt                 = gdi32.NewProc("BitBlt")
	pCreateSolidBrush       = gdi32.NewProc("CreateSolidBrush")
	pCreatePen              = gdi32.NewProc("CreatePen")
	pCreateFontW            = gdi32.NewProc("CreateFontW")
	pSetTextColor           = gdi32.NewProc("SetTextColor")
	pSetBkMode              = gdi32.NewProc("SetBkMode")
	pGetStockObject         = gdi32.NewProc("GetStockObject")
	pRectangle              = gdi32.NewProc("Rectangle")
	pRoundRect              = gdi32.NewProc("RoundRect")
	pEllipse                = gdi32.NewProc("Ellipse")
	pGdiFlush               = gdi32.NewProc("GdiFlush")

	pGlobalAlloc        = kernel32.NewProc("GlobalAlloc")
	pGlobalFree         = kernel32.NewProc("GlobalFree")
	pGlobalLock         = kernel32.NewProc("GlobalLock")
	pGlobalUnlock       = kernel32.NewProc("GlobalUnlock")
	pGlobalSize         = kernel32.NewProc("GlobalSize")
	pGetModuleHandleW   = kernel32.NewProc("GetModuleHandleW")
	pGetCurrentThreadId = kernel32.NewProc("GetCurrentThreadId")
	pGetLogicalDrives   = kernel32.NewProc("GetLogicalDrives")
	pGetDriveTypeW      = kernel32.NewProc("GetDriveTypeW")

	pShellNotifyIconW           = shell32.NewProc("Shell_NotifyIconW")
	pShellExecuteW              = shell32.NewProc("ShellExecuteW")
	pExtractIconExW             = shell32.NewProc("ExtractIconExW")
	pSHOpenFolderAndSelectItems = shell32.NewProc("SHOpenFolderAndSelectItems")
	pILCreateFromPathW          = shell32.NewProc("ILCreateFromPathW")
	pILFree                     = shell32.NewProc("ILFree")

	pGetSaveFileNameW = comdlg32.NewProc("GetSaveFileNameW")
)

const (
	WM_DESTROY         = 0x0002
	WM_PAINT           = 0x000F
	WM_CLOSE           = 0x0010
	WM_ERASEBKGND      = 0x0014
	WM_SETCURSOR       = 0x0020
	WM_NCHITTEST       = 0x0084
	WM_KEYDOWN         = 0x0100
	WM_COMMAND         = 0x0111
	WM_TIMER           = 0x0113
	WM_MOUSEMOVE       = 0x0200
	WM_LBUTTONDOWN     = 0x0201
	WM_LBUTTONUP       = 0x0202
	WM_LBUTTONDBLCLK   = 0x0203
	WM_RBUTTONDOWN     = 0x0204
	WM_RBUTTONUP       = 0x0205
	WM_CONTEXTMENU     = 0x007B
	WM_HOTKEY          = 0x0312
	WM_CLIPBOARDUPDATE = 0x031D
	WM_DPICHANGED      = 0x02E0
	WM_ACTIVATE        = 0x0006
	WM_KILLFOCUS       = 0x0008
	WM_APP             = 0x8000
	WM_USER            = 0x0400
	WM_NULL            = 0x0000
	WM_MOUSEACTIVATE   = 0x0021
	MA_NOACTIVATE      = 3

	WS_POPUP          = 0x80000000
	WS_VISIBLE        = 0x10000000
	WS_EX_TOPMOST     = 0x00000008
	WS_EX_TOOLWINDOW  = 0x00000080
	WS_EX_LAYERED     = 0x00080000
	WS_EX_TRANSPARENT = 0x00000020
	WS_EX_NOACTIVATE  = 0x08000000

	CS_DBLCLKS = 0x0008
	CS_HREDRAW = 0x0002
	CS_VREDRAW = 0x0001

	SW_HIDE           = 0
	SW_SHOWNORMAL     = 1
	SW_SHOW           = 5
	SW_RESTORE        = 9
	SW_SHOWNA         = 8
	SW_SHOWNOACTIVATE = 4

	SWP_NOSIZE     = 0x0001
	SWP_NOMOVE     = 0x0002
	SWP_NOACTIVATE = 0x0010
	SWP_SHOWWINDOW = 0x0040
	HWND_TOPMOST   = ^uintptr(0) // -1

	SM_CXSCREEN        = 0
	SM_CYSCREEN        = 1
	SM_XVIRTUALSCREEN  = 76
	SM_YVIRTUALSCREEN  = 77
	SM_CXVIRTUALSCREEN = 78
	SM_CYVIRTUALSCREEN = 79

	SRCCOPY    = 0x00CC0020
	CAPTUREBLT = 0x40000000

	CF_UNICODETEXT = 13
	CF_DIB         = 8
	CF_DIBV5       = 17
	CF_HDROP       = 15
	GMEM_MOVEABLE  = 0x0002

	MOD_ALT      = 0x0001
	MOD_CONTROL  = 0x0002
	MOD_SHIFT    = 0x0004
	MOD_WIN      = 0x0008
	MOD_NOREPEAT = 0x4000

	VK_ESCAPE  = 0x1B
	VK_RETURN  = 0x0D
	VK_CONTROL = 0x11
	VK_MENU    = 0x12
	VK_SPACE   = 0x20

	IDC_ARROW   = 32512
	IDC_CROSS   = 32515
	IDC_HAND    = 32649
	IDC_SIZEALL = 32646

	DT_CENTER      = 0x0001
	DT_VCENTER     = 0x0004
	DT_SINGLELINE  = 0x0020
	DT_LEFT        = 0x0000
	DT_NOPREFIX    = 0x0800
	TRANSPARENT    = 1
	NULL_BRUSH     = 5
	NULL_PEN       = 8
	PS_SOLID       = 0
	PS_INSIDEFRAME = 6

	HTCAPTION = 2
	HTCLIENT  = 1

	LWA_COLORKEY = 0x1
	LWA_ALPHA    = 0x2

	WDA_EXCLUDEFROMCAPTURE = 0x11

	MF_STRING       = 0x0000
	MF_SEPARATOR    = 0x0800
	TPM_RIGHTBUTTON = 0x0002
	TPM_RETURNCMD   = 0x0100
	TPM_NONOTIFY    = 0x0080

	NIM_ADD     = 0
	NIM_MODIFY  = 1
	NIM_DELETE  = 2
	NIF_MESSAGE = 0x1
	NIF_ICON    = 0x2
	NIF_TIP     = 0x4
	NIF_INFO    = 0x10

	MB_OK            = 0x0
	MB_ICONERROR     = 0x10
	MB_ICONWARNING   = 0x30
	MB_TOPMOST       = 0x40000
	MB_SETFOREGROUND = 0x10000

	MONITOR_DEFAULTTONEAREST = 2
	HWND_MESSAGE             = ^uintptr(2) // -3
)

type POINT struct{ X, Y int32 }

type RECT struct{ Left, Top, Right, Bottom int32 }

type MSG struct {
	Hwnd    uintptr
	Message uint32
	WParam  uintptr
	LParam  uintptr
	Time    uint32
	Pt      POINT
	Private uint32
}

type WNDCLASSEXW struct {
	CbSize        uint32
	Style         uint32
	LpfnWndProc   uintptr
	CbClsExtra    int32
	CbWndExtra    int32
	HInstance     uintptr
	HIcon         uintptr
	HCursor       uintptr
	HbrBackground uintptr
	LpszMenuName  *uint16
	LpszClassName *uint16
	HIconSm       uintptr
}

type PAINTSTRUCT struct {
	Hdc         uintptr
	FErase      int32
	RcPaint     RECT
	FRestore    int32
	FIncUpdate  int32
	RgbReserved [32]byte
}

type BITMAPINFOHEADER struct {
	BiSize          uint32
	BiWidth         int32
	BiHeight        int32
	BiPlanes        uint16
	BiBitCount      uint16
	BiCompression   uint32
	BiSizeImage     uint32
	BiXPelsPerMeter int32
	BiYPelsPerMeter int32
	BiClrUsed       uint32
	BiClrImportant  uint32
}

type BITMAPINFO struct {
	BmiHeader BITMAPINFOHEADER
	BmiColors [1]uint32
}

type MONITORINFO struct {
	CbSize    uint32
	RcMonitor RECT
	RcWork    RECT
	DwFlags   uint32
}

type NOTIFYICONDATAW struct {
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

type OPENFILENAMEW struct {
	LStructSize       uint32
	HwndOwner         uintptr
	HInstance         uintptr
	LpstrFilter       *uint16
	LpstrCustomFilter *uint16
	NMaxCustFilter    uint32
	NFilterIndex      uint32
	LpstrFile         *uint16
	NMaxFile          uint32
	LpstrFileTitle    *uint16
	NMaxFileTitle     uint32
	LpstrInitialDir   *uint16
	LpstrTitle        *uint16
	Flags             uint32
	NFileOffset       uint16
	NFileExtension    uint16
	LpstrDefExt       *uint16
	LCustData         uintptr
	LpfnHook          uintptr
	LpTemplateName    *uint16
	PvReserved        uintptr
	DwReserved        uint32
	FlagsEx           uint32
}

func u16(s string) *uint16 {
	p, _ := syscall.UTF16PtrFromString(s)
	return p
}

func loword(v uintptr) int { return int(int16(v & 0xFFFF)) }
func hiword(v uintptr) int { return int(int16((v >> 16) & 0xFFFF)) }

func rgb(r, g, b uint8) uintptr { return uintptr(r) | uintptr(g)<<8 | uintptr(b)<<16 }

func getSystemMetrics(i int) int {
	r, _, _ := pGetSystemMetrics.Call(uintptr(i))
	return int(int32(r))
}

func currentThreadID() uint32 {
	r, _, _ := pGetCurrentThreadId.Call()
	return uint32(r)
}

func moduleHandle() uintptr {
	r, _, _ := pGetModuleHandleW.Call(0)
	return r
}

func loadCursor(id uintptr) uintptr {
	r, _, _ := pLoadCursorW.Call(0, id)
	return r
}

func invalidate(hwnd uintptr, rc *RECT) {
	if rc == nil {
		pInvalidateRect.Call(hwnd, 0, 0)
		return
	}
	pInvalidateRect.Call(hwnd, uintptr(unsafe.Pointer(rc)), 0)
}

func dpiForWindow(hwnd uintptr) int {
	if pGetDpiForWindow.Find() != nil {
		return 96
	}
	r, _, _ := pGetDpiForWindow.Call(hwnd)
	if r == 0 {
		return 96
	}
	return int(r)
}

// InitDPI 尽早设置 Per-Monitor V2 DPI 感知（清单中也已声明）。
func InitDPI() {
	if pSetProcessDpiAwarenessContext.Find() == nil {
		pSetProcessDpiAwarenessContext.Call(^uintptr(3)) // DPI_AWARENESS_CONTEXT_PER_MONITOR_AWARE_V2 = -4
	}
}

// MessageBox 弹出系统消息框。
func MessageBox(title, text string, warn bool) {
	flags := uintptr(MB_OK | MB_TOPMOST | MB_SETFOREGROUND)
	if warn {
		flags |= MB_ICONWARNING
	} else {
		flags |= MB_ICONERROR
	}
	pMessageBoxW.Call(0, uintptr(unsafe.Pointer(u16(text))), uintptr(unsafe.Pointer(u16(title))), flags)
}

func createFont(height int, bold bool) uintptr {
	weight := uintptr(400)
	if bold {
		weight = 600
	}
	h := int32(-height)
	r, _, _ := pCreateFontW.Call(uintptr(h), 0, 0, 0, weight, 0, 0, 0, 1 /*DEFAULT_CHARSET*/, 0, 0, 5 /*CLEARTYPE_QUALITY*/, 0,
		uintptr(unsafe.Pointer(u16("Microsoft YaHei UI"))))
	return r
}

func drawText(hdc uintptr, s string, rc *RECT, flags uintptr) {
	p, _ := syscall.UTF16FromString(s)
	pDrawTextW.Call(hdc, uintptr(unsafe.Pointer(&p[0])), uintptr(len(p)-1), uintptr(unsafe.Pointer(rc)), flags)
}

func fillRect(hdc uintptr, rc *RECT, color uintptr) {
	br, _, _ := pCreateSolidBrush.Call(color)
	pFillRect.Call(hdc, uintptr(unsafe.Pointer(rc)), br)
	pDeleteObject.Call(br)
}

func roundRect(hdc uintptr, rc RECT, radius int, fill uintptr, border uintptr, hasBorder bool) {
	br, _, _ := pCreateSolidBrush.Call(fill)
	var pen uintptr
	if hasBorder {
		pen, _, _ = pCreatePen.Call(PS_SOLID, 1, border)
	} else {
		pen, _, _ = pGetStockObject.Call(NULL_PEN)
	}
	ob, _, _ := pSelectObject.Call(hdc, br)
	op, _, _ := pSelectObject.Call(hdc, pen)
	pRoundRect.Call(hdc, uintptr(rc.Left), uintptr(rc.Top), uintptr(rc.Right), uintptr(rc.Bottom), uintptr(radius), uintptr(radius))
	pSelectObject.Call(hdc, ob)
	pSelectObject.Call(hdc, op)
	pDeleteObject.Call(br)
	if hasBorder {
		pDeleteObject.Call(pen)
	}
}

func frameRect(hdc uintptr, rc RECT, color uintptr, width int) {
	pen, _, _ := pCreatePen.Call(PS_INSIDEFRAME, uintptr(width), color)
	nb, _, _ := pGetStockObject.Call(NULL_BRUSH)
	ob, _, _ := pSelectObject.Call(hdc, nb)
	op, _, _ := pSelectObject.Call(hdc, pen)
	pRectangle.Call(hdc, uintptr(rc.Left), uintptr(rc.Top), uintptr(rc.Right), uintptr(rc.Bottom))
	pSelectObject.Call(hdc, ob)
	pSelectObject.Call(hdc, op)
	pDeleteObject.Call(pen)
}

func ellipse(hdc uintptr, rc RECT, color uintptr) {
	br, _, _ := pCreateSolidBrush.Call(color)
	pen, _, _ := pGetStockObject.Call(NULL_PEN)
	ob, _, _ := pSelectObject.Call(hdc, br)
	op, _, _ := pSelectObject.Call(hdc, pen)
	pEllipse.Call(hdc, uintptr(rc.Left), uintptr(rc.Top), uintptr(rc.Right), uintptr(rc.Bottom))
	pSelectObject.Call(hdc, ob)
	pSelectObject.Call(hdc, op)
	pDeleteObject.Call(br)
}

func (r RECT) contains(x, y int) bool {
	return x >= int(r.Left) && x < int(r.Right) && y >= int(r.Top) && y < int(r.Bottom)
}

func unionRect(a, b RECT) RECT {
	if a.Right <= a.Left || a.Bottom <= a.Top {
		return b
	}
	if b.Right <= b.Left || b.Bottom <= b.Top {
		return a
	}
	return RECT{min32(a.Left, b.Left), min32(a.Top, b.Top), max32(a.Right, b.Right), max32(a.Bottom, b.Bottom)}
}

func min32(a, b int32) int32 {
	if a < b {
		return a
	}
	return b
}

func max32(a, b int32) int32 {
	if a > b {
		return a
	}
	return b
}
