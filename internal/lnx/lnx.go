//go:build linux

// Package lnx 桌面工具箱的 Linux（X11 + GTK3）原生层：托盘、剪贴板历史监听、全局热键（XGrabKey）、
// 截屏、截图 / 录屏框选遮罩、录屏悬浮条。面向统信 UOS / 深度 DDE（X11 会话）。
//
// GTK 不是线程安全的：所有 GTK 调用都通过 OnMain 投递到 GTK 主线程执行（Wails 在主线程运行 gtk_main）。
// GTK 主线程上的 C 回调（托盘菜单、剪贴板变化、遮罩结果）一律在新的 goroutine 中执行 Go 处理函数，避免阻塞主循环。
package lnx

/*
#cgo pkg-config: gtk+-3.0 x11 xtst
#cgo CFLAGS: -Wno-deprecated-declarations -O2
#cgo LDFLAGS: -lm
#include <stdlib.h>
#include "lnx.h"
*/
import "C"

import (
	"errors"
	"sync"
	"sync/atomic"
	"unsafe"
)

// Handlers 原生事件回调（均在独立 goroutine 中调用）。
type Handlers struct {
	OnTray         func(id int)
	OnTrayActivate func()
	OnClipText     func(text string)
	OnClipPNG      func(png []byte)
	OnRecbarStop   func()
}

var (
	hmu      sync.RWMutex
	handlers Handlers
)

// SetHandlers 设置回调。
func SetHandlers(h Handlers) {
	hmu.Lock()
	handlers = h
	hmu.Unlock()
}

func getHandlers() Handlers {
	hmu.RLock()
	defer hmu.RUnlock()
	return handlers
}

// ------------------------------------------------------------------ 主线程调度

var (
	cbMu   sync.Mutex
	cbs    = map[uintptr]func(){}
	cbNext uintptr
)

// Post 异步在 GTK 主线程执行 fn。
func Post(fn func()) {
	cbMu.Lock()
	cbNext++
	id := cbNext
	cbs[id] = fn
	cbMu.Unlock()
	C.lnx_call_main(C.uintptr_t(id))
}

// OnMain 在 GTK 主线程执行 fn 并等待完成。不能在 GTK 主线程上调用（会死锁）。
func OnMain(fn func()) {
	done := make(chan struct{})
	Post(func() {
		defer close(done)
		fn()
	})
	<-done
}

//export goMainCall
func goMainCall(id C.uintptr_t) {
	cbMu.Lock()
	fn := cbs[uintptr(id)]
	delete(cbs, uintptr(id))
	cbMu.Unlock()
	if fn != nil {
		fn()
	}
}

// GtkInit 初始化 GTK（仅自检等不经过 Wails 的模式使用）。
func GtkInit() bool { return C.lnx_gtk_init() != 0 }

// GtkMain 运行 GTK 主循环（阻塞，须在主线程）。
func GtkMain() { C.lnx_gtk_main() }

// GtkQuit 退出 GTK 主循环。
func GtkQuit() { Post(func() { C.lnx_gtk_quit() }) }

// ------------------------------------------------------------------ 托盘

// TrayItem 托盘菜单项；ID 为 0 表示分隔线。
type TrayItem struct {
	ID    int
	Label string
}

// ShowTray 显示托盘图标与右键菜单（左键单击触发 OnTrayActivate）。
func ShowTray(tooltip, iconPath string, items []TrayItem) {
	OnMain(func() {
		ct, ci := C.CString(tooltip), C.CString(iconPath)
		defer C.free(unsafe.Pointer(ct))
		defer C.free(unsafe.Pointer(ci))
		C.lnx_tray_begin(ct, ci)
		for _, it := range items {
			cl := C.CString(it.Label)
			C.lnx_tray_item(C.int(it.ID), cl)
			C.free(unsafe.Pointer(cl))
		}
		C.lnx_tray_end()
	})
}

// RemoveTray 移除托盘图标。
func RemoveTray() { OnMain(func() { C.lnx_tray_remove() }) }

// TrayEmbedded 托盘图标是否已嵌入任务栏（没有系统托盘的桌面环境返回 false）。
func TrayEmbedded() bool {
	var ok bool
	OnMain(func() { ok = C.lnx_tray_embedded() != 0 })
	return ok
}

//export goTrayCmd
func goTrayCmd(id C.int) {
	if h := getHandlers().OnTray; h != nil {
		go h(int(id))
	}
}

//export goTrayActivate
func goTrayActivate() {
	if h := getHandlers().OnTrayActivate; h != nil {
		go h()
	}
}

