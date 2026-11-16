//go:build windows

package main

import (
	"context"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/wailsapp/wails/v2/pkg/options"
	"github.com/wailsapp/wails/v2/pkg/runtime"

	"desktoptoolbox/internal/cliphist"
	"desktoptoolbox/internal/converter"
	"desktoptoolbox/internal/win"
)

const (
	hkShow = 1
	hkShot = 2
	hkClip = 3

	trayShow = 101
	trayShot = 102
	trayClip = 103
	trayRec  = 104
	trayQuit = 199
)

var hotkeys = []win.Hotkey{
	{ID: hkShow, Mods: win.MOD_ALT, VK: win.VK_SPACE, Name: "Alt+Space"},
	{ID: hkShot, Mods: win.MOD_CONTROL | win.MOD_ALT, VK: 'A', Name: "Ctrl+Alt+A"},
	{ID: hkClip, Mods: win.MOD_CONTROL | win.MOD_SHIFT, VK: 'V', Name: "Ctrl+Shift+V"},
}

// App 绑定到前端的应用对象。
type App struct {
	ctx      context.Context
	exeDir   string
	dataDir  string
	clips    *cliphist.Store
	conv     *converter.Converter
	ffmpeg   string
	hkFailed []string
	initErrs []string

	mu       sync.Mutex
	settings Settings
	popup    bool // 剪贴板弹出模式

	search searchState
	convs  convertState
	rec    recordState
	shot   shotState
}

// NewApp 创建应用。
func NewApp() *App {
	exe, _ := os.Executable()
	exeDir := filepath.Dir(exe)
	cfg, err := os.UserConfigDir()
	if err != nil {
		cfg = exeDir
	}
	a := &App{
		exeDir:   exeDir,
		dataDir:  filepath.Join(cfg, "DesktopToolbox"),
		settings: Settings{ClipEnabled: true},
	}
	res := filepath.Join(exeDir, "resources")
	a.conv = &converter.Converter{
		Python: filepath.Join(res, "python", "python.exe"),
		Script: filepath.Join(res, "scripts", "convert.py"),
	}
	a.ffmpeg = filepath.Join(res, "ffmpeg", "ffmpeg.exe")
	return a
}

func (a *App) startup(ctx context.Context) {
	a.ctx = ctx
	if err := os.MkdirAll(a.dataDir, 0o755); err != nil {
		a.initErrs = append(a.initErrs, "无法创建数据目录："+err.Error())
	}
	a.loadSettings()
	store, err := cliphist.Open(filepath.Join(a.dataDir, "clipboard"), cliphist.DefaultLimit)
	if err != nil {
		a.initErrs = append(a.initErrs, "剪贴板历史无法加载："+err.Error())
	}
	a.clips = store

	err = win.Start(win.Handlers{
		OnHotkey:        a.onHotkey,
		OnClipboardText: a.onClipText,
		OnClipboardPNG:  a.onClipPNG,
		OnTrayCommand:   a.onTray,
		OnTrayClick:     func() { a.showMain("") },
	}, AppName+" "+AppVersion)
	if err != nil {
		a.initErrs = append(a.initErrs, "原生组件初始化失败："+err.Error())
		return
	}
	a.hkFailed = win.RegisterHotkeys(hotkeys)
	if err := win.ShowTray([]win.TrayItem{
		{ID: trayShow, Label: "显示主窗口\tAlt+Space"},
		{ID: trayShot, Label: "截图\tCtrl+Alt+A"},
		{ID: trayClip, Label: "剪贴板历史\tCtrl+Shift+V"},
		{ID: trayRec, Label: "录屏"},
		{},
		{ID: trayQuit, Label: "退出"},
	}); err != nil {
		a.initErrs = append(a.initErrs, err.Error())
	}
	if a.clips != nil && a.settings.ClipEnabled {
		if err := win.WatchClipboard(true); err != nil {
			a.initErrs = append(a.initErrs, err.Error())
		}
	}
}

func (a *App) domReady(ctx context.Context) {}

func (a *App) shutdown(ctx context.Context) {
	a.CancelSearch()
	a.CancelConvert()
	a.stopRecordingSync()
	win.UnregisterHotkeys(hotkeys)
	win.WatchClipboard(false)
	win.RemoveTray()
}

func (a *App) onSecondInstance(data options.SecondInstanceData) {
	a.showMain("")
}

// ---------------------------------------------------------------- window control

