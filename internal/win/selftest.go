//go:build windows

package win

import (
	"bytes"
	"errors"
	"image"
	"image/png"
	"syscall"
	"time"
	"unsafe"

	"desktoptoolbox/internal/dib"
)

// 本文件提供 `DesktopToolbox.exe --selftest` 使用的辅助函数（读取剪贴板、模拟其他程序写入、模拟按键）。
// 正常运行时不会调用。

var (
	pAttachConsole = kernel32.NewProc("AttachConsole")
	pSendInput     = user32.NewProc("SendInput")
)

// AttachParentConsole 让 GUI 程序的标准输出显示在启动它的控制台中（自检用）。
func AttachParentConsole() bool {
	r, _, _ := pAttachConsole.Call(^uintptr(0)) // ATTACH_PARENT_PROCESS
	return r != 0
}

// ReadClipboardText 读取剪贴板中的文本（CF_UNICODETEXT）。
func ReadClipboardText() (string, error) {
	var (
		s   string
		err error
	)
	Invoke(func() {
		if err = openClipboard(); err != nil {
			return
		}
		defer pCloseClipboard.Call()
		if !available(CF_UNICODETEXT) {
			err = errors.New("剪贴板中没有文本")
			return
		}
		s = readText()
	})
	return s, err
}

// ClipboardImageFormats 读取剪贴板中的图片：分别返回 PNG 格式与 CF_DIB 格式解码后的图像（缺失为 nil）。
func ClipboardImageFormats() (fromPNG, fromDIB image.Image, err error) {
	Invoke(func() {
		if err = openClipboard(); err != nil {
			return
		}
		defer pCloseClipboard.Call()
		if available(cfPNG) {
			if b := globalBytes(getData(cfPNG)); b != nil {
				fromPNG, _ = png.Decode(bytes.NewReader(b))
			}
		}
		if available(CF_DIB) {
			if b := globalBytes(getData(CF_DIB)); b != nil {
				if img, e := dib.Decode(b); e == nil {
					fromDIB = img
				}
			}
		}
	})
	return
}

// WriteForeignText 像“其他程序”一样写入文本（不标记为本程序写入），用于验证剪贴板监听。
func WriteForeignText(text string) error {
	var err error
	Invoke(func() {
		if err = openClipboard(); err != nil {
			return
		}
		defer pCloseClipboard.Call()
		pEmptyClipboard.Call()
		u, e := syscall.UTF16FromString(text)
		if e != nil {
			err = e
			return
		}
		err = setGlobal(CF_UNICODETEXT, unsafe.Slice((*byte)(unsafe.Pointer(&u[0])), len(u)*2))
	})
	return err
}

// WriteForeignDIB 像“其他程序”（如画图）一样只写入 CF_DIB 图片，用于验证 DIB→PNG 记录路径。
func WriteForeignDIB(img image.Image) error {
	data := dib.Encode24(img)
	var err error
	Invoke(func() {
		if err = openClipboard(); err != nil {
			return
		}
		defer pCloseClipboard.Call()
		pEmptyClipboard.Call()
		err = setGlobal(CF_DIB, data)
	})
	return err
}

type keybdInput struct {
	Type    uint32
	_       uint32
	Vk      uint16
	Scan    uint16
	Flags   uint32
	Time    uint32
	_       uint32
	Extra   uintptr
	Padding [8]byte
}

// SendKeyCombo 用 SendInput 模拟按下并松开组合键（依次按下 vks，再逆序松开）。返回成功注入的事件数。
func SendKeyCombo(vks ...uint16) int {
	var in []keybdInput
	for _, vk := range vks {
		in = append(in, keybdInput{Type: 1, Vk: vk})
	}
	for i := len(vks) - 1; i >= 0; i-- {
		in = append(in, keybdInput{Type: 1, Vk: vks[i], Flags: 0x0002}) // KEYEVENTF_KEYUP
	}
	r, _, _ := pSendInput.Call(uintptr(len(in)), uintptr(unsafe.Pointer(&in[0])), unsafe.Sizeof(in[0]))
	time.Sleep(50 * time.Millisecond)
	return int(r)
}
