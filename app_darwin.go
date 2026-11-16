//go:build darwin

package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/wailsapp/wails/v2/pkg/options"
	"github.com/wailsapp/wails/v2/pkg/runtime"

	"desktoptoolbox/internal/cliphist"
	"desktoptoolbox/internal/converter"
	"desktoptoolbox/internal/mac"
)

const (
	hkShow = 1
	hkShot = 2
	hkClip = 3

	trayShow = 101
	trayShot = 102
	trayClip = 103
	trayRec  = 104
	trayStop = 105
	trayQuit = 199
)

// macHotkey 全局快捷键定义（Carbon 键码 + 修饰键）。
type macHotkey struct {
	ID   int
	Key  uint32
	Mods uint32
	Name string // 设置页显示，按 + 拆成按键
}

// macOS 默认快捷键：
//   - Option+Space：显示/隐藏主窗口（⌘Space 是 Spotlight，Ctrl+Space 是切换输入法）
//   - Control+Option+A：截图（避开系统的 ⌘⇧3/4/5）
//   - Command+Shift+V：剪贴板历史
var hotkeys = []macHotkey{
	{ID: hkShow, Key: mac.KeySpace, Mods: mac.ModOption, Name: "⌥ Option+Space"},
	{ID: hkShot, Key: mac.KeyA, Mods: mac.ModControl | mac.ModOption, Name: "⌃ Control+⌥ Option+A"},
	{ID: hkClip, Key: mac.KeyV, Mods: mac.ModCmd | mac.ModShift, Name: "⌘ Command+⇧ Shift+V"},
}

// App 绑定到前端的应用对象。
type App struct {
	ctx      context.Context
	exeDir   string
	resDir   string
	dataDir  string
	clips    *cliphist.Store
	conv     *converter.Converter
	ffmpeg   string
	hkFailed []string
	initErrs []string

	mu        sync.Mutex
	settings  Settings
	popup     bool // 剪贴板弹出模式
	clipStop  chan struct{}
	permAsked bool // 已请求过“屏幕录制”权限

	search searchState
	convs  convertState
	rec    recordState
	shot   shotState
	ai     aiState
}

// NewApp 创建应用。资源位于 .app/Contents/Resources（开发时退回到可执行文件旁的 resources/）。
func NewApp() *App {
	exe, _ := os.Executable()
	if p, err := filepath.EvalSymlinks(exe); err == nil {
		exe = p
	}
	exeDir := filepath.Dir(exe)
	res := filepath.Join(exeDir, "..", "Resources")
	if _, err := os.Stat(filepath.Join(res, "scripts")); err != nil {
		res = filepath.Join(exeDir, "resources")
	}
	res = filepath.Clean(res)
	cfg, err := os.UserConfigDir() // ~/Library/Application Support
	if err != nil {
		cfg = exeDir
	}
	a := &App{
		exeDir:   exeDir,
		resDir:   res,
		dataDir:  filepath.Join(cfg, "DesktopToolbox"),
		settings: Settings{ClipEnabled: true},
	}
	a.conv = &converter.Converter{
		Python: filepath.Join(res, "python", "bin", "python3"),
		Script: filepath.Join(res, "scripts", "convert.py"),
	}
	a.ffmpeg = filepath.Join(res, "ffmpeg", "ffmpeg")
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

	mac.Init(mac.Handlers{
		OnHotkey: a.onHotkey,
		OnTray:   a.onTray,
		OnReopen: func() { a.showMain("") },
	})
	for _, h := range hotkeys {
		if err := mac.RegisterHotkey(h.ID, h.Key, h.Mods); err != nil {
			a.hkFailed = append(a.hkFailed, h.Name)
		}
	}
	if err := mac.ShowTray(AppName+" "+AppVersion, []mac.TrayItem{
		{ID: trayShow, Label: "显示主窗口", Key: " ", KeyMods: mac.NSOption},
		{ID: trayShot, Label: "截图", Key: "a", KeyMods: mac.NSControl | mac.NSOption},
		{ID: trayClip, Label: "剪贴板历史", Key: "v", KeyMods: mac.NSCommand | mac.NSShift},
		{ID: trayRec, Label: "录屏…"},
		{ID: trayStop, Label: "停止录屏"},
		{},
		{ID: trayQuit, Label: "退出桌面工具箱", Key: "q", KeyMods: mac.NSCommand},
	}); err != nil {
		a.initErrs = append(a.initErrs, "菜单栏图标无法显示："+err.Error())
	}
	if a.clips != nil && a.settings.ClipEnabled {
		_ = a.watchClipboard(true)
	}
}

