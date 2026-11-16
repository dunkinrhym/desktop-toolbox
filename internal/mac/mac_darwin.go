//go:build darwin && cgo

package mac

/*
#cgo CFLAGS: -x objective-c -fobjc-arc -Wno-deprecated-declarations
#cgo LDFLAGS: -framework Cocoa -framework Carbon -framework CoreGraphics
#include <stdlib.h>
#include "native.h"
*/
import "C"

import (
	"errors"
	"fmt"
	"sync"
	"unsafe"
)

var (
	hMu      sync.Mutex
	handlers Handlers
	regionCb func(Region)
	recStop  func()
)

func getHandlers() Handlers {
	hMu.Lock()
	defer hMu.Unlock()
	return handlers
}

// Init 保存回调并安装“点击程序坞图标重新打开窗口”的处理。需在 Wails 启动后（OnStartup 中）调用。
func Init(h Handlers) {
	hMu.Lock()
	handlers = h
	hMu.Unlock()
	C.dtInstallReopenHandler()
}

//export dtGoHotkey
func dtGoHotkey(id C.int) {
	if f := getHandlers().OnHotkey; f != nil {
		go f(int(id)) // 回调发生在主线程，立即转到 goroutine，避免阻塞 AppKit
	}
}

//export dtGoTray
func dtGoTray(id C.int) {
	if f := getHandlers().OnTray; f != nil {
		go f(int(id))
	}
}

//export dtGoReopen
func dtGoReopen() {
	if f := getHandlers().OnReopen; f != nil {
		go f()
	}
}

//export dtGoRegion
func dtGoRegion(ok C.int, x, y, w, h, sw, sh, scale C.double, idx C.int) {
	hMu.Lock()
	cb := regionCb
	regionCb = nil
	hMu.Unlock()
	if cb == nil {
		return
	}
	r := Region{OK: ok != 0, X: float64(x), Y: float64(y), W: float64(w), H: float64(h),
		ScreenW: float64(sw), ScreenH: float64(sh), Scale: float64(scale), DisplayIndex: int(idx)}
	go cb(r)
}

//export dtGoRecStop
func dtGoRecStop() {
	hMu.Lock()
	f := recStop
	hMu.Unlock()
	if f != nil {
		go f()
	}
}

// ActivateApp 显示并激活本程序（取消隐藏）。
func ActivateApp() { C.dtActivateApp() }

// HideApp 隐藏本程序，焦点回到之前的应用（剪贴板小窗口复制后可直接 ⌘V）。
func HideApp() { C.dtHideApp() }

// IsAppActive 本程序是否为前台应用。
func IsAppActive() bool { return C.dtIsAppActive() != 0 }

// IsMainWindowVisible 主窗口是否可见。
func IsMainWindowVisible() bool { return C.dtIsMainWindowVisible() != 0 }

// Alert 弹出提示框（异步）。
func Alert(title, msg string) {
	ct, cm := C.CString(title), C.CString(msg)
	defer C.free(unsafe.Pointer(ct))
	defer C.free(unsafe.Pointer(cm))
	C.dtAlert(ct, cm)
}

// RegisterHotkey 注册全局热键（Carbon）。
func RegisterHotkey(id int, keyCode, mods uint32) error {
	st := C.dtRegisterHotkey(C.int(id), C.uint(keyCode), C.uint(mods))
	if st != 0 {
		return fmt.Errorf("RegisterEventHotKey 失败（OSStatus %d）", int(st))
	}
	return nil
}

// UnregisterHotkeys 注销全部热键。
func UnregisterHotkeys() { C.dtUnregisterHotkeys() }

// ShowTray 创建/更新菜单栏图标及菜单。ID 为 0 的项是分隔线。
func ShowTray(tooltip string, items []TrayItem) error {
	C.dtTrayBegin()
	for _, it := range items {
		cl, ck := C.CString(it.Label), C.CString(it.Key)
		C.dtTrayAdd(C.int(it.ID), cl, ck, C.uint(it.KeyMods))
		C.free(unsafe.Pointer(cl))
		C.free(unsafe.Pointer(ck))
	}
	ct := C.CString(tooltip)
	defer C.free(unsafe.Pointer(ct))
	C.dtTrayCommit(ct)
	return nil
}

// SetTrayTitle 设置菜单栏图标旁的文字（空串清除）。
func SetTrayTitle(t string) {
	c := C.CString(t)
	defer C.free(unsafe.Pointer(c))
	C.dtTraySetTitle(c)
}

// RemoveTray 移除菜单栏图标。
func RemoveTray() { C.dtTrayRemove() }

// ClipboardChangeCount NSPasteboard changeCount。
func ClipboardChangeCount() int64 { return int64(C.dtClipChangeCount()) }

// ReadClipboard 读取剪贴板当前内容。
func ReadClipboard() (kind ClipKind, text string, png []byte) {
	var ct *C.char
	var cp unsafe.Pointer
	var n C.int
	k := C.dtClipRead(&ct, &cp, &n)
	if ct != nil {
		text = C.GoString(ct)
		C.free(unsafe.Pointer(ct))
	}
	if cp != nil {
		png = C.GoBytes(cp, n)
		C.free(cp)
	}
	return ClipKind(k), text, png
}

// SetClipboardText 写入文本。
func SetClipboardText(s string) error {
	c := C.CString(s)
	defer C.free(unsafe.Pointer(c))
	if C.dtClipSetText(c) != 0 {
		return errors.New("无法写入剪贴板")
	}
	return nil
}

// SetClipboardPNG 写入图片（同时提供 PNG 与 TIFF）。
func SetClipboardPNG(png []byte) error {
	if len(png) == 0 {
		return errors.New("空图片")
	}
	c := C.CBytes(png)
	defer C.free(c)
	switch C.dtClipSetPNG(c, C.int(len(png))) {
	case 0:
		return nil
	case 1:
		return errors.New("图片格式无效")
	default:
		return errors.New("无法写入剪贴板")
	}
}

// ScreenCaptureAllowed 是否已获得“屏幕录制”权限（不弹窗）。
func ScreenCaptureAllowed() bool { return C.dtScreenCaptureAllowed() != 0 }

// RequestScreenCapture 请求“屏幕录制”权限（首次会弹出系统对话框）。
func RequestScreenCapture() bool { return C.dtRequestScreenCapture() != 0 }

// SelectRegion 显示区域选择遮罩（鼠标所在屏幕）。fixedW/fixedH 为像素，0 表示自由框选。
func SelectRegion(fixedW, fixedH int, done func(Region)) {
	hMu.Lock()
	regionCb = done
	hMu.Unlock()
	C.dtSelectRegion(C.int(fixedW), C.int(fixedH))
}

// ScreenUnderMouse 鼠标所在屏幕（整屏区域）。
func ScreenUnderMouse() Region {
	var w, h, s C.double
	var idx C.int
	C.dtScreenUnderMouse(&w, &h, &s, &idx)
	return Region{OK: true, W: float64(w), H: float64(h), ScreenW: float64(w), ScreenH: float64(h), Scale: float64(s), DisplayIndex: int(idx)}
}

// ShowRecordingBar 显示录屏悬浮条（计时 + 停止按钮，不会被录进视频），菜单栏图标同时显示计时。
func ShowRecordingBar(startedUnix float64, onStop func()) {
	hMu.Lock()
	recStop = onStop
	hMu.Unlock()
	C.dtShowRecordingBar(C.double(startedUnix))
}

// HideRecordingBar 隐藏录屏悬浮条。
func HideRecordingBar() {
	hMu.Lock()
	recStop = nil
	hMu.Unlock()
	C.dtHideRecordingBar()
}
