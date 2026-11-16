//go:build windows

package win

import (
	"errors"
	"fmt"
	"image"
	"unsafe"
)

// 截图 / 录屏区域选择遮罩：全屏无边框置顶窗口，显示冻结的屏幕画面，
// 用户拖拽出矩形后显示工具栏。所有坐标均为物理像素（进程为 Per-Monitor V2 DPI 感知）。

// OverlayMode 遮罩模式。
type OverlayMode int

const (
	ModeScreenshot  OverlayMode = iota // 工具栏：复制 / 保存 / 取消
	ModeRecord                         // 工具栏：开始录制 / 取消
	ModeRecordFixed                    // 固定尺寸框跟随鼠标，单击放置
)

// OverlayResult 用户的选择结果。Rect 为相对于截图图像的坐标；Screen 为虚拟桌面坐标。
type OverlayResult struct {
	Action string // copy | save | desktop | ocr | record | cancel
	Rect   image.Rectangle
	Screen image.Rectangle
	Image  *image.RGBA // 截取到的整幅虚拟桌面画面
}

type obutton struct {
	label   string
	action  string
	primary bool
	danger  bool
	rc      RECT
}

type overlay struct {
	hwnd      uintptr
	origin    image.Point // 虚拟桌面左上角
	w, h      int
	img       *image.RGBA
	dcBright  uintptr
	bmBright  uintptr
	dcDim     uintptr
	bmDim     uintptr
	dcBuf     uintptr
	bmBuf     uintptr
	font      uintptr
	fontBold  uintptr
	scale     float64
	mode      OverlayMode
	fixedW    int
	fixedH    int
	state     int // 0 未选择 1 拖拽中 2 已选择 3 移动中
	sx, sy    int
	moveDX    int
	moveDY    int
	sel       RECT
	buttons   []obutton
	hover     int
	lastDecor RECT
	mouse     POINT
	done      func(OverlayResult)
	finished  bool
}

const overlayClass = "DesktopToolboxOverlay"

// ShowOverlay 截取整个虚拟桌面并显示选择遮罩。done 在用户完成选择后（遮罩关闭后）于新 goroutine 中调用。
// fixedW/fixedH 仅在 ModeRecordFixed 下使用。
func ShowOverlay(mode OverlayMode, fixedW, fixedH int, done func(OverlayResult)) error {
	if host == 0 {
		return ErrNotStarted
	}
	vs := VirtualScreen()
	img, err := CaptureScreen(vs)
	if err != nil {
		return err
	}
	var ret error
	Invoke(func() {
		ret = createOverlay(vs, img, mode, fixedW, fixedH, done)
	})
	return ret
}

func createOverlay(vs image.Rectangle, img *image.RGBA, mode OverlayMode, fixedW, fixedH int, done func(OverlayResult)) error {
	if err := registerClass(overlayClass, loadCursor(IDC_CROSS), CS_DBLCLKS); err != nil {
		return err
	}
	o := &overlay{origin: vs.Min, w: vs.Dx(), h: vs.Dy(), img: img, mode: mode, fixedW: fixedW, fixedH: fixedH, hover: -1, done: done}
	var err error
	if o.dcBright, o.bmBright, err = bitmapFromRGBA(img, 256); err != nil {
		return err
	}
	if o.dcDim, o.bmDim, err = bitmapFromRGBA(img, 140); err != nil {
		o.free()
		return err
	}
	screenDC, _, _ := pGetDC.Call(0)
	o.dcBuf, _, _ = pCreateCompatibleDC.Call(screenDC)
	o.bmBuf, _, _ = pCreateCompatibleBitmap.Call(screenDC, uintptr(o.w), uintptr(o.h))
	pReleaseDC.Call(0, screenDC)
	if o.dcBuf == 0 || o.bmBuf == 0 {
		o.free()
		return errors.New("创建绘图缓冲失败（内存不足？）")
	}
	pSelectObject.Call(o.dcBuf, o.bmBuf)

	hwnd, err := createWindow(overlayClass, WS_EX_TOPMOST|WS_EX_TOOLWINDOW, WS_POPUP,
		vs.Min.X, vs.Min.Y, o.w, o.h, o.proc)
	if err != nil {
		o.free()
		return err
	}
	o.hwnd = hwnd
	o.scale = float64(dpiForWindow(hwnd)) / 96.0
	if o.scale < 1 {
		o.scale = 1
	}
	o.font = createFont(o.px(14), false)
	o.fontBold = createFont(o.px(14), true)
	pGetCursorPos.Call(uintptr(unsafe.Pointer(&o.mouse)))
	o.mouse.X -= int32(o.origin.X)
	o.mouse.Y -= int32(o.origin.Y)
	if mode == ModeRecordFixed {
		o.placeFixed(int(o.mouse.X), int(o.mouse.Y))
	}
	pSetWindowPos.Call(hwnd, HWND_TOPMOST, uintptr(int32(vs.Min.X)), uintptr(int32(vs.Min.Y)), uintptr(o.w), uintptr(o.h), SWP_SHOWWINDOW)
	pShowWindow.Call(hwnd, SW_SHOW)
	forceForeground(hwnd)
	pSetFocus.Call(hwnd)
	o.lastDecor = o.decorBounds()
	return nil
}