// showMain 显示主窗口；view 非空时切换到对应页面。
func (a *App) showMain(view string) {
	a.mu.Lock()
	wasPopup := a.popup
	a.popup = false
	a.mu.Unlock()
	if wasPopup {
		runtime.WindowSetAlwaysOnTop(a.ctx, false)
		runtime.WindowSetSize(a.ctx, 1120, 740)
		runtime.WindowCenter(a.ctx)
	}
	runtime.EventsEmit(a.ctx, "ui:mode", map[string]any{"popup": false, "view": view})
	runtime.WindowShow(a.ctx)
	runtime.WindowUnminimise(a.ctx)
	time.Sleep(30 * time.Millisecond)
	win.FocusWindowByClass(MainWinClass)
}

// showClipPopup 以小窗口形式显示剪贴板历史。
func (a *App) showClipPopup() {
	a.mu.Lock()
	a.popup = true
	a.mu.Unlock()
	runtime.EventsEmit(a.ctx, "ui:mode", map[string]any{"popup": true, "view": "clipboard"})
	runtime.WindowSetSize(a.ctx, 440, 640)
	runtime.WindowCenter(a.ctx)
	runtime.WindowSetAlwaysOnTop(a.ctx, true)
	runtime.WindowShow(a.ctx)
	runtime.WindowUnminimise(a.ctx)
	time.Sleep(30 * time.Millisecond)
	win.FocusWindowByClass(MainWinClass)
}

// HideWindow 隐藏主窗口（前端 Esc / 弹出模式复制后）。
func (a *App) HideWindow() {
	runtime.WindowHide(a.ctx)
}

func (a *App) onHotkey(id int) {
	switch id {
	case hkShow:
		if win.IsForegroundClass(MainWinClass) && win.IsWindowVisibleByClass(MainWinClass) {
			runtime.WindowHide(a.ctx)
		} else {
			a.showMain("")
		}
	case hkShot:
		a.takeScreenshot(false)
	case hkClip:
		if a.isPopup() && win.IsForegroundClass(MainWinClass) {
			runtime.WindowHide(a.ctx)
			return
		}
		a.showClipPopup()
	}
}

func (a *App) onTray(cmd int) {
	switch cmd {
	case trayShow:
		a.showMain("")
	case trayShot:
		a.takeScreenshot(false)
	case trayClip:
		a.showMain("clipboard")
	case trayRec:
		a.showMain("record")
	case trayQuit:
		a.Quit()
	}
}

// ---------------------------------------------------------------- info / settings

// GetInfo 返回应用状态信息。
func (a *App) GetInfo() AppInfo {
	failed := map[string]bool{}
	for _, f := range a.hkFailed {
		failed[f] = true
	}
	actions := map[int]string{hkShow: "显示/隐藏主窗口", hkShot: "截图", hkClip: "剪贴板历史"}
	var hks []HotkeyInfo
	for _, h := range hotkeys {
		hks = append(hks, HotkeyInfo{Name: h.Name, Keys: h.Name, OK: !failed[h.Name], Action: actions[h.ID]})
	}
	_, perr := os.Stat(a.conv.Python)
	_, ferr := os.Stat(a.ffmpeg)
	a.mu.Lock()
	clipOn := a.settings.ClipEnabled
	a.mu.Unlock()
	return AppInfo{
		Name: AppName, Version: AppVersion, Hotkeys: hks, DataDir: a.dataDir, Desktop: win.DesktopDir(),
		Autostart: win.GetAutostart("DesktopToolbox"), HasPython: perr == nil, HasFFmpeg: ferr == nil,
		ClipOn: clipOn && a.clips != nil, InitErrors: a.initErrs, Platform: "windows", ScreenOK: true,
	}
}

// SetAutostart 设置开机自启。
func (a *App) SetAutostart(on bool) error {
	return win.SetAutostart("DesktopToolbox", on)
}

// OpenPath 用默认程序打开文件。
func (a *App) OpenPath(path string) error { return win.OpenFile(path) }

// ShowInFolder 在资源管理器中显示文件。
func (a *App) ShowInFolder(path string) error { return win.ShowInFolder(path) }

// CopyText 复制文本到剪贴板（如复制文件路径）。
func (a *App) CopyText(text string) error { return win.SetClipboardText(text) }

// OpenDataDir 打开数据目录。
func (a *App) OpenDataDir() error {
	return win.OpenFile(a.dataDir)
}
