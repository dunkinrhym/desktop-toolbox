//go:build windows

package win

import (
	"fmt"
	"image"
	"time"
	"unsafe"
)

// 录屏时的悬浮计时条（含停止按钮）与录制区域红框。两者都设置为不被录入画面。

const (
	recbarClass = "DesktopToolboxRecBar"
	frameClass  = "DesktopToolboxRecFrame"
	timerRecbar = 2001
)

type recbar struct {
	hwnd    uintptr
	frame   uintptr
	start   time.Time
	font    uintptr
	fontB   uintptr
	scale   float64
	btn     RECT
	hover   bool
	onStop  func()
	stopped bool
}

var currentBar *recbar

// ShowRecordingUI 显示录制计时条与区域边框。region 为虚拟桌面坐标。onStop 在点击“停止”时调用。
func ShowRecordingUI(region image.Rectangle, onStop func()) error {
	var err error
	Invoke(func() {
		if currentBar != nil {
			currentBar.close()
		}
		err = createRecbar(region, onStop)
	})
	return err
}

// HideRecordingUI 关闭计时条与边框。
func HideRecordingUI() {
	Invoke(func() {
		if currentBar != nil {
			currentBar.close()
			currentBar = nil
		}
	})
}

func createRecbar(region image.Rectangle, onStop func()) error {
	if err := registerClass(recbarClass, loadCursor(IDC_ARROW), 0); err != nil {
		return err
	}
	if err := registerClass(frameClass, loadCursor(IDC_ARROW), 0); err != nil {
		return err
	}
	b := &recbar{start: time.Now(), onStop: onStop}
	mon := monitorAt(POINT{int32(region.Min.X + region.Dx()/2), int32(region.Min.Y + region.Dy()/2)})
	// 先按 96 DPI 创建再根据实际 DPI 调整尺寸
	hwnd, err := createWindow(recbarClass, WS_EX_TOPMOST|WS_EX_TOOLWINDOW|WS_EX_LAYERED|WS_EX_NOACTIVATE, WS_POPUP,
		mon.Min.X+mon.Dx()/2-100, mon.Min.Y+12, 200, 44, b.proc)
	if err != nil {
		return err
	}
	b.hwnd = hwnd
	b.scale = float64(dpiForWindow(hwnd)) / 96.0
	if b.scale < 1 {
		b.scale = 1
	}
	w, h := b.px(210), b.px(44)
	x := mon.Min.X + mon.Dx()/2 - w/2
	y := mon.Min.Y + b.px(12)
	// 若区域不是整屏，尽量放在区域上方外侧
	if region.Min.Y-h-b.px(8) >= mon.Min.Y && region != mon {
		y = region.Min.Y - h - b.px(8)
		x = region.Min.X + region.Dx()/2 - w/2
	}
	b.font = createFont(b.px(15), true)
	b.fontB = createFont(b.px(14), true)
	b.btn = RECT{int32(w - b.px(84)), int32(b.px(7)), int32(w - b.px(8)), int32(h - b.px(7))}
	pSetLayeredWindowAttributes.Call(hwnd, 0, 238, LWA_ALPHA)
	pSetWindowDisplayAffinity.Call(hwnd, WDA_EXCLUDEFROMCAPTURE) // Win10 2004+；旧系统失败无妨
	pSetWindowPos.Call(hwnd, HWND_TOPMOST, uintptr(int32(x)), uintptr(int32(y)), uintptr(w), uintptr(h), SWP_SHOWWINDOW|SWP_NOACTIVATE)
	pSetTimer.Call(hwnd, timerRecbar, 500, 0)

	// 区域边框（非整屏时）：窗口比区域大 3px，中间用颜色键透明，红框在录制区域之外
	if region != mon && region.Dx() > 0 {
		t := b.px(3)
		fx, fy, fw, fh := region.Min.X-t, region.Min.Y-t, region.Dx()+2*t, region.Dy()+2*t
		frame, err := createWindow(frameClass, WS_EX_TOPMOST|WS_EX_TOOLWINDOW|WS_EX_LAYERED|WS_EX_TRANSPARENT|WS_EX_NOACTIVATE, WS_POPUP,
			fx, fy, fw, fh, func(hwnd, msg, wp, lp uintptr) (uintptr, bool) {
				switch uint32(msg) {
				case WM_ERASEBKGND:
					return 1, true
				case WM_PAINT:
					var ps PAINTSTRUCT
					hdc, _, _ := pBeginPaint.Call(hwnd, uintptr(unsafe.Pointer(&ps)))
					all := RECT{0, 0, int32(fw), int32(fh)}
					fillRect(hdc, &all, rgb(255, 0, 255))
					frameRect(hdc, all, rgb(255, 59, 48), t)
					pEndPaint.Call(hwnd, uintptr(unsafe.Pointer(&ps)))
					return 0, true
				case WM_NCHITTEST:
					return ^uintptr(0), true // HTTRANSPARENT
				}
				return 0, false
			})
		if err == nil {
			b.frame = frame
			pSetLayeredWindowAttributes.Call(frame, rgb(255, 0, 255), 0, LWA_COLORKEY)
			pSetWindowDisplayAffinity.Call(frame, WDA_EXCLUDEFROMCAPTURE)
			pSetWindowPos.Call(frame, HWND_TOPMOST, uintptr(int32(fx)), uintptr(int32(fy)), uintptr(fw), uintptr(fh), SWP_SHOWWINDOW|SWP_NOACTIVATE)
		}
	}
	currentBar = b
	return nil
}

