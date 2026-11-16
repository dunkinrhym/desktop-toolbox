//go:build windows

package main

import (
	"errors"
	"image"
	"path/filepath"
	"sync"
	"time"

	"github.com/wailsapp/wails/v2/pkg/runtime"

	"desktoptoolbox/internal/recorder"
	"desktoptoolbox/internal/win"
)

type recordState struct {
	mu       sync.Mutex
	session  *recorder.Session
	starting bool
	restore  bool
}

// ListMicrophones 列出麦克风。
func (a *App) ListMicrophones() ([]string, error) {
	devs, err := recorder.ListMicrophones(a.ffmpeg)
	if err != nil {
		return nil, errors.New("无法获取麦克风列表：" + err.Error())
	}
	if devs == nil {
		devs = []string{}
	}
	return devs, nil
}

// IsRecording 是否正在录制。
func (a *App) IsRecording() bool {
	a.rec.mu.Lock()
	defer a.rec.mu.Unlock()
	return a.rec.session != nil
}

// StartRecording 开始录屏。region/preset 模式会先弹出区域选择。
func (a *App) StartRecording(req RecordRequest) error {
	a.rec.mu.Lock()
	if a.rec.session != nil || a.rec.starting {
		a.rec.mu.Unlock()
		return errors.New("已经在录制中")
	}
	a.rec.starting = true
	a.rec.mu.Unlock()
	fail := func(err error) error {
		a.rec.mu.Lock()
		a.rec.starting = false
		a.rec.mu.Unlock()
		return err
	}
	restore := a.hideMainForCapture()
	switch req.Mode {
	case "region", "preset":
		mode := win.ModeRecord
		w, h := 0, 0
		if req.Mode == "preset" {
			mode = win.ModeRecordFixed
			w, h = req.Width, req.Height
			vs := win.VirtualScreen()
			if w <= 0 || h <= 0 {
				if restore {
					a.showMain("")
				}
				return fail(errors.New("预设尺寸无效"))
			}
			if w > vs.Dx() || h > vs.Dy() {
				if restore {
					a.showMain("")
				}
				return fail(errors.New("预设尺寸大于屏幕尺寸"))
			}
		}
		err := win.ShowOverlay(mode, w, h, func(r win.OverlayResult) {
			if r.Action != "record" || r.Screen.Empty() {
				a.rec.mu.Lock()
				a.rec.starting = false
				a.rec.mu.Unlock()
				if restore {
					a.showMain("")
				}
				return
			}
			time.Sleep(150 * time.Millisecond) // 等遮罩消失
			a.beginRecording(req, r.Screen, restore)
		})
		if err != nil {
			if restore {
				a.showMain("")
			}
			return fail(errors.New("无法显示区域选择：" + err.Error()))
		}
		return nil
	default:
		go a.beginRecording(req, win.CursorMonitor(), restore)
		return nil
	}
}

func (a *App) beginRecording(req RecordRequest, rect image.Rectangle, restore bool) {
	out := filepath.Join(win.DesktopDir(), timestampName("录屏", ".mp4"))
	r := recorder.Even(recorder.Rect{X: rect.Min.X, Y: rect.Min.Y, W: rect.Dx(), H: rect.Dy()})
	fps := req.FPS
	if fps <= 0 || fps > 60 {
		fps = 30
	}
	s, err := recorder.Start(recorder.Options{
		FFmpeg: a.ffmpeg, Rect: r, FPS: fps, Microphone: req.Microphone, Output: out, DrawMouse: req.Cursor,
	})
	a.rec.mu.Lock()
	a.rec.starting = false
	if err == nil {
		a.rec.session = s
		a.rec.restore = restore
	}
	a.rec.mu.Unlock()
	if err != nil {
		if restore {
			a.showMain("")
		}
		msg := "录屏启动失败：" + err.Error()
		runtime.EventsEmit(a.ctx, "rec:state", RecordEvent{Error: msg})
		if !restore {
			win.MessageBox(AppName, msg, true)
		}
		return
	}
	runtime.EventsEmit(a.ctx, "rec:state", RecordEvent{Recording: true, Started: s.Started.UnixMilli(), Output: out, Width: r.W, Height: r.H})
	if err := win.ShowRecordingUI(rect, func() { a.StopRecording() }); err != nil {
		runtime.EventsEmit(a.ctx, "rec:state", RecordEvent{Recording: true, Started: s.Started.UnixMilli(), Output: out, Error: "悬浮停止按钮无法显示，请按 Alt+Space 打开主窗口停止录制"})
	}
	// ffmpeg 意外退出时通知
	go func() {
		<-s.Done()
		a.rec.mu.Lock()
		still := a.rec.session == s
		if still {
			a.rec.session = nil
		}
		a.rec.mu.Unlock()
		if still {
			win.HideRecordingUI()
			runtime.EventsEmit(a.ctx, "rec:state", RecordEvent{Error: "录制意外中止：" + s.Log()})
			a.showMain("record")
		}
	}()
}

// StopRecording 停止录屏并保存。
func (a *App) StopRecording() error {
	a.rec.mu.Lock()
	s := a.rec.session
	a.rec.session = nil
	restore := a.rec.restore
	a.rec.mu.Unlock()
	if s == nil {
		return errors.New("当前没有在录制")
	}
	win.HideRecordingUI()
	runtime.EventsEmit(a.ctx, "rec:state", RecordEvent{Recording: false, Output: "", Error: ""})
	err := s.Stop(20 * time.Second)
	ev := RecordEvent{Output: s.Output}
	if err != nil {
		ev.Error = err.Error()
	}
	runtime.EventsEmit(a.ctx, "rec:saved", ev)
	_ = restore // 无论从哪里开始录制，结束后都显示主窗口告知保存位置
	a.showMain("record")
	return err
}

func (a *App) stopRecordingSync() {
	a.rec.mu.Lock()
	s := a.rec.session
	a.rec.session = nil
	a.rec.mu.Unlock()
	if s != nil {
		_ = s.Stop(10 * time.Second)
	}
}
