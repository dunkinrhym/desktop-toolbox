//go:build linux

package main

import (
	"errors"
	"path/filepath"
	"sync"
	"time"

	"github.com/wailsapp/wails/v2/pkg/runtime"

	"desktoptoolbox/internal/lnx"
	"desktoptoolbox/internal/recorder"
)

// Linux 录屏：系统 FFmpeg（x11grab 采集 X 屏幕区域 + PulseAudio 音源），H.264 + AAC 的 MP4 保存到桌面。
// 区域 / 预设尺寸用与截图相同的框选遮罩；录制中在区域外显示计时条与“停止录制”按钮，托盘菜单也可停止。

type recordState struct {
	mu       sync.Mutex
	session  *recorder.Session
	starting bool
	restore  bool
	sources  map[string]string // 界面名称 → PulseAudio 音源名
}

// ListMicrophones 列出可录制的声音来源（麦克风，以及“电脑播放的声音”）。
func (a *App) ListMicrophones() ([]string, error) {
	srcs, err := recorder.ListPulseSources(a.ffmpeg)
	if err != nil {
		return nil, errors.New("无法获取声音设备列表（需要 PulseAudio 声音服务）：" + err.Error())
	}
	names := []string{}
	m := map[string]string{}
	// 麦克风在前，监视源（电脑播放的声音）在后
	for pass := 0; pass < 2; pass++ {
		for _, s := range srcs {
			if s.Monitor != (pass == 1) {
				continue
			}
			label := recorder.SourceLabel(s)
			for i := 2; m[label] != ""; i++ {
				label = recorder.SourceLabel(s) + " " + string(rune('0'+i))
			}
			m[label] = s.Name
			names = append(names, label)
		}
	}
	a.rec.mu.Lock()
	a.rec.sources = m
	a.rec.mu.Unlock()
	return names, nil
}

// IsRecording 是否正在录制。
func (a *App) IsRecording() bool {
	a.rec.mu.Lock()
	defer a.rec.mu.Unlock()
	return a.rec.session != nil
}

// StartRecording 开始录屏。region/preset 模式会先显示区域选择遮罩。
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
	if req.Mode == "preset" {
		if req.Width <= 0 || req.Height <= 0 {
			return fail(errors.New("预设尺寸无效"))
		}
		sw, sh, _ := lnx.ScreenSize()
		if req.Width > sw || req.Height > sh {
			return fail(errors.New("预设尺寸大于屏幕尺寸"))
		}
	}
	restore := a.hideMainForCapture()
	switch req.Mode {
	case "region", "preset":
		mode, w, h := lnx.ModeRecord, 0, 0
		if req.Mode == "preset" {
			mode, w, h = lnx.ModeRecordFixed, req.Width, req.Height
		}
		err := lnx.ShowOverlay(mode, w, h, func(r lnx.OverlayResult) {
			if r.Action != "record" || r.Rect.W < 4 || r.Rect.H < 4 {
				a.rec.mu.Lock()
				a.rec.starting = false
				a.rec.mu.Unlock()
				if restore {
					a.showMain("")
				}
				return
			}
			time.Sleep(200 * time.Millisecond) // 等遮罩消失
			a.beginRecording(req, r.Rect, restore)
		})
		if err != nil {
			if restore {
				a.showMain("")
			}
			return fail(errors.New("无法显示区域选择：" + err.Error()))
		}
		return nil
	default:
		go a.beginRecording(req, lnx.MonitorAtPointer(), restore)
		return nil
	}
}

func (a *App) beginRecording(req RecordRequest, rect lnx.Rect, restore bool) {
	fail := func(msg string) {
		a.rec.mu.Lock()
		a.rec.starting = false
		a.rec.mu.Unlock()
		if restore {
			a.showMain("")
		}
		runtime.EventsEmit(a.ctx, "rec:state", RecordEvent{Error: msg})
		if !restore {
			lnx.Alert(AppName, msg)
		}
	}
	pulse := ""
	if req.Microphone != "" {
		a.rec.mu.Lock()
		pulse = a.rec.sources[req.Microphone]
		a.rec.mu.Unlock()
		if pulse == "" { // 界面记住了上次的设备但本次还没刷新列表
			_, _ = a.ListMicrophones()
			a.rec.mu.Lock()
			pulse = a.rec.sources[req.Microphone]
			a.rec.mu.Unlock()
		}
		if pulse == "" {
			fail("找不到声音设备“" + req.Microphone + "”，请刷新设备列表")
			return
		}
	}
	fps := req.FPS
	if fps <= 0 || fps > 60 {
		fps = 30
	}
	r := recorder.Rect{X: rect.X, Y: rect.Y, W: rect.W, H: rect.H}
	if req.Mode == "preset" {
		r.W, r.H = req.Width, req.Height
	}
	r = recorder.Even(r)
	out := filepath.Join(desktopDir(), timestampName("录屏", ".mp4"))
	s, err := recorder.StartX11(recorder.X11Options{
		FFmpeg: a.ffmpeg, Rect: r, FPS: fps, Pulse: pulse, Output: out, DrawMouse: req.Cursor,
	})
	if err != nil {
		fail("录屏启动失败：" + err.Error())
		return
	}
	a.rec.mu.Lock()
	a.rec.starting = false
	a.rec.session = s
	a.rec.restore = restore
	a.rec.mu.Unlock()

	runtime.EventsEmit(a.ctx, "rec:state", RecordEvent{Recording: true, Started: s.Started.UnixMilli(), Output: out, Width: r.W, Height: r.H})
	lnx.ShowRecordingBar(lnx.Rect{X: r.X, Y: r.Y, W: r.W, H: r.H}, s.Started.UnixMilli())

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
			lnx.HideRecordingBar()
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
	a.rec.mu.Unlock()
	if s == nil {
		return errors.New("当前没有在录制")
	}
	lnx.HideRecordingBar()
	runtime.EventsEmit(a.ctx, "rec:state", RecordEvent{Recording: false})
	err := s.Stop(20 * time.Second)
	ev := RecordEvent{Output: s.Output}
	if err != nil {
		ev.Error = err.Error()
	}
	runtime.EventsEmit(a.ctx, "rec:saved", ev)
	a.showMain("record") // 无论从哪里开始录制，结束后都显示主窗口告知保存位置
	return err
}

// stopRecordingSync 退出时结束录制（GTK 主循环可能已停止，不操作悬浮条）。
func (a *App) stopRecordingSync() {
	a.rec.mu.Lock()
	s := a.rec.session
	a.rec.session = nil
	a.rec.mu.Unlock()
	if s != nil {
		_ = s.Stop(10 * time.Second)
	}
}
