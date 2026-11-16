//go:build windows

package main

import (
	"bytes"
	"image"
	"image/png"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/wailsapp/wails/v2/pkg/runtime"

	"desktoptoolbox/internal/win"
)

type shotState struct {
	mu     sync.Mutex
	active bool
}

// ShotEvent 截图结果事件。
type ShotEvent struct {
	Action string `json:"action"`
	Path   string `json:"path"`
	Width  int    `json:"width"`
	Height int    `json:"height"`
	Error  string `json:"error"`
}

// StartScreenshot 由界面按钮触发：先隐藏主窗口再截图，完成后恢复主窗口。
func (a *App) StartScreenshot() {
	go a.takeScreenshot(true)
}

// hideMainForCapture 隐藏主窗口并等待动画结束；返回之前是否可见。
func (a *App) hideMainForCapture() bool {
	visible := win.IsWindowVisibleByClass(MainWinClass)
	if visible {
		runtime.WindowHide(a.ctx)
		time.Sleep(300 * time.Millisecond)
	}
	return visible
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
	err := win.ShowOverlay(win.ModeScreenshot, 0, 0, func(r win.OverlayResult) {
		defer release()
		ev := a.handleShot(r)
		if restore {
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
			win.MessageBox(AppName, "截图失败："+err.Error(), true)
		}
	}
}

func crop(img *image.RGBA, r image.Rectangle) *image.RGBA {
	sub := img.SubImage(r).(*image.RGBA)
	out := image.NewRGBA(image.Rect(0, 0, r.Dx(), r.Dy()))
	for y := 0; y < r.Dy(); y++ {
		copy(out.Pix[y*out.Stride:y*out.Stride+r.Dx()*4], sub.Pix[y*sub.Stride:y*sub.Stride+r.Dx()*4])
	}
	return out
}

func (a *App) handleShot(r win.OverlayResult) ShotEvent {
	ev := ShotEvent{Action: r.Action}
	if r.Action == "cancel" || r.Rect.Empty() {
		ev.Action = "cancel"
		return ev
	}
	img := crop(r.Image, r.Rect)
	ev.Width, ev.Height = img.Bounds().Dx(), img.Bounds().Dy()
	var buf bytes.Buffer
	if err := png.Encode(&buf, img); err != nil {
		ev.Error = "图片编码失败：" + err.Error()
		return ev
	}
	switch r.Action {
	case "copy":
		if err := win.SetClipboardImage(img, buf.Bytes()); err != nil {
			ev.Error = "复制到剪贴板失败：" + err.Error()
			win.MessageBox(AppName, ev.Error, true)
		}
		// 截图也记录到剪贴板历史
		if a.clips != nil && a.clipEnabled() {
			if ok, _ := a.clips.AddImage(buf.Bytes()); ok {
				runtime.EventsEmit(a.ctx, "clip:changed")
			}
		}
	case "save":
		desk := win.DesktopDir()
		name := timestampName("截图", ".png")
		path, ok := win.SaveFileDialog("保存截图", desk, name, "PNG 图片 (*.png)|*.png", "png")
		if !ok {
			ev.Action = "cancel"
			return ev
		}
		if filepath.Ext(path) == "" {
			path += ".png"
		}
		if err := os.WriteFile(path, buf.Bytes(), 0o644); err != nil {
			ev.Error = "保存失败：" + err.Error()
			win.MessageBox(AppName, ev.Error, true)
			return ev
		}
		ev.Path = path
	}
	return ev
}
