//go:build windows

package win

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"syscall"
	"unsafe"

	"golang.org/x/sys/windows"
	"golang.org/x/sys/windows/registry"
)

// OpenFile 用系统默认程序打开文件。
func OpenFile(path string) error {
	if _, err := os.Stat(path); err != nil {
		return fmt.Errorf("文件不存在或无法访问：%s", path)
	}
	r, _, _ := pShellExecuteW.Call(0, uintptr(unsafe.Pointer(u16("open"))), uintptr(unsafe.Pointer(u16(path))), 0,
		uintptr(unsafe.Pointer(u16(filepath.Dir(path)))), SW_SHOWNORMAL)
	if r > 32 {
		return nil
	}
	switch r {
	case 31: // SE_ERR_NOASSOC
		// 没有关联程序：弹出“打开方式”对话框
		cmd := exec.Command("rundll32.exe", "shell32.dll,OpenAs_RunDLL", path)
		if err := cmd.Start(); err == nil {
			return nil
		}
		return errors.New("没有可以打开此类文件的程序")
	case 2, 3:
		return errors.New("找不到文件")
	case 5:
		return errors.New("没有权限打开该文件")
	}
	return fmt.Errorf("无法打开文件（错误码 %d）", r)
}

// ShowInFolder 打开资源管理器并选中文件。
func ShowInFolder(path string) error {
	if _, err := os.Stat(path); err != nil {
		return fmt.Errorf("文件不存在：%s", path)
	}
	errc := make(chan error, 1)
	go func() {
		runtime.LockOSThread()
		defer runtime.UnlockOSThread()
		windows.CoInitializeEx(0, windows.COINIT_APARTMENTTHREADED)
		defer windows.CoUninitialize()
		pidl, _, _ := pILCreateFromPathW.Call(uintptr(unsafe.Pointer(u16(path))))
		if pidl == 0 {
			errc <- errors.New("ILCreateFromPath 失败")
			return
		}
		defer pILFree.Call(pidl)
		hr, _, _ := pSHOpenFolderAndSelectItems.Call(pidl, 0, 0, 0)
		if hr != 0 {
			errc <- fmt.Errorf("SHOpenFolderAndSelectItems: 0x%x", hr)
			return
		}
		errc <- nil
	}()
	if err := <-errc; err != nil {
		// 回退：explorer /select,"path"（需原样命令行）
		cmd := exec.Command("explorer.exe")
		cmd.SysProcAttr = &syscall.SysProcAttr{CmdLine: `explorer.exe /select,"` + path + `"`}
		if e := cmd.Start(); e != nil {
			return fmt.Errorf("无法打开所在文件夹：%v", e)
		}
	}
	return nil
}