// ------------------------------------------------------------------ 剪贴板

// WatchClipboard 开始 / 停止监听剪贴板变化（XFixes 选区通知）。
func WatchClipboard(on bool) {
	v := C.int(0)
	if on {
		v = 1
	}
	OnMain(func() { C.lnx_clip_watch(v) })
}

// SetClipboardText 写入文本。
func SetClipboardText(text string) error {
	OnMain(func() {
		cs := C.CString(text)
		C.lnx_clip_set_text(cs)
		C.free(unsafe.Pointer(cs))
	})
	return nil
}

// SetClipboardPNG 写入图片（PNG 数据；GTK 会同时提供 image/png、image/bmp、image/jpeg 等格式）。
func SetClipboardPNG(png []byte) error {
	if len(png) == 0 {
		return errors.New("空图片")
	}
	var ok bool
	OnMain(func() {
		p := C.CBytes(png)
		ok = C.lnx_clip_set_png((*C.uchar)(p), C.int(len(png))) != 0
		C.free(p)
	})
	if !ok {
		return errors.New("图片格式无效")
	}
	return nil
}

// ClipboardText 读取剪贴板文本。
func ClipboardText() (string, bool) {
	var s string
	var ok bool
	OnMain(func() {
		cs := C.lnx_clip_get_text()
		if cs != nil {
			s, ok = C.GoString(cs), true
			C.lnx_free(unsafe.Pointer(cs))
		}
	})
	return s, ok
}

// ClipboardPNG 读取剪贴板图片（转为 PNG）。
func ClipboardPNG() ([]byte, bool) {
	var out []byte
	OnMain(func() {
		var p *C.uchar
		var n C.int
		if C.lnx_clip_get_png(&p, &n) != 0 {
			out = C.GoBytes(unsafe.Pointer(p), n)
			C.lnx_free(unsafe.Pointer(p))
		}
	})
	return out, out != nil
}

//export goClipText
func goClipText(s *C.char) {
	t := C.GoString(s)
	if h := getHandlers().OnClipText; h != nil {
		go h(t)
	}
}

//export goClipPNG
func goClipPNG(p *C.uchar, n C.int) {
	b := C.GoBytes(unsafe.Pointer(p), n)
	if h := getHandlers().OnClipPNG; h != nil {
		go h(b)
	}
}

// ------------------------------------------------------------------ 窗口

// WindowState 按标题查找顶层窗口：是否可见、是否为当前激活窗口。
func WindowState(title string) (visible, active bool) {
	OnMain(func() {
		ct := C.CString(title)
		st := C.lnx_win_state(ct)
		C.free(unsafe.Pointer(ct))
		visible, active = st&1 != 0, st&2 != 0
	})
	return
}

// PresentWindow 显示并激活窗口（绕过窗口管理器的防抢焦点策略）。
func PresentWindow(title string) {
	OnMain(func() {
		ct := C.CString(title)
		C.lnx_win_present(ct)
		C.free(unsafe.Pointer(ct))
	})
}

// SetWindowIcon 设置窗口图标。
func SetWindowIcon(title, icon string) {
	OnMain(func() {
		ct, ci := C.CString(title), C.CString(icon)
		C.lnx_win_set_icon(ct, ci)
		C.free(unsafe.Pointer(ct))
		C.free(unsafe.Pointer(ci))
	})
}

// ------------------------------------------------------------------ 截屏

// CapturePNG 截取整个 X 屏幕（所有显示器），返回 PNG 与物理像素尺寸。
func CapturePNG() ([]byte, int, int, error) {
	var out []byte
	var w, h C.int
	OnMain(func() {
		var p *C.uchar
		var n C.int
		if C.lnx_capture_png(&p, &n, &w, &h) != 0 {
			out = C.GoBytes(unsafe.Pointer(p), n)
			C.lnx_free(unsafe.Pointer(p))
		}
	})
	if out == nil {
		return nil, 0, 0, errors.New("无法截取屏幕（需要 X11 会话）")
	}
	return out, int(w), int(h), nil
}

// ScreenSize 整个屏幕的物理像素尺寸与缩放倍数。
func ScreenSize() (w, h int, scale float64) {
	OnMain(func() {
		var cw, ch C.int
		var cs C.double
		C.lnx_screen_info(&cw, &ch, &cs)
		w, h, scale = int(cw), int(ch), float64(cs)
	})
	return
}

// Rect 物理像素矩形。
type Rect struct{ X, Y, W, H int }