func (o *overlay) px(v int) int { return int(float64(v)*o.scale + 0.5) }

func (o *overlay) free() {
	for _, p := range [][2]uintptr{{o.dcBright, o.bmBright}, {o.dcDim, o.bmDim}, {o.dcBuf, o.bmBuf}} {
		if p[0] != 0 {
			pDeleteDC.Call(p[0])
		}
		if p[1] != 0 {
			pDeleteObject.Call(p[1])
		}
	}
	if o.font != 0 {
		pDeleteObject.Call(o.font)
	}
	if o.fontBold != 0 {
		pDeleteObject.Call(o.fontBold)
	}
	o.dcBright, o.bmBright, o.dcDim, o.bmDim, o.dcBuf, o.bmBuf, o.font, o.fontBold = 0, 0, 0, 0, 0, 0, 0, 0
}

func (o *overlay) finish(action string) {
	if o.finished {
		return
	}
	o.finished = true
	res := OverlayResult{Action: action, Image: o.img}
	if action != "cancel" {
		r := image.Rect(int(o.sel.Left), int(o.sel.Top), int(o.sel.Right), int(o.sel.Bottom)).Intersect(o.img.Bounds())
		res.Rect = r
		res.Screen = r.Add(o.origin)
	}
	pReleaseCapture.Call()
	destroyWindow(o.hwnd)
	o.free()
	if o.done != nil {
		go o.done(res)
	}
}

func normRect(x0, y0, x1, y1 int) RECT {
	if x1 < x0 {
		x0, x1 = x1, x0
	}
	if y1 < y0 {
		y0, y1 = y1, y0
	}
	return RECT{int32(x0), int32(y0), int32(x1), int32(y1)}
}

func (o *overlay) placeFixed(cx, cy int) {
	w, h := o.fixedW, o.fixedH
	if w > o.w {
		w = o.w
	}
	if h > o.h {
		h = o.h
	}
	x := cx - w/2
	y := cy - h/2
	if x < 0 {
		x = 0
	}
	if y < 0 {
		y = 0
	}
	if x+w > o.w {
		x = o.w - w
	}
	if y+h > o.h {
		y = o.h - h
	}
	o.sel = RECT{int32(x), int32(y), int32(x + w), int32(y + h)}
}

func (o *overlay) selW() int { return int(o.sel.Right - o.sel.Left) }
func (o *overlay) selH() int { return int(o.sel.Bottom - o.sel.Top) }
func (o *overlay) hasSel() bool {
	return o.selW() > 0 && o.selH() > 0
}

