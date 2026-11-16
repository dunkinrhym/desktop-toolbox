//go:build darwin

package main

import (
	"bytes"
	"errors"
	"fmt"
	"image/png"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/wailsapp/wails/v2/pkg/runtime"

	"desktoptoolbox/internal/mac"
)

// macOS 截图：调用系统自带的 /usr/sbin/screencapture -i（交互式框选，原生外观，
// 自动处理 Retina 与多显示器；框选时按空格可切换为“窗口截图”，Esc 取消）。
// 截好后自动复制到剪贴板（PNG + TIFF，可直接粘贴到 Word / 微信 / 邮件），并记入剪贴板历史；
// 界面上可“另存为…”保存为 PNG。

type shotState struct {
	mu     sync.Mutex
	active bool
	last   []byte // 最近一次截图（PNG），供“另存为”
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
	defer func() {
		a.shot.mu.Lock()
		a.shot.active = false
		a.shot.mu.Unlock()
	}()

	if err := a.screenPermissionError(); err != nil {
		runtime.EventsEmit(a.ctx, "shot:done", ShotEvent{Action: "error", Error: err.Error()})
		if !fromUI {
			mac.Alert(AppName, err.Error())
		}
		return
	}
	restore := false
	if fromUI {
		restore = a.hideMainForCapture()
	}
	ev := a.captureInteractive()
	if restore {
		a.showMain("")
	}
	if ev.Action == "cancel" {
		return
	}
	runtime.EventsEmit(a.ctx, "shot:done", ev)
	if ev.Error != "" && !restore && !fromUI {
		mac.Alert(AppName, ev.Error)
	}
}

func (a *App) captureInteractive() ShotEvent {
	tmp := filepath.Join(os.TempDir(), fmt.Sprintf("dt-shot-%d.png", time.Now().UnixNano()))
	defer os.Remove(tmp)
	// -i 交互式；-x 不播放快门声；-t png 输出格式；-o 窗口截图不带阴影
	cmd := exec.Command("/usr/sbin/screencapture", "-i", "-x", "-o", "-t", "png", tmp)
	out, err := cmd.CombinedOutput()
	data, rerr := os.ReadFile(tmp)
	if rerr != nil || len(data) == 0 {
		// 用户按 Esc 取消时不会生成文件
		if err != nil && len(strings.TrimSpace(string(out))) > 0 {
			return ShotEvent{Action: "error", Error: "截图失败：" + strings.TrimSpace(string(out))}
		}
		return ShotEvent{Action: "cancel"}
	}
	cfg, err := png.DecodeConfig(bytes.NewReader(data))
	if err != nil {
		return ShotEvent{Action: "error", Error: "截图文件无效：" + err.Error()}
	}
	ev := ShotEvent{Action: "copy", Width: cfg.Width, Height: cfg.Height, CanSave: true}
	a.shot.mu.Lock()
	a.shot.last = data
	a.shot.mu.Unlock()
	if err := mac.SetClipboardPNG(data); err != nil {
		ev.Error = "复制到剪贴板失败：" + err.Error()
	}
	if a.clips != nil && a.clipEnabled() {
		if ok, _ := a.clips.AddImage(data); ok {
			a.emitClipChanged()
		}
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
