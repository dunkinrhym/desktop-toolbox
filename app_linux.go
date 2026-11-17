//go:build linux

package main

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/godbus/dbus/v5"
	"github.com/wailsapp/wails/v2/pkg/options"
	"github.com/wailsapp/wails/v2/pkg/runtime"

	"desktoptoolbox/internal/cliphist"
	"desktoptoolbox/internal/converter"
	"desktoptoolbox/internal/lnx"
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

// linuxHotkey 一个功能的候选快捷键：按顺序尝试，第一个没被桌面环境占用的生效。
// 统信 / 深度 DDE 自带截图默认占用 Ctrl+Alt+A，此时退到 Ctrl+Shift+A。
type linuxHotkey struct {
	ID     int
	Action string
	Cands  []lnx.Combo
}

var hotkeys = []linuxHotkey{
	{ID: hkShow, Action: "显示/隐藏主窗口", Cands: []lnx.Combo{
		{Keysym: lnx.KeySpace, Mods: lnx.ModAlt, Name: "Alt+Space"},
		{Keysym: lnx.KeySpace, Mods: lnx.ModControl | lnx.ModAlt, Name: "Ctrl+Alt+Space"},
	}},
	{ID: hkShot, Action: "截图", Cands: []lnx.Combo{
		{Keysym: lnx.KeyA, Mods: lnx.ModControl | lnx.ModAlt, Name: "Ctrl+Alt+A"},
		{Keysym: lnx.KeyA, Mods: lnx.ModControl | lnx.ModShift, Name: "Ctrl+Shift+A"},
	}},
	{ID: hkClip, Action: "剪贴板历史", Cands: []lnx.Combo{
		{Keysym: lnx.KeyV, Mods: lnx.ModControl | lnx.ModShift, Name: "Ctrl+Shift+V"},
		{Keysym: lnx.KeyV, Mods: lnx.ModControl | lnx.ModAlt | lnx.ModShift, Name: "Ctrl+Alt+Shift+V"},
	}},
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
	icon     string
	initErrs []string

	hkMu     sync.Mutex
	hkActive map[int]lnx.Combo // 已生效的快捷键
	hkFailed map[int]bool

	mu       sync.Mutex
	settings Settings
	popup    bool // 剪贴板弹出模式

	search searchState
	convs  convertState
	rec    recordState
	shot   shotState
	ai     aiState
}

// NewApp 创建应用。安装布局：/opt/apps/desktop-toolbox/files/bin/desktop-toolbox，资源在同目录 resources/。
func NewApp() *App {
	exe, _ := os.Executable()
	if p, err := filepath.EvalSymlinks(exe); err == nil {
		exe = p
	}
	exeDir := filepath.Dir(exe)
	res := filepath.Join(exeDir, "resources")
	cfg, err := os.UserConfigDir() // ~/.config
	if err != nil {
		cfg = os.TempDir()
	}
	a := &App{
		exeDir:   exeDir,
		resDir:   res,
		dataDir:  filepath.Join(cfg, "DesktopToolbox"),
		settings: Settings{ClipEnabled: true},
		icon:     filepath.Join(res, "icon.png"),
		hkActive: map[int]lnx.Combo{},
		hkFailed: map[int]bool{},
	}
	a.conv = &converter.Converter{
		Python: filepath.Join(res, "python", "bin", "python3"),
		Script: filepath.Join(res, "scripts", "convert.py"),
	}
	a.ffmpeg = findFFmpeg(res)
	return a
}

// findFFmpeg 优先使用随包附带的 ffmpeg，其次系统 ffmpeg（deb 依赖）。
func findFFmpeg(res string) string {
	if p := filepath.Join(res, "ffmpeg", "ffmpeg"); fileExists(p) {
		return p
	}
	if p, err := exec.LookPath("ffmpeg"); err == nil {
		return p
	}
	return "/usr/bin/ffmpeg"
}

func fileExists(p string) bool {
	_, err := os.Stat(p)
	return err == nil
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

	lnx.SetHandlers(lnx.Handlers{
		OnTray:         a.onTray,
		OnTrayActivate: func() { a.showMain("") },
		OnClipText:     a.onClipText,
		OnClipPNG:      a.onClipPNG,
		OnRecbarStop:   func() { _ = a.StopRecording() },
	})
	// OnStartup 在 Wails 的 goroutine 中执行；GTK 调用经 lnx.OnMain 投递到主循环
	if err := lnx.StartHotkeys(a.onHotkey); err != nil {
		a.initErrs = append(a.initErrs, err.Error())
	} else {
		a.registerHotkeys()
	}
	lnx.ShowTray(AppName+" "+AppVersion, a.icon, []lnx.TrayItem{
		{ID: trayShow, Label: "显示主窗口"},
		{ID: trayShot, Label: "截图"},
		{ID: trayClip, Label: "剪贴板历史"},
		{ID: trayRec, Label: "录屏…"},
		{ID: trayStop, Label: "停止录屏"},
		{},
		{ID: trayQuit, Label: "退出桌面工具箱"},
	})
	if a.clips != nil && a.settings.ClipEnabled {
		lnx.WatchClipboard(true)
	}
}

