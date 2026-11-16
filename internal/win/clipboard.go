//go:build windows

package win

import (
	"bytes"
	"errors"
	"fmt"
	"image"
	"image/png"
	"syscall"
	"time"
	"unsafe"

	"desktoptoolbox/internal/dib"
)

var (
	syscallNewCallback = syscall.NewCallback
	utf16FromString    = syscall.UTF16FromString

	cfPNG          uint32
	cfExclude      uint32
	cfCanInclude   uint32
	cfViewerIgnore uint32
	ignoreSeq      uint32
)

func init() {
	r, _, _ := pRegisterClipboardFormatW.Call(uintptr(unsafe.Pointer(u16("PNG"))))
	cfPNG = uint32(r)
	r, _, _ = pRegisterClipboardFormatW.Call(uintptr(unsafe.Pointer(u16("ExcludeClipboardContentFromMonitorProcessing"))))
	cfExclude = uint32(r)
	r, _, _ = pRegisterClipboardFormatW.Call(uintptr(unsafe.Pointer(u16("CanIncludeInClipboardHistory"))))
	cfCanInclude = uint32(r)
	r, _, _ = pRegisterClipboardFormatW.Call(uintptr(unsafe.Pointer(u16("Clipboard Viewer Ignore"))))
	cfViewerIgnore = uint32(r)
}

func available(f uint32) bool {
	if f == 0 {
		return false
	}
	r, _, _ := pIsClipboardFormatAvailable.Call(uintptr(f))
	return r != 0
}

func openClipboard() error {
	var lastErr error
	for i := 0; i < 20; i++ {
		r, _, err := pOpenClipboard.Call(host)
		if r != 0 {
			return nil
		}
		lastErr = err
		time.Sleep(15 * time.Millisecond)
	}
	return fmt.Errorf("剪贴板被其他程序占用: %v", lastErr)
}

func globalBytes(h uintptr) []byte {
	if h == 0 {
		return nil
	}
	p, _, _ := pGlobalLock.Call(h)
	if p == 0 {
		return nil
	}
	defer pGlobalUnlock.Call(h)
	n, _, _ := pGlobalSize.Call(h)
	if n == 0 {
		return nil
	}
	out := make([]byte, n)
	copy(out, unsafe.Slice((*byte)(foreignPtr(p)), n))
	return out
}

// readClipboardAndNotify 在 UI 线程上调用。
func readClipboardAndNotify() {
	seq, _, _ := pGetClipboardSequenceNumber.Call()
	if uint32(seq) == ignoreSeq && ignoreSeq != 0 {
		return // 本程序自己写入的内容
	}
	if available(cfExclude) || available(cfViewerIgnore) {
		return // 密码管理器等要求不记录
	}
	if openClipboard() != nil {
		return
	}
	defer pCloseClipboard.Call()
	if available(cfCanInclude) {
		if b := globalBytes(getData(cfCanInclude)); len(b) >= 4 && b[0] == 0 && b[1] == 0 && b[2] == 0 && b[3] == 0 {
			return
		}
	}
	if available(CF_UNICODETEXT) {
		if text := readText(); text != "" {
			if handlers.OnClipboardText != nil {
				go handlers.OnClipboardText(text)
			}
		}
		return
	}
	var pngData []byte
	if available(cfPNG) {
		b := globalBytes(getData(cfPNG))
		if _, err := png.DecodeConfig(bytes.NewReader(b)); err == nil {
			pngData = b
		}
	}
	if pngData == nil {
		var raw []byte
		if available(CF_DIBV5) {
			raw = globalBytes(getData(CF_DIBV5))
		}
		if raw == nil && available(CF_DIB) {
			raw = globalBytes(getData(CF_DIB))
		}
		if raw != nil {
			if img, err := dib.Decode(raw); err == nil {
				var buf bytes.Buffer
				if png.Encode(&buf, img) == nil {
					pngData = buf.Bytes()
				}
			}
		}
	}
	if pngData != nil && handlers.OnClipboardPNG != nil {
		go handlers.OnClipboardPNG(pngData)
	}
}

func getData(f uint32) uintptr {
	h, _, _ := pGetClipboardData.Call(uintptr(f))
	return h
}

func readText() string {
	h := getData(CF_UNICODETEXT)
	if h == 0 {
		return ""
	}
	p, _, _ := pGlobalLock.Call(h)
	if p == 0 {
		return ""
	}
	defer pGlobalUnlock.Call(h)
	n, _, _ := pGlobalSize.Call(h)
	u := unsafe.Slice((*uint16)(foreignPtr(p)), n/2)
	for i, c := range u {
		if c == 0 {
			u = u[:i]
			break
		}
	}
	return syscall.UTF16ToString(u)
}

func setGlobal(format uint32, data []byte) error {
	h, _, err := pGlobalAlloc.Call(GMEM_MOVEABLE, uintptr(len(data)))
	if h == 0 {
		return fmt.Errorf("GlobalAlloc: %v", err)
	}
	p, _, _ := pGlobalLock.Call(h)
	if p == 0 {
		pGlobalFree.Call(h)
		return errors.New("GlobalLock 失败")
	}
	copy(unsafe.Slice((*byte)(foreignPtr(p)), len(data)), data)
	pGlobalUnlock.Call(h)
	r, _, err := pSetClipboardData.Call(uintptr(format), h)
	if r == 0 {
		pGlobalFree.Call(h)
		return fmt.Errorf("SetClipboardData: %v", err)
	}
	return nil
}

func markOwnWrite() {
	seq, _, _ := pGetClipboardSequenceNumber.Call()
	ignoreSeq = uint32(seq)
}

// SetClipboardText 写入文本到剪贴板。
func SetClipboardText(text string) error {
	var err error
	Invoke(func() {
		if err = openClipboard(); err != nil {
			return
		}
		defer func() { pCloseClipboard.Call(); markOwnWrite() }()
		pEmptyClipboard.Call()
		u, e := syscall.UTF16FromString(text)
		if e != nil { // 含 NUL 字符
			u, _ = syscall.UTF16FromString(string(bytes.ReplaceAll([]byte(text), []byte{0}, nil)))
		}
		b := unsafe.Slice((*byte)(unsafe.Pointer(&u[0])), len(u)*2)
		err = setGlobal(CF_UNICODETEXT, b)
	})
	return err
}

// SetClipboardImage 写入图片：同时提供 CF_DIB（Word/画图/微信等通用）与 PNG 格式。
func SetClipboardImage(img image.Image, pngData []byte) error {
	if pngData == nil {
		var buf bytes.Buffer
		if err := png.Encode(&buf, img); err != nil {
			return err
		}
		pngData = buf.Bytes()
	}
	if img == nil {
		var err error
		img, err = png.Decode(bytes.NewReader(pngData))
		if err != nil {
			return fmt.Errorf("图片数据无效: %v", err)
		}
	}
	dibData := dib.Encode24(img)
	var err error
	Invoke(func() {
		if err = openClipboard(); err != nil {
			return
		}
		defer func() { pCloseClipboard.Call(); markOwnWrite() }()
		pEmptyClipboard.Call()
		if err = setGlobal(CF_DIB, dibData); err != nil {
			return
		}
		if cfPNG != 0 {
			_ = setGlobal(cfPNG, pngData)
		}
	})
	return err
}

// foreignPtr 把非 Go 堆内存地址（GlobalLock 返回值）转换为指针。
func foreignPtr(p uintptr) unsafe.Pointer { return *(*unsafe.Pointer)(unsafe.Pointer(&p)) }