// Drives 返回本机固定磁盘与可移动磁盘的根目录。
func Drives() []string {
	mask, _, _ := pGetLogicalDrives.Call()
	var out []string
	for i := 0; i < 26; i++ {
		if mask&(1<<uint(i)) == 0 {
			continue
		}
		root := string(rune('A'+i)) + `:\`
		t, _, _ := pGetDriveTypeW.Call(uintptr(unsafe.Pointer(u16(root))))
		if t == 2 || t == 3 { // DRIVE_REMOVABLE / DRIVE_FIXED
			out = append(out, root)
		}
	}
	return out
}

// DesktopDir 返回桌面目录。
func DesktopDir() string {
	if p, err := windows.KnownFolderPath(windows.FOLDERID_Desktop, 0); err == nil && p != "" {
		return p
	}
	home, _ := os.UserHomeDir()
	return filepath.Join(home, "Desktop")
}

// SaveFileDialog 显示“另存为”对话框。filter 形如 "PNG 图片|*.png"。
func SaveFileDialog(title, initialDir, defaultName, filter, defExt string) (string, bool) {
	var result string
	var ok bool
	Invoke(func() {
		buf := make([]uint16, 32768)
		name, _ := syscall.UTF16FromString(defaultName)
		copy(buf, name)
		f := strings.ReplaceAll(filter, "|", "\x00") + "\x00\x00"
		fu := utf16.EncodeString(f)
		ofn := OPENFILENAMEW{
			LStructSize:     uint32(unsafe.Sizeof(OPENFILENAMEW{})),
			HwndOwner:       0,
			LpstrFilter:     &fu[0],
			NFilterIndex:    1,
			LpstrFile:       &buf[0],
			NMaxFile:        uint32(len(buf)),
			LpstrInitialDir: u16(initialDir),
			LpstrTitle:      u16(title),
			Flags:           0x00000002 | 0x00000800 | 0x00080000 | 0x00000008, // OVERWRITEPROMPT|PATHMUSTEXIST|EXPLORER|NOCHANGEDIR
			LpstrDefExt:     u16(defExt),
		}
		r, _, _ := pGetSaveFileNameW.Call(uintptr(unsafe.Pointer(&ofn)))
		if r != 0 {
			result = syscall.UTF16ToString(buf)
			ok = true
		}
	})
	return result, ok
}

// utf16 小工具：允许字符串内含 NUL。
var utf16 = struct{ EncodeString func(string) []uint16 }{EncodeString: func(s string) []uint16 {
	out := make([]uint16, 0, len(s)+1)
	for _, r := range s {
		if r >= 0x10000 {
			r -= 0x10000
			out = append(out, uint16(0xD800+(r>>10)), uint16(0xDC00+(r&0x3FF)))
		} else {
			out = append(out, uint16(r))
		}
	}
	return out
}}

// FocusWindowByClass 把指定窗口类的顶层窗口恢复并置于前台。
func FocusWindowByClass(class string) bool {
	hwnd, _, _ := pFindWindowW.Call(uintptr(unsafe.Pointer(u16(class))), 0)
	if hwnd == 0 {
		return false
	}
	if r, _, _ := pIsIconic.Call(hwnd); r != 0 {
		pShowWindow.Call(hwnd, SW_RESTORE)
	}
	forceForeground(hwnd)
	return true
}

// IsWindowVisibleByClass 主窗口当前是否可见。
func IsWindowVisibleByClass(class string) bool {
	hwnd, _, _ := pFindWindowW.Call(uintptr(unsafe.Pointer(u16(class))), 0)
	if hwnd == 0 {
		return false
	}
	r, _, _ := pIsWindowVisible.Call(hwnd)
	if r == 0 {
		return false
	}
	ic, _, _ := pIsIconic.Call(hwnd)
	return ic == 0
}

// IsForegroundClass 指定类窗口是否为当前前台窗口。
func IsForegroundClass(class string) bool {
	hwnd, _, _ := pFindWindowW.Call(uintptr(unsafe.Pointer(u16(class))), 0)
	fg, _, _ := pGetForegroundWindow.Call()
	return hwnd != 0 && hwnd == fg
}

const runKey = `Software\Microsoft\Windows\CurrentVersion\Run`

// SetAutostart 设置开机自启（当前用户）。
func SetAutostart(name string, enable bool) error {
	k, _, err := registry.CreateKey(registry.CURRENT_USER, runKey, registry.SET_VALUE|registry.QUERY_VALUE)
	if err != nil {
		return fmt.Errorf("无法写入注册表：%v", err)
	}
	defer k.Close()
	if !enable {
		if err := k.DeleteValue(name); err != nil && !errors.Is(err, registry.ErrNotExist) && !errors.Is(err, syscall.ERROR_FILE_NOT_FOUND) {
			return err
		}
		return nil
	}
	exe, err := os.Executable()
	if err != nil {
		return err
	}
	return k.SetStringValue(name, `"`+exe+`" --background`)
}

// GetAutostart 是否已设置开机自启。
func GetAutostart(name string) bool {
	k, err := registry.OpenKey(registry.CURRENT_USER, runKey, registry.QUERY_VALUE)
	if err != nil {
		return false
	}
	defer k.Close()
	_, _, err = k.GetStringValue(name)
	return err == nil
}

// appIcon 从 exe 资源中取出应用图标。
func appIcon(small bool) uintptr {
	exe, err := os.Executable()
	if err != nil {
		return 0
	}
	var large, sm uintptr
	pExtractIconExW.Call(uintptr(unsafe.Pointer(u16(exe))), 0, uintptr(unsafe.Pointer(&large)), uintptr(unsafe.Pointer(&sm)), 1)
	if small && sm != 0 {
		if large != 0 {
			pDestroyIcon.Call(large)
		}
		return sm
	}
	if sm != 0 {
		pDestroyIcon.Call(sm)
	}
	if large == 0 {
		r, _, _ := pLoadImageW.Call(0, 32512 /*IDI_APPLICATION*/, 1 /*IMAGE_ICON*/, 0, 0, 0x8000 /*LR_SHARED*/)
		return r
	}
	return large
}
