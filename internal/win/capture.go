//go:build windows

package win

import (
	"errors"
	"image"
	"unsafe"
)

// VirtualScreen 返回虚拟桌面（所有显示器）的物理像素矩形。
func VirtualScreen() image.Rectangle {
	x := getSystemMetrics(SM_XVIRTUALSCREEN)
	y := getSystemMetrics(SM_YVIRTUALSCREEN)
	w := getSystemMetrics(SM_CXVIRTUALSCREEN)
	h := getSystemMetrics(SM_CYVIRTUALSCREEN)
	return image.Rect(x, y, x+w, y+h)
}

// CursorMonitor 返回鼠标所在显示器的矩形（虚拟桌面坐标）。
func CursorMonitor() image.Rectangle {
	var pt POINT
	pGetCursorPos.Call(uintptr(unsafe.Pointer(&pt)))
	return monitorAt(pt)
}

func monitorAt(pt POINT) image.Rectangle {
	m, _, _ := pMonitorFromPoint.Call(uintptr(uint32(pt.X))|uintptr(uint32(pt.Y))<<32, MONITOR_DEFAULTTONEAREST)
	mi := MONITORINFO{CbSize: uint32(unsafe.Sizeof(MONITORINFO{}))}
	if m == 0 {
		return image.Rect(0, 0, getSystemMetrics(SM_CXSCREEN), getSystemMetrics(SM_CYSCREEN))
	}
	pGetMonitorInfoW.Call(m, uintptr(unsafe.Pointer(&mi)))
	r := mi.RcMonitor
	return image.Rect(int(r.Left), int(r.Top), int(r.Right), int(r.Bottom))
}

// CaptureScreen 截取虚拟桌面上的矩形区域，返回 RGBA 图像（不透明）。
func CaptureScreen(r image.Rectangle) (*image.RGBA, error) {
	w, h := r.Dx(), r.Dy()
	if w <= 0 || h <= 0 {
		return nil, errors.New("截图区域无效")
	}
	screenDC, _, _ := pGetDC.Call(0)
	if screenDC == 0 {
		return nil, errors.New("GetDC 失败")
	}
	defer pReleaseDC.Call(0, screenDC)
	memDC, _, _ := pCreateCompatibleDC.Call(screenDC)
	if memDC == 0 {
		return nil, errors.New("CreateCompatibleDC 失败")
	}
	defer pDeleteDC.Call(memDC)
	bmi := BITMAPINFO{BmiHeader: BITMAPINFOHEADER{
		BiSize: uint32(unsafe.Sizeof(BITMAPINFOHEADER{})), BiWidth: int32(w), BiHeight: -int32(h),
		BiPlanes: 1, BiBitCount: 32,
	}}
	var bits unsafe.Pointer
	hbm, _, _ := pCreateDIBSection.Call(memDC, uintptr(unsafe.Pointer(&bmi)), 0, uintptr(unsafe.Pointer(&bits)), 0, 0)
	if hbm == 0 || bits == nil {
		return nil, errors.New("CreateDIBSection 失败（内存不足？）")
	}
	defer pDeleteObject.Call(hbm)
	old, _, _ := pSelectObject.Call(memDC, hbm)
	ok, _, _ := pBitBlt.Call(memDC, 0, 0, uintptr(w), uintptr(h), screenDC, uintptr(int32(r.Min.X)), uintptr(int32(r.Min.Y)), SRCCOPY|CAPTUREBLT)
	pGdiFlush.Call()
	pSelectObject.Call(memDC, old)
	if ok == 0 {
		return nil, errors.New("BitBlt 截屏失败")
	}
	src := unsafe.Slice((*byte)(bits), w*h*4)
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	for i := 0; i < len(src); i += 4 {
		img.Pix[i] = src[i+2]
		img.Pix[i+1] = src[i+1]
		img.Pix[i+2] = src[i]
		img.Pix[i+3] = 255
	}
	return img, nil
}

// bitmapFromRGBA 创建一个 32 位 DIB 位图并选入新的内存 DC；dim 为 true 时整体变暗。
func bitmapFromRGBA(img *image.RGBA, dimFactor int) (dc, hbm uintptr, err error) {
	w, h := img.Bounds().Dx(), img.Bounds().Dy()
	screenDC, _, _ := pGetDC.Call(0)
	defer pReleaseDC.Call(0, screenDC)
	dc, _, _ = pCreateCompatibleDC.Call(screenDC)
	if dc == 0 {
		return 0, 0, errors.New("CreateCompatibleDC 失败")
	}
	bmi := BITMAPINFO{BmiHeader: BITMAPINFOHEADER{
		BiSize: uint32(unsafe.Sizeof(BITMAPINFOHEADER{})), BiWidth: int32(w), BiHeight: -int32(h),
		BiPlanes: 1, BiBitCount: 32,
	}}
	var bits unsafe.Pointer
	hbm, _, _ = pCreateDIBSection.Call(dc, uintptr(unsafe.Pointer(&bmi)), 0, uintptr(unsafe.Pointer(&bits)), 0, 0)
	if hbm == 0 || bits == nil {
		pDeleteDC.Call(dc)
		return 0, 0, errors.New("CreateDIBSection 失败（内存不足？）")
	}
	dst := unsafe.Slice((*byte)(bits), w*h*4)
	p := img.Pix
	if dimFactor >= 256 {
		for i := 0; i < len(dst); i += 4 {
			dst[i], dst[i+1], dst[i+2], dst[i+3] = p[i+2], p[i+1], p[i], 255
		}
	} else {
		f := uint32(dimFactor)
		for i := 0; i < len(dst); i += 4 {
			dst[i] = byte(uint32(p[i+2]) * f >> 8)
			dst[i+1] = byte(uint32(p[i+1]) * f >> 8)
			dst[i+2] = byte(uint32(p[i]) * f >> 8)
			dst[i+3] = 255
		}
	}
	pSelectObject.Call(dc, hbm)
	return dc, hbm, nil
}