func (a *App) domReady(ctx context.Context) {}

func (a *App) shutdown(ctx context.Context) {
	a.CancelSearch()
	a.CancelConvert()
	a.stopRecordingSync()
	mac.UnregisterHotkeys()
	_ = a.watchClipboard(false)
	mac.RemoveTray()
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
		runtime.WindowSetMinSize(a.ctx, 860, 560)
		runtime.WindowSetSize(a.ctx, 1120, 740)
		runtime.WindowCenter(a.ctx)
	}
	runtime.EventsEmit(a.ctx, "ui:mode", map[string]any{"popup": false, "view": view})
	mac.ActivateApp()
	runtime.WindowShow(a.ctx)
	runtime.WindowUnminimise(a.ctx)
	time.Sleep(30 * time.Millisecond)
	mac.ActivateApp()
}

// showClipPopup 以小窗口形式显示剪贴板历史。
func (a *App) showClipPopup() {
	a.mu.Lock()
	a.popup = true
	a.mu.Unlock()
	runtime.EventsEmit(a.ctx, "ui:mode", map[string]any{"popup": true, "view": "clipboard"})
	mac.ActivateApp()
	runtime.WindowSetMinSize(a.ctx, 360, 400) // 主窗口最小 860 宽，小窗口模式需放宽
	runtime.WindowSetSize(a.ctx, 440, 640)
	runtime.WindowCenter(a.ctx)
	runtime.WindowSetAlwaysOnTop(a.ctx, true)
	runtime.WindowShow(a.ctx)
	runtime.WindowUnminimise(a.ctx)
	time.Sleep(30 * time.Millisecond)
	mac.ActivateApp()
}

// HideWindow 隐藏主窗口（前端 Esc / 弹出模式复制后）。同时隐藏程序，让焦点回到之前的应用，可直接 ⌘V 粘贴。
func (a *App) HideWindow() {
	runtime.WindowHide(a.ctx)
	mac.HideApp()
}

func (a *App) onHotkey(id int) {
	switch id {
	case hkShow:
		if mac.IsAppActive() && mac.IsMainWindowVisible() {
			a.HideWindow()
		} else {
			a.showMain("")
		}
	case hkShot:
		a.takeScreenshot(false)
	case hkClip:
		if a.isPopup() && mac.IsAppActive() && mac.IsMainWindowVisible() {
			a.HideWindow()
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
	case trayStop:
		if a.IsRecording() {
			_ = a.StopRecording()
		}
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
		Name: AppName, Version: AppVersion, Hotkeys: hks, DataDir: a.dataDir, Desktop: desktopDir(),
		Autostart: getAutostart(), HasPython: perr == nil, HasFFmpeg: ferr == nil,
		ClipOn: clipOn && a.clips != nil, InitErrors: a.initErrs,
		Platform: "darwin", ScreenOK: mac.ScreenCaptureAllowed(),
	}
}

// Confirm 原生确认对话框（WKWebView 不支持 window.confirm）。
func (a *App) Confirm(msg string) bool {
	r, err := runtime.MessageDialog(a.ctx, runtime.MessageDialogOptions{
		Type: runtime.QuestionDialog, Title: AppName, Message: msg,
		Buttons: []string{"确定", "取消"}, DefaultButton: "确定", CancelButton: "取消",
	})
	return err == nil && r == "确定"
}

// ---------------------------------------------------------------- 登录时自动启动（LaunchAgent）

func launchAgentPath() string {
	home, _ := os.UserHomeDir()
	return filepath.Join(home, "Library", "LaunchAgents", BundleID+".plist")
}

func getAutostart() bool {
	_, err := os.Stat(launchAgentPath())
	return err == nil
}

func xmlEscape(s string) string {
	return strings.NewReplacer("&", "&amp;", "<", "&lt;", ">", "&gt;", `"`, "&quot;").Replace(s)
}

// SetAutostart 设置登录时自动启动（写入 ~/Library/LaunchAgents，后台启动不显示窗口）。
func (a *App) SetAutostart(on bool) error {
	p := launchAgentPath()
	if !on {
		if err := os.Remove(p); err != nil && !os.IsNotExist(err) {
			return errors.New("无法取消自动启动：" + err.Error())
		}
		return nil
	}
	exe, err := os.Executable()
	if err != nil {
		return err
	}
	if r, err := filepath.EvalSymlinks(exe); err == nil {
		exe = r
	}
	if strings.Contains(exe, "/AppTranslocation/") {
		return errors.New("请先把“桌面工具箱”拖到“应用程序”文件夹再开启自动启动")
	}
	plist := fmt.Sprintf(`<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
	<key>Label</key>
	<string>%s</string>
	<key>ProgramArguments</key>
	<array>
		<string>%s</string>
		<string>--background</string>
	</array>
	<key>RunAtLoad</key>
	<true/>
	<key>ProcessType</key>
	<string>Interactive</string>
</dict>
</plist>
`, BundleID, xmlEscape(exe))
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		return err
	}
	if err := os.WriteFile(p, []byte(plist), 0o644); err != nil {
		return errors.New("无法设置自动启动：" + err.Error())
	}
	return nil
}

