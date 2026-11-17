//go:build linux

package lnx

/*
#include "lnx.h"
#include <X11/keysym.h>
*/
import "C"

import (
	"errors"
	"runtime"
	"sync"
	"unsafe"
)

// 常用 X11 keysym 与修饰键。
const (
	KeySpace  = 0x0020
	KeyA      = 0x0061
	KeyV      = 0x0076
	KeyF12    = 0xffc9
	KeyEscape = 0xff1b
	KeyReturn = 0xff0d

	KeyShiftL   = 0xffe1
	KeyControlL = 0xffe3
	KeyAltL     = 0xffe9

	ModShift   = 1 << 0
	ModControl = 1 << 2
	ModAlt     = 1 << 3 // Mod1
	ModSuper   = 1 << 6 // Mod4
)

// Combo 一个组合键。
type Combo struct {
	Keysym uint32
	Mods   uint32
	Name   string
}

type grabReq struct {
	id    int
	combo Combo
	grab  bool
	res   chan error
}

type hkKey struct{ code, mods uint32 }

var (
	hkOnce   sync.Once
	hkErr    error
	hkReqs   chan grabReq
	hkMu     sync.Mutex
	hkByKey  = map[hkKey]int{}
	hkByID   = map[int]hkKey{}
	hkNotify func(id int)
)

// StartHotkeys 打开独立的 X 连接并启动热键线程。onHotkey 在新 goroutine 中调用。
func StartHotkeys(onHotkey func(id int)) error {
	hkMu.Lock()
	hkNotify = onHotkey
	hkMu.Unlock()
	hkOnce.Do(func() {
		hkReqs = make(chan grabReq)
		ready := make(chan error)
		go hkLoop(ready)
		hkErr = <-ready
	})
	return hkErr
}

func hkLoop(ready chan error) {
	runtime.LockOSThread()
	d := C.lnx_hk_open()
	if d == nil {
		ready <- errors.New("无法连接 X11 显示服务器（全局快捷键需要 X11 会话）")
		return
	}
	ready <- nil
	for {
		select {
		case r := <-hkReqs:
			r.res <- hkApply(d, r)
			continue
		default:
		}
		var code, state C.uint
		if C.lnx_hk_poll(d, 50, &code, &state) != 0 {
			hkMu.Lock()
			id, ok := hkByKey[hkKey{uint32(code), uint32(state)}]
			fn := hkNotify
			hkMu.Unlock()
			if ok && fn != nil {
				go fn(id)
			}
		}
	}
}

func hkApply(d *C.Display, r grabReq) error {
	code := uint32(C.lnx_hk_keycode(d, C.ulong(r.combo.Keysym)))
	if code == 0 {
		return errors.New("键盘上没有这个按键")
	}
	k := hkKey{code, r.combo.Mods}
	g := C.int(0)
	if r.grab {
		g = 1
	}
	if C.lnx_hk_grab(d, C.uint(code), C.uint(r.combo.Mods), g) == 0 {
		return errors.New("已被其他程序占用")
	}
	hkMu.Lock()
	if r.grab {
		hkByKey[k] = r.id
		hkByID[r.id] = k
	} else {
		delete(hkByKey, k)
		delete(hkByID, r.id)
	}
	hkMu.Unlock()
	return nil
}

// GrabHotkey 注册全局热键（XGrabKey；被桌面环境或其他程序占用时返回错误）。
func GrabHotkey(id int, c Combo) error {
	if hkReqs == nil || hkErr != nil {
		return errors.New("全局快捷键不可用")
	}
	res := make(chan error, 1)
	hkReqs <- grabReq{id: id, combo: c, grab: true, res: res}
	return <-res
}

// UngrabHotkey 取消注册。
func UngrabHotkey(id int, c Combo) {
	if hkReqs == nil || hkErr != nil {
		return
	}
	res := make(chan error, 1)
	hkReqs <- grabReq{id: id, combo: c, grab: false, res: res}
	<-res
}

// SendKeys 用 XTest 依次按下再松开一组按键（自检 / 测试用）。
func SendKeys(keysyms ...uint32) bool {
	if len(keysyms) == 0 {
		return false
	}
	ks := make([]C.ulong, len(keysyms))
	for i, k := range keysyms {
		ks[i] = C.ulong(k)
	}
	return C.lnx_xtest_keys((*C.ulong)(unsafe.Pointer(&ks[0])), C.int(len(ks))) != 0
}

// PointerTo 用 XTest 移动鼠标；button>0 时同时按下（press）或松开该按键。
func PointerTo(x, y, button int, press bool) bool {
	p := C.int(0)
	if press {
		p = 1
	}
	return C.lnx_xtest_pointer(C.int(x), C.int(y), C.int(button), p) != 0
}