func (a *App) registerHotkeys() {
	a.hkMu.Lock()
	defer a.hkMu.Unlock()
	for _, h := range hotkeys {
		ok := false
		for _, c := range h.Cands {
			if err := lnx.GrabHotkey(h.ID, c); err == nil {
				a.hkActive[h.ID] = c
				ok = true
				break
			}
		}
		a.hkFailed[h.ID] = !ok
	}
}

func (a *App) unregisterHotkeys() {
	a.hkMu.Lock()
	defer a.hkMu.Unlock()
	for id, c := range a.hkActive {
		lnx.UngrabHotkey(id, c)
	}
	a.hkActive = map[int]lnx.Combo{}
}

func (a *App) hotkeyName(id int) string {
	a.hkMu.Lock()
	defer a.hkMu.Unlock()
	if c, ok := a.hkActive[id]; ok {
		return c.Name
	}
	for _, h := range hotkeys {
		if h.ID == id {
			return h.Cands[0].Name
		}
	}
	return ""
}

func (a *App) domReady(ctx context.Context) {
	go lnx.SetWindowIcon(AppName, a.icon)
}

func (a *App) shutdown(ctx context.Context) {
	a.CancelSearch()
	a.CancelConvert()
	a.aiShutdown()
	a.stopRecordingSync()
	a.unregisterHotkeys()
	// 此时 GTK 主循环已结束：不能再调用 lnx.OnMain（托盘、剪贴板随进程退出自动清理）
}

func (a *App) onSecondInstance(data options.SecondInstanceData) {
	// --show=<页面>：切换到指定页面（冒烟测试逐页打开各工具时使用）；--quit：退出
	view := ""
	for _, s := range data.Args {
		switch {
		case strings.HasPrefix(s, "--show="):
			view = strings.TrimPrefix(s, "--show=")
		case s == "--quit":
			a.Quit()
			return
		case s == "--background" || s == "--hidden":
			return
		}
	}
	a.showMain(view)
}

// UIProbe 前端切换页面后回报页面是否正常渲染。仅在设置了环境变量 DT_UI_PROBE（文件路径）时写入，供冒烟测试校验。
func (a *App) UIProbe(view string, ok bool, detail string) {
	p := os.Getenv("DT_UI_PROBE")
	if p == "" {
		return
	}
	f, err := os.OpenFile(p, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		return
	}
	defer f.Close()
	fmt.Fprintf(f, "%s\t%v\t%s\n", view, ok, detail)
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
	runtime.WindowShow(a.ctx)
	runtime.WindowUnminimise(a.ctx)
	time.Sleep(30 * time.Millisecond)
	lnx.PresentWindow(AppName)
}

// showClipPopup 以小窗口形式显示剪贴板历史。
func (a *App) showClipPopup() {
	a.mu.Lock()
	a.popup = true
	a.mu.Unlock()
	runtime.EventsEmit(a.ctx, "ui:mode", map[string]any{"popup": true, "view": "clipboard"})
	runtime.WindowSetMinSize(a.ctx, 360, 400) // 主窗口最小 860 宽，小窗口模式需放宽
	runtime.WindowSetSize(a.ctx, 440, 640)
	runtime.WindowCenter(a.ctx)
	runtime.WindowSetAlwaysOnTop(a.ctx, true)
	runtime.WindowShow(a.ctx)
	runtime.WindowUnminimise(a.ctx)
	time.Sleep(30 * time.Millisecond)
	lnx.PresentWindow(AppName)
}

// HideWindow 隐藏主窗口（前端 Esc / 弹出模式复制后）。窗口管理器会把焦点还给之前的程序，可直接 Ctrl+V。
func (a *App) HideWindow() {
	runtime.WindowHide(a.ctx)
}