func (o *overlay) layoutButtons() {
	o.buttons = o.buttons[:0]
	switch o.mode {
	case ModeScreenshot:
		o.buttons = append(o.buttons,
			obutton{label: "取消", action: "cancel"},
			obutton{label: "保存", action: "save"},
			obutton{label: "存桌面", action: "desktop"},
			obutton{label: "识字/翻译/改写", action: "ocr"},
			obutton{label: "复制", action: "copy", primary: true})
	default:
		o.buttons = append(o.buttons,
			obutton{label: "取消", action: "cancel"},
			obutton{label: "开始录制", action: "record", primary: true, danger: true})
	}
	bh := o.px(32)
	gap := o.px(8)
	pad := o.px(6)
	widths := make([]int, len(o.buttons))
	total := pad
	for i, b := range o.buttons {
		widths[i] = o.px(24) + o.px(14)*len([]rune(b.label))
		total += widths[i] + gap
	}
	total += pad - gap
	barH := bh + pad*2
	x := int(o.sel.Right) - total
	if x < 0 {
		x = 0
	}
	y := int(o.sel.Bottom) + o.px(8)
	if y+barH > o.h {
		y = int(o.sel.Top) - o.px(8) - barH
		if y < 0 {
			y = int(o.sel.Bottom) - barH - o.px(8)
		}
	}
	if x+total > o.w {
		x = o.w - total
	}
	cx := x + pad
	for i := range o.buttons {
		o.buttons[i].rc = RECT{int32(cx), int32(y + pad), int32(cx + widths[i]), int32(y + pad + bh)}
		cx += widths[i] + gap
	}
}

func (o *overlay) toolbarRect() RECT {
	if len(o.buttons) == 0 {
		return RECT{}
	}
	pad := int32(o.px(6))
	f, l := o.buttons[0].rc, o.buttons[len(o.buttons)-1].rc
	return RECT{f.Left - pad, f.Top - pad, l.Right + pad, l.Bottom + pad}
}

func (o *overlay) labelRect() RECT {
	lw, lh := o.px(110), o.px(24)
	x := int(o.sel.Left)
	y := int(o.sel.Top) - lh - o.px(6)
	if y < 0 {
		y = int(o.sel.Top) + o.px(6)
		x += o.px(6)
	}
	return RECT{int32(x), int32(y), int32(x + lw), int32(y + lh)}
}

func (o *overlay) hintRect() RECT {
	m := monitorAt(POINT{o.mouse.X + int32(o.origin.X), o.mouse.Y + int32(o.origin.Y)}).Sub(o.origin)
	w, h := o.px(440), o.px(40)
	cx := m.Min.X + m.Dx()/2
	return RECT{int32(cx - w/2), int32(m.Min.Y + o.px(40)), int32(cx + w/2), int32(m.Min.Y + o.px(40) + h)}
}

// decorBounds 当前所有叠加元素占据的区域（用于局部重绘）。
func (o *overlay) decorBounds() RECT {
	var r RECT
	if o.hasSel() {
		m := int32(o.px(4))
		r = RECT{o.sel.Left - m, o.sel.Top - m, o.sel.Right + m, o.sel.Bottom + m}
		r = unionRect(r, o.labelRect())
		if o.state == 2 {
			r = unionRect(r, o.toolbarRect())
		}
	}
	if o.state == 0 || o.mode == ModeRecordFixed && o.state != 2 {
		r = unionRect(r, o.hintRect())
	}
	return r
}

func (o *overlay) refresh() {
	cur := o.decorBounds()
	u := unionRect(o.lastDecor, cur)
	u.Left -= 2
	u.Top -= 2
	u.Right += 2
	u.Bottom += 2
	o.lastDecor = cur
	invalidate(o.hwnd, &u)
}