// MonitorAtPointer 鼠标所在显示器（物理像素）。
func MonitorAtPointer() Rect {
	var r Rect
	OnMain(func() {
		var x, y, w, h C.int
		C.lnx_monitor_at_pointer(&x, &y, &w, &h)
		r = Rect{int(x), int(y), int(w), int(h)}
	})
	return r
}

// ------------------------------------------------------------------ 遮罩

// 遮罩模式。
const (
	ModeScreenshot  = 0
	ModeRecord      = 1
	ModeRecordFixed = 2
)

var actionNames = map[int]string{0: "cancel", 1: "copy", 2: "save", 3: "desktop", 4: "ocr", 5: "record"}

// OverlayResult 遮罩结果。Rect 为物理像素（相对整个 X 屏幕）；截图动作时 PNG 为选中区域。
type OverlayResult struct {
	Action string
	Rect   Rect
	PNG    []byte
}

var (
	ovMu    sync.Mutex
	ovCbs   = map[uint64]func(OverlayResult){}
	ovToken atomic.Uint64
)

// ShowOverlay 冻结当前屏幕画面并显示全屏框选遮罩；结束后在新 goroutine 中调用 done。
func ShowOverlay(mode, fixW, fixH int, done func(OverlayResult)) error {
	tok := ovToken.Add(1)
	ovMu.Lock()
	ovCbs[tok] = done
	ovMu.Unlock()
	var ok bool
	OnMain(func() { ok = C.lnx_overlay_show(C.int(mode), C.int(fixW), C.int(fixH), C.uint64_t(tok)) != 0 })
	if !ok {
		ovMu.Lock()
		delete(ovCbs, tok)
		ovMu.Unlock()
		return errors.New("无法截取屏幕或已有框选窗口（需要 X11 会话）")
	}
	return nil
}

// OverlayActive 是否正在显示遮罩。
func OverlayActive() bool {
	var ok bool
	OnMain(func() { ok = C.lnx_overlay_active() != 0 })
	return ok
}

//export goOverlayDone
func goOverlayDone(tok C.uint64_t, action C.int, buf *C.uchar, n C.int, x, y, w, h C.int) {
	res := OverlayResult{Action: actionNames[int(action)], Rect: Rect{int(x), int(y), int(w), int(h)}}
	if buf != nil && n > 0 {
		res.PNG = C.GoBytes(unsafe.Pointer(buf), n)
	}
	ovMu.Lock()
	cb := ovCbs[uint64(tok)]
	delete(ovCbs, uint64(tok))
	ovMu.Unlock()
	if cb != nil {
		go cb(res)
	}
}

// ------------------------------------------------------------------ 录屏悬浮条

// ShowRecordingBar 在录制区域外显示计时与“停止录制”按钮（点击触发 OnRecbarStop）。
func ShowRecordingBar(r Rect, startedMs int64) {
	OnMain(func() { C.lnx_recbar_show(C.int(r.X), C.int(r.Y), C.int(r.W), C.int(r.H), C.int64_t(startedMs)) })
}

// HideRecordingBar 隐藏悬浮条。
func HideRecordingBar() { OnMain(func() { C.lnx_recbar_hide() }) }

//export goRecbarStop
func goRecbarStop() {
	if h := getHandlers().OnRecbarStop; h != nil {
		go h()
	}
}

// ------------------------------------------------------------------ 杂项

// Alert 非模态提示框。
func Alert(title, msg string) {
	Post(func() {
		ct, cm := C.CString(title), C.CString(msg)
		C.lnx_alert(ct, cm)
		C.free(unsafe.Pointer(ct))
		C.free(unsafe.Pointer(cm))
	})
}

// ShowURI 用系统默认程序打开 URI（file:// 等，经由 GIO）。
func ShowURI(uri string) error {
	var msg string
	var ok bool
	OnMain(func() {
		cu := C.CString(uri)
		defer C.free(unsafe.Pointer(cu))
		var ce *C.char
		ok = C.lnx_show_uri(cu, &ce) != 0
		if ce != nil {
			msg = C.GoString(ce)
			C.lnx_free(unsafe.Pointer(ce))
		}
	})
	if !ok {
		if msg == "" {
			msg = "没有可用的打开方式"
		}
		return errors.New(msg)
	}
	return nil
}

// TestWindow 显示 / 关闭自检用的彩色测试窗口。
func TestWindow(show bool) {
	v := C.int(0)
	if show {
		v = 1
	}
	OnMain(func() { C.lnx_test_window(v) })
}
