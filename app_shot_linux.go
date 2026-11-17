//go:build linux

package main

import (
	"errors"
	"os"
	"path/filepath"
	"sync"

	"github.com/wailsapp/wails/v2/pkg/runtime"

	"desktoptoolbox/internal/lnx"
)

// Linux 截图：冻结整个 X 屏幕画面，显示全屏框选遮罩（多显示器、HiDPI 缩放），选好后弹出工具栏：
// 复制（回车 / 双击）、存桌面、识字/翻译/改写、保存、取消（Esc / 右键）。

type shotState struct {
	mu     sync.Mutex
	active bool
	last   []byte // 最近一次截图（PNG），供识字 / 翻译 / 改写 / 存桌面 / 另存为
}

// StartScreenshot 由界面按钮触发：先隐藏主窗口再截图，完成后恢复主窗口。
func (a *App) StartScreenshot() {
	go a.takeScreenshot(true)
}

func (a *App) takeScreenshot(fromUI bool) {
	a.shot.mu.Lock()
	if a.shot.active {
		a.shot.mu.Unlock()
		return
	}
	a.shot.active = true
	a.shot.mu.Unlock()
	release := func() {
		a.shot.mu.Lock()
		a.shot.active = false
		a.shot.mu.Unlock()
	}
	restore := false
	if fromUI {
		restore = a.hideMainForCapture()
	}
	err := lnx.ShowOverlay(lnx.ModeScreenshot, 0, 0, func(r lnx.OverlayResult) {
		defer release()
		ev := a.handleShot(r)
		if restore && ev.Action != "ocr" {
			a.showMain("")
		}
		if ev.Action != "cancel" {
			runtime.EventsEmit(a.ctx, "shot:done", ev)
		}
	})
	if err != nil {
		release()
		if restore {
			a.showMain("")
		}
		runtime.EventsEmit(a.ctx, "shot:done", ShotEvent{Action: "error", Error: "截图失败：" + err.Error()})
		if !restore && !fromUI {
			lnx.Alert(AppName, "截图失败："+err.Error())
		}
	}
}

func (a *App) handleShot(r lnx.OverlayResult) ShotEvent {
	ev := ShotEvent{Action: r.Action, Width: r.Rect.W, Height: r.Rect.H}
	if r.Action == "cancel" || len(r.PNG) == 0 {
		ev.Action = "cancel"
		return ev
	}
	data := r.PNG
	a.shot.mu.Lock()
	a.shot.last = data
	a.shot.mu.Unlock()
	switch r.Action {
	case "desktop":
		p := filepath.Join(desktopDir(), timestampName("截图", ".png"))
		if err := os.WriteFile(p, data, 0o644); err != nil {
			ev.Error = "保存失败：" + err.Error()
			lnx.Alert(AppName, ev.Error)
			return ev
		}
		ev.Path = p
	case "ocr":
		// 识字 / 翻译 / 改写：复制到剪贴板并打开主窗口的截图页
		_ = lnx.SetClipboardPNG(data)
		go a.showMain("shot")
	case "copy":
		if err := lnx.SetClipboardPNG(data); err != nil {
			ev.Error = "复制到剪贴板失败：" + err.Error()
			lnx.Alert(AppName, ev.Error)
		}
		ev.CanSave = true
		if a.clips != nil && a.clipEnabled() {
			if ok, _ := a.clips.AddImage(data); ok {
				a.emitClipChanged()
			}
		}
	case "save":
		path, err := runtime.SaveFileDialog(a.ctx, runtime.SaveDialogOptions{
			Title:            "保存截图",
			DefaultDirectory: desktopDir(),
			DefaultFilename:  timestampName("截图", ".png"),
			Filters:          []runtime.FileFilter{{DisplayName: "PNG 图片 (*.png)", Pattern: "*.png"}},
		})
		if err != nil || path == "" {
			ev.Action = "cancel"
			return ev
		}
		if filepath.Ext(path) == "" {
			path += ".png"
		}
		if err := os.WriteFile(path, data, 0o644); err != nil {
			ev.Error = "保存失败：" + err.Error()
			lnx.Alert(AppName, ev.Error)
			return ev
		}
		ev.Path = path
	}
	return ev
}

// SaveLastShot 把最近一次截图另存为 PNG（默认桌面），返回保存路径；取消时返回空串。
func (a *App) SaveLastShot() (string, error) {
	a.shot.mu.Lock()
	data := a.shot.last
	a.shot.mu.Unlock()
	if len(data) == 0 {
		return "", errors.New("没有可保存的截图")
	}
	path, err := runtime.SaveFileDialog(a.ctx, runtime.SaveDialogOptions{
		Title:            "保存截图",
		DefaultDirectory: desktopDir(),
		DefaultFilename:  timestampName("截图", ".png"),
		Filters:          []runtime.FileFilter{{DisplayName: "PNG 图片 (*.png)", Pattern: "*.png"}},
	})
	if err != nil || path == "" {
		return "", err
	}
	if filepath.Ext(path) == "" {
		path += ".png"
	}
	if err := os.WriteFile(path, data, 0o644); err != nil {
		return "", errors.New("保存失败：" + err.Error())
	}
	return path, nil
}