func (o *overlay) paint() {
	var ps PAINTSTRUCT
	hdc, _, _ := pBeginPaint.Call(o.hwnd, uintptr(unsafe.Pointer(&ps)))
	defer pEndPaint.Call(o.hwnd, uintptr(unsafe.Pointer(&ps)))
	rc := ps.RcPaint
	if rc.Right <= rc.Left || rc.Bottom <= rc.Top {
		return
	}
	bw, bh := uintptr(rc.Right-rc.Left), uintptr(rc.Bottom-rc.Top)
	pBitBlt.Call(o.dcBuf, uintptr(rc.Left), uintptr(rc.Top), bw, bh, o.dcDim, uintptr(rc.Left), uintptr(rc.Top), SRCCOPY)
	dc := o.dcBuf
	pSetBkMode.Call(dc, TRANSPARENT)
	if o.hasSel() {
		s := o.sel
		pBitBlt.Call(dc, uintptr(s.Left), uintptr(s.Top), uintptr(s.Right-s.Left), uintptr(s.Bottom-s.Top), o.dcBright, uintptr(s.Left), uintptr(s.Top), SRCCOPY)
		bw := o.px(2)
		frameRect(dc, RECT{s.Left - int32(bw), s.Top - int32(bw), s.Right + int32(bw), s.Bottom + int32(bw)}, rgb(10, 132, 255), bw)
		// 尺寸标签
		lr := o.labelRect()
		roundRect(dc, lr, o.px(8), rgb(30, 30, 32), 0, false)
		pSelectObject.Call(dc, o.font)
		pSetTextColor.Call(dc, rgb(255, 255, 255))
		drawText(dc, fmt.Sprintf("%d × %d", o.selW(), o.selH()), &lr, DT_CENTER|DT_VCENTER|DT_SINGLELINE|DT_NOPREFIX)
		if o.state == 2 {
			tr := o.toolbarRect()
			roundRect(dc, tr, o.px(12), rgb(255, 255, 255), rgb(210, 210, 215), true)
			for i, b := range o.buttons {
				fill := rgb(242, 242, 247)
				text := rgb(29, 29, 31)
				if b.primary {
					fill = rgb(10, 132, 255)
					if b.danger {
						fill = rgb(255, 59, 48)
					}
					text = rgb(255, 255, 255)
				}
				if i == o.hover {
					if b.primary {
						fill = rgb(0, 112, 230)
						if b.danger {
							fill = rgb(230, 40, 30)
						}
					} else {
						fill = rgb(225, 225, 232)
					}
				}
				roundRect(dc, b.rc, o.px(8), fill, 0, false)
				pSelectObject.Call(dc, o.fontBold)
				pSetTextColor.Call(dc, text)
				r := b.rc
				drawText(dc, b.label, &r, DT_CENTER|DT_VCENTER|DT_SINGLELINE|DT_NOPREFIX)
			}
		}
	}
	if o.state == 0 || o.mode == ModeRecordFixed && o.state != 2 {
		hr := o.hintRect()
		roundRect(dc, hr, o.px(12), rgb(30, 30, 32), 0, false)
		pSelectObject.Call(dc, o.font)
		pSetTextColor.Call(dc, rgb(255, 255, 255))
		hint := "拖动鼠标选择区域 · 右键或 Esc 取消"
		if o.mode == ModeRecordFixed {
			hint = fmt.Sprintf("移动鼠标放置 %d×%d 录制框，单击确定 · Esc 取消", o.fixedW, o.fixedH)
		}
		drawText(dc, hint, &hr, DT_CENTER|DT_VCENTER|DT_SINGLELINE|DT_NOPREFIX)
	}
	pBitBlt.Call(hdc, uintptr(rc.Left), uintptr(rc.Top), bw, bh, dc, uintptr(rc.Left), uintptr(rc.Top), SRCCOPY)
}

func (o *overlay) buttonAt(x, y int) int {
	if o.state != 2 {
		return -1
	}
	for i, b := range o.buttons {
		if b.rc.contains(x, y) {
			return i
		}
	}
	return -1
}