func (b *recbar) px(v int) int { return int(float64(v)*b.scale + 0.5) }

func (b *recbar) close() {
	pKillTimer.Call(b.hwnd, timerRecbar)
	destroyWindow(b.hwnd)
	if b.frame != 0 {
		destroyWindow(b.frame)
	}
	pDeleteObject.Call(b.font)
	pDeleteObject.Call(b.fontB)
}

func (b *recbar) paint() {
	var ps PAINTSTRUCT
	hdc, _, _ := pBeginPaint.Call(b.hwnd, uintptr(unsafe.Pointer(&ps)))
	defer pEndPaint.Call(b.hwnd, uintptr(unsafe.Pointer(&ps)))
	var wr RECT
	pGetWindowRect.Call(b.hwnd, uintptr(unsafe.Pointer(&wr)))
	w, h := int(wr.Right-wr.Left), int(wr.Bottom-wr.Top)
	// 双缓冲
	mdc, _, _ := pCreateCompatibleDC.Call(hdc)
	bm, _, _ := pCreateCompatibleBitmap.Call(hdc, uintptr(w), uintptr(h))
	pSelectObject.Call(mdc, bm)
	all := RECT{0, 0, int32(w), int32(h)}
	fillRect(mdc, &all, rgb(28, 28, 30))
	pSetBkMode.Call(mdc, TRANSPARENT)
	// 闪烁的红点
	el := time.Since(b.start)
	if (el/time.Millisecond/500)%2 == 0 {
		d := b.px(12)
		cy := h / 2
		ellipse(mdc, RECT{int32(b.px(14)), int32(cy - d/2), int32(b.px(14) + d), int32(cy + d/2)}, rgb(255, 59, 48))
	}
	secs := int(el.Seconds())
	tr := RECT{int32(b.px(34)), 0, b.btn.Left - int32(b.px(4)), int32(h)}
	pSelectObject.Call(mdc, b.font)
	pSetTextColor.Call(mdc, rgb(255, 255, 255))
	drawText(mdc, fmt.Sprintf("%02d:%02d:%02d", secs/3600, secs/60%60, secs%60), &tr, DT_LEFT|DT_VCENTER|DT_SINGLELINE|DT_NOPREFIX)
	fill := rgb(255, 59, 48)
	if b.hover {
		fill = rgb(230, 40, 30)
	}
	roundRect(mdc, b.btn, b.px(8), fill, 0, false)
	pSelectObject.Call(mdc, b.fontB)
	br := b.btn
	drawText(mdc, "停止", &br, DT_CENTER|DT_VCENTER|DT_SINGLELINE|DT_NOPREFIX)
	pBitBlt.Call(hdc, 0, 0, uintptr(w), uintptr(h), mdc, 0, 0, SRCCOPY)
	pDeleteDC.Call(mdc)
	pDeleteObject.Call(bm)
}

func (b *recbar) proc(hwnd, msg, wp, lp uintptr) (uintptr, bool) {
	switch uint32(msg) {
	case WM_ERASEBKGND:
		return 1, true
	case WM_PAINT:
		b.paint()
		return 0, true
	case WM_TIMER:
		invalidate(hwnd, nil)
		return 0, true
	case WM_MOUSEACTIVATE:
		return MA_NOACTIVATE, true
	case WM_NCHITTEST:
		var wr RECT
		pGetWindowRect.Call(hwnd, uintptr(unsafe.Pointer(&wr)))
		x, y := loword(lp)-int(wr.Left), hiword(lp)-int(wr.Top)
		if b.btn.contains(x, y) {
			return HTCLIENT, true
		}
		return HTCAPTION, true // 其余区域可拖动
	case WM_MOUSEMOVE:
		if !b.hover {
			b.hover = true
			invalidate(hwnd, nil)
		}
		return 0, true
	case WM_LBUTTONUP:
		x, y := loword(lp), hiword(lp)
		if b.btn.contains(x, y) && !b.stopped {
			b.stopped = true
			if b.onStop != nil {
				go b.onStop()
			}
		}
		return 0, true
	case WM_SETCURSOR:
		pSetCursor.Call(loadCursor(IDC_HAND))
		return 1, true
	}
	return 0, false
}