// ---------------------------------------------------------------- shell

// OpenPath 用默认程序打开文件。
func (a *App) OpenPath(path string) error {
	if _, err := os.Stat(path); err != nil {
		return errors.New("文件不存在或无法访问：" + path)
	}
	if out, err := exec.Command("/usr/bin/open", path).CombinedOutput(); err != nil {
		return fmt.Errorf("无法打开：%s", strings.TrimSpace(string(out)))
	}
	return nil
}

// ShowInFolder 在访达中显示并选中文件。
func (a *App) ShowInFolder(path string) error {
	if _, err := os.Stat(path); err != nil {
		return errors.New("文件不存在或无法访问：" + path)
	}
	if out, err := exec.Command("/usr/bin/open", "-R", path).CombinedOutput(); err != nil {
		return fmt.Errorf("无法在访达中显示：%s", strings.TrimSpace(string(out)))
	}
	return nil
}

// CopyText 复制文本到剪贴板（如复制文件路径）。
func (a *App) CopyText(text string) error { return mac.SetClipboardText(text) }

// OpenDataDir 打开数据目录。
func (a *App) OpenDataDir() error {
	_ = os.MkdirAll(a.dataDir, 0o755)
	return exec.Command("/usr/bin/open", a.dataDir).Run()
}

// 系统设置中各隐私面板的 URL。
var privacyURLs = map[string]string{
	"screen":        "x-apple.systempreferences:com.apple.preference.security?Privacy_ScreenCapture",
	"microphone":    "x-apple.systempreferences:com.apple.preference.security?Privacy_Microphone",
	"accessibility": "x-apple.systempreferences:com.apple.preference.security?Privacy_Accessibility",
}

// OpenPrivacySettings 打开“系统设置 → 隐私与安全性”的对应面板（screen / microphone / accessibility）。
func (a *App) OpenPrivacySettings(kind string) error {
	u, ok := privacyURLs[kind]
	if !ok {
		u = privacyURLs["screen"]
	}
	return exec.Command("/usr/bin/open", u).Run()
}

// screenPermissionError 未获得“屏幕录制”权限时：触发系统授权弹窗、打开设置面板，并返回中文说明。
func (a *App) screenPermissionError() error {
	if mac.ScreenCaptureAllowed() {
		return nil
	}
	a.mu.Lock()
	first := !a.permAsked
	a.permAsked = true
	a.mu.Unlock()
	if first {
		// 第一次：系统会弹出授权对话框（其中有“打开系统设置”按钮）
		if mac.RequestScreenCapture() {
			return nil
		}
		return errors.New("需要“屏幕录制”权限：请在系统弹出的对话框中点“打开系统设置”，打开“桌面工具箱”旁的开关，然后退出并重新打开本程序。")
	}
	_ = a.OpenPrivacySettings("screen")
	return errors.New("需要“屏幕录制”权限：已打开“系统设置 → 隐私与安全性 → 屏幕录制”，请打开“桌面工具箱”旁的开关，然后退出并重新打开本程序。")
}

func desktopDir() string {
	home, err := os.UserHomeDir()
	if err != nil {
		return os.TempDir()
	}
	d := filepath.Join(home, "Desktop")
	if st, err := os.Stat(d); err == nil && st.IsDir() {
		return d
	}
	return home
}

// hideMainForCapture 截图/录屏前隐藏主窗口（并让出焦点）；返回之前是否可见。
func (a *App) hideMainForCapture() bool {
	visible := mac.IsMainWindowVisible()
	if visible {
		runtime.WindowHide(a.ctx)
		mac.HideApp()
		time.Sleep(350 * time.Millisecond)
	}
	return visible
}