func (o *overlay) proc(hwnd, msg, wp, lp uintptr) (uintptr, bool) {
	switch uint32(msg) {
	case WM_ERASEBKGND:
		return 1, true
	case WM_PAINT:
		o.paint()
		return 0, true
	case WM_DPICHANGED:
		return 0, true // 窗口跨越多个显示器，不跟随 DPI 缩放
	case WM_SETCURSOR:
		var pt POINT
		pGetCursorPos.Call(uintptr(unsafe.Pointer(&pt)))
		x, y := int(pt.X)-o.origin.X, int(pt.Y)-o.origin.Y
		cur := uintptr(IDC_CROSS)
		if o.buttonAt(x, y) >= 0 {
			cur = IDC_HAND
		} else if o.state == 2 && o.sel.contains(x, y) && o.mode != ModeRecordFixed {
			cur = IDC_SIZEALL
		}
		pSetCursor.Call(loadCursor(cur))
		return 1, true
	case WM_LBUTTONDOWN:
		x, y := loword(lp), hiword(lp)
		if i := o.buttonAt(x, y); i >= 0 {
			o.finish(o.buttons[i].action)
			return 0, true
		}
		if o.mode == ModeRecordFixed {
			o.placeFixed(x, y)
			o.state = 2
			o.layoutButtons()
			o.refresh()
			return 0, true
		}
		if o.state == 2 && o.sel.contains(x, y) {
			o.state = 3
			o.moveDX, o.moveDY = x-int(o.sel.Left), y-int(o.sel.Top)
			pSetCapture.Call(hwnd)
			o.refresh()
			return 0, true
		}
		o.state = 1
		o.sx, o.sy = x, y
		o.sel = RECT{int32(x), int32(y), int32(x), int32(y)}
		pSetCapture.Call(hwnd)
		o.refresh()
		return 0, true
	case WM_MOUSEMOVE:
		x, y := loword(lp), hiword(lp)
		o.mouse = POINT{int32(x), int32(y)}
		switch o.state {
		case 1:
			if x < 0 {
				x = 0
			}
			if y < 0 {
				y = 0
			}
			if x > o.w {
				x = o.w
			}
			if y > o.h {
				y = o.h
			}
			o.sel = normRect(o.sx, o.sy, x, y)
			o.refresh()
		case 3:
			w, h := o.selW(), o.selH()
			nx, ny := x-o.moveDX, y-o.moveDY
			if nx < 0 {
				nx = 0
			}
			if ny < 0 {
				ny = 0
			}
			if nx+w > o.w {
				nx = o.w - w
			}
			if ny+h > o.h {
				ny = o.h - h
			}
			o.sel = RECT{int32(nx), int32(ny), int32(nx + w), int32(ny + h)}
			o.refresh()
		case 0:
			if o.mode == ModeRecordFixed {
				o.placeFixed(x, y)
			}
			o.refresh() // 提示条跟随所在显示器
		case 2:
			if h := o.buttonAt(x, y); h != o.hover {
				o.hover = h
				r := o.toolbarRect()
				invalidate(hwnd, &r)
			}
		}
		return 0, true
	case WM_LBUTTONUP:
		pReleaseCapture.Call()
		switch o.state {
		case 1:
			if o.selW() < o.px(4) || o.selH() < o.px(4) {
				// 单击：选中当前显示器整屏
				m := monitorAt(POINT{int32(loword(lp) + o.origin.X), int32(hiword(lp) + o.origin.Y)}).Sub(o.origin)
				o.sel = RECT{int32(m.Min.X), int32(m.Min.Y), int32(m.Max.X), int32(m.Max.Y)}
			}
			o.state = 2
			o.layoutButtons()
			o.refresh()
		case 3:
			o.state = 2
			o.layoutButtons()
			o.refresh()
		}
		return 0, true
	case WM_LBUTTONDBLCLK:
		x, y := loword(lp), hiword(lp)
		if o.state == 2 && o.sel.contains(x, y) {
			if o.mode == ModeScreenshot {
				o.finish("copy")
			} else {
				o.finish("record")
			}
		}
		return 0, true
	case WM_RBUTTONUP:
		if o.state == 2 && o.mode != ModeRecordFixed {
			o.state = 0
			o.sel = RECT{}
			o.buttons = nil
			o.refresh()
		} else {
			o.finish("cancel")
		}
		return 0, true
	case WM_KEYDOWN:
		ctrl := keyDown(VK_CONTROL)
		switch wp {
		case VK_ESCAPE:
			o.finish("cancel")
		case VK_RETURN:
			if o.state == 2 {
				if o.mode == ModeScreenshot {
					o.finish("copy")
				} else {
					o.finish("record")
				}
			}
		case 'C':
			if ctrl && o.state == 2 && o.mode == ModeScreenshot {
				o.finish("copy")
			}
		case 'S':
			if ctrl && o.state == 2 && o.mode == ModeScreenshot {
				o.finish("save")
			}
		}
		return 0, true
	case WM_CLOSE:
		o.finish("cancel")
		return 0, true
	}
	return 0, false
}

func keyDown(vk int) bool {
	r, _, _ := pGetKeyState.Call(uintptr(vk))
	return int16(r) < 0
}

// forceForeground 尽量把窗口置于前台。
func forceForeground(hwnd uintptr) {
	r, _, _ := pSetForegroundWindow.Call(hwnd)
	if r != 0 {
		return
	}
	// 经典技巧：模拟一次 Alt 键以解除前台锁定
	pKeybdEvent.Call(VK_MENU, 0, 0, 0)
	pKeybdEvent.Call(VK_MENU, 0, 2 /*KEYEVENTF_KEYUP*/, 0)
	pSetForegroundWindow.Call(hwnd)
	pBringWindowToTop.Call(hwnd)
}