func (a *App) onHotkey(id int) {
	switch id {
	case hkShow:
		if vis, act := lnx.WindowState(AppName); vis && act {
			a.HideWindow()
		} else {
			a.showMain("")
		}
	case hkShot:
		a.takeScreenshot(false)
	case hkClip:
		if vis, act := lnx.WindowState(AppName); a.isPopup() && vis && act {
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
	var hks []HotkeyInfo
	a.hkMu.Lock()
	failed := map[int]bool{}
	for k, v := range a.hkFailed {
		failed[k] = v
	}
	a.hkMu.Unlock()
	for _, h := range hotkeys {
		name := a.hotkeyName(h.ID)
		hks = append(hks, HotkeyInfo{Name: name, Keys: name, OK: !failed[h.ID], Action: h.Action})
	}
	a.mu.Lock()
	clipOn := a.settings.ClipEnabled
	a.mu.Unlock()
	return AppInfo{
		Name: AppName, Version: AppVersion, Hotkeys: hks, DataDir: a.dataDir, Desktop: desktopDir(),
		Autostart: getAutostart(), HasPython: fileExists(a.conv.Python), HasFFmpeg: fileExists(a.ffmpeg),
		ClipOn: clipOn && a.clips != nil, InitErrors: a.initErrs, Platform: "linux", ScreenOK: true,
	}
}

// ---------------------------------------------------------------- 开机自启（XDG autostart）

func autostartPath() string {
	cfg, err := os.UserConfigDir()
	if err != nil {
		home, _ := os.UserHomeDir()
		cfg = filepath.Join(home, ".config")
	}
	return filepath.Join(cfg, "autostart", PkgName+".desktop")
}

func getAutostart() bool { return fileExists(autostartPath()) }

// SetAutostart 设置开机（登录）自启：写入 ~/.config/autostart/desktop-toolbox.desktop，后台启动不显示窗口。
func (a *App) SetAutostart(on bool) error {
	p := autostartPath()
	if !on {
		if err := os.Remove(p); err != nil && !os.IsNotExist(err) {
			return errors.New("无法取消开机自启：" + err.Error())
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
	content := fmt.Sprintf(`[Desktop Entry]
Type=Application
Name=%s
Name[zh_CN]=%s
Exec="%s" --background
Icon=%s
Terminal=false
X-GNOME-Autostart-enabled=true
X-Deepin-Vendor=user-custom
`, "Desktop Toolbox", AppName, exe, PkgName)
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		return err
	}
	if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
		return errors.New("无法设置开机自启：" + err.Error())
	}
	return nil
}

// ---------------------------------------------------------------- shell

func fileURI(p string) string {
	return (&url.URL{Scheme: "file", Path: p}).String()
}

// OpenPath 用默认程序打开文件。
func (a *App) OpenPath(path string) error {
	if _, err := os.Stat(path); err != nil {
		return errors.New("文件不存在或无法访问：" + path)
	}
	if err := lnx.ShowURI(fileURI(path)); err != nil {
		if out, err2 := exec.Command("xdg-open", path).CombinedOutput(); err2 != nil {
			return fmt.Errorf("无法打开：%v %s", err, strings.TrimSpace(string(out)))
		}
	}
	return nil
}

// ShowInFolder 在文件管理器中显示并选中文件（org.freedesktop.FileManager1，统信 / 深度文件管理器支持）。
func (a *App) ShowInFolder(path string) error {
	if _, err := os.Stat(path); err != nil {
		return errors.New("文件不存在或无法访问：" + path)
	}
	if conn, err := dbus.ConnectSessionBus(); err == nil {
		defer conn.Close()
		call := conn.Object("org.freedesktop.FileManager1", "/org/freedesktop/FileManager1").
			Call("org.freedesktop.FileManager1.ShowItems", 0, []string{fileURI(path)}, "")
		if call.Err == nil {
			return nil
		}
	}
	return lnx.ShowURI(fileURI(filepath.Dir(path)))
}

// CopyText 复制文本到剪贴板（如复制文件路径）。
func (a *App) CopyText(text string) error { return lnx.SetClipboardText(text) }

// OpenDataDir 打开数据目录。
func (a *App) OpenDataDir() error {
	_ = os.MkdirAll(a.dataDir, 0o755)
	return lnx.ShowURI(fileURI(a.dataDir))
}

// desktopDir 桌面目录：读取 ~/.config/user-dirs.dirs 的 XDG_DESKTOP_DIR（中文系统可能是“桌面”），否则 ~/Desktop。
func desktopDir() string {
	home, err := os.UserHomeDir()
	if err != nil {
		return os.TempDir()
	}
	if d := xdgUserDir(home, "XDG_DESKTOP_DIR"); d != "" {
		if st, err := os.Stat(d); err == nil && st.IsDir() {
			return d
		}
	}
	for _, n := range []string{"Desktop", "桌面"} {
		d := filepath.Join(home, n)
		if st, err := os.Stat(d); err == nil && st.IsDir() {
			return d
		}
	}
	d := filepath.Join(home, "Desktop")
	if err := os.MkdirAll(d, 0o755); err == nil {
		return d
	}
	return home
}

func xdgUserDir(home, key string) string {
	cfg := os.Getenv("XDG_CONFIG_HOME")
	if cfg == "" {
		cfg = filepath.Join(home, ".config")
	}
	f, err := os.Open(filepath.Join(cfg, "user-dirs.dirs"))
	if err != nil {
		return ""
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if !strings.HasPrefix(line, key+"=") {
			continue
		}
		v := strings.Trim(strings.TrimPrefix(line, key+"="), `"`)
		v = strings.ReplaceAll(v, "$HOME", home)
		if v == home || v == home+"/" {
			return ""
		}
		return v
	}
	return ""
}

// hideMainForCapture 截图/录屏前隐藏主窗口；返回之前是否可见。
func (a *App) hideMainForCapture() bool {
	visible, _ := lnx.WindowState(AppName)
	if visible {
		runtime.WindowHide(a.ctx)
		time.Sleep(350 * time.Millisecond) // 等窗口管理器的隐藏动画结束
	}
	return visible
}
