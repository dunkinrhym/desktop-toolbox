//go:build darwin

package main

import (
	"errors"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/wailsapp/wails/v2/pkg/runtime"

	"desktoptoolbox/internal/mac"
	"desktoptoolbox/internal/recorder"
)

// macOS 录屏：内置 FFmpeg（avfoundation 输入，按显示器采集），区域/预设尺寸用 crop 滤镜裁剪。
// 区域选择使用原生全屏透明遮罩（鼠标所在显示器）；录制中显示悬浮计时条（不会被录进视频）
// 并在菜单栏图标旁显示计时，菜单中可“停止录屏”。

type recordState struct {
	mu       sync.Mutex
	session  *recorder.Session
	starting bool
	restore  bool
}

// ListMicrophones 列出麦克风。
func (a *App) ListMicrophones() ([]string, error) {
	_, audio, err := recorder.ListAVFoundation(a.ffmpeg)
	if err != nil {
		return nil, errors.New("无法获取麦克风列表：" + err.Error())
	}
	devs := []string{}
	for _, d := range audio {
		devs = append(devs, d.Name)
	}
	return devs, nil
}

// IsRecording 是否正在录制。
func (a *App) IsRecording() bool {
	a.rec.mu.Lock()
	defer a.rec.mu.Unlock()
	return a.rec.session != nil
}

// StartRecording 开始录屏。region/preset 模式会先显示区域选择遮罩。
func (a *App) StartRecording(req RecordRequest) error {
	if err := a.screenPermissionError(); err != nil {
		return err
	}
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
	if req.Mode == "preset" && (req.Width <= 0 || req.Height <= 0) {
		return fail(errors.New("预设尺寸无效"))
	}
	restore := a.hideMainForCapture()
	switch req.Mode {
	case "region", "preset":
		w, h := 0, 0
		if req.Mode == "preset" {
			w, h = req.Width, req.Height
		}
		mac.SelectRegion(w, h, func(r mac.Region) {
			if !r.OK || r.W < 4 || r.H < 4 {
				a.rec.mu.Lock()
				a.rec.starting = false
				a.rec.mu.Unlock()
				if restore {
					a.showMain("")
				}
				return
			}
			time.Sleep(200 * time.Millisecond) // 等遮罩消失
			a.beginRecording(req, r, restore)
		})
		return nil
	default:
		go a.beginRecording(req, mac.ScreenUnderMouse(), restore)
		return nil
	}
}

// cropFor 把点坐标区域换算为采集画面的比例裁剪；整屏返回 nil。
func cropFor(r mac.Region) *recorder.CropFrac {
	if r.ScreenW <= 0 || r.ScreenH <= 0 {
		return nil
	}
	if r.X <= 0 && r.Y <= 0 && r.W >= r.ScreenW && r.H >= r.ScreenH {
		return nil
	}
	c := &recorder.CropFrac{X: r.X / r.ScreenW, Y: r.Y / r.ScreenH, W: r.W / r.ScreenW, H: r.H / r.ScreenH}
	if c.X+c.W > 1 {
		c.W = 1 - c.X
	}
	if c.Y+c.H > 1 {
		c.H = 1 - c.Y
	}
	return c
}

func (a *App) beginRecording(req RecordRequest, region mac.Region, restore bool) {
	fail := func(msg string) {
		a.rec.mu.Lock()
		a.rec.starting = false
		a.rec.mu.Unlock()
		if restore {
			a.showMain("")
		}
		runtime.EventsEmit(a.ctx, "rec:state", RecordEvent{Error: msg})
		if !restore {
			mac.Alert(AppName, msg)
		}
	}
	video, audio, err := recorder.ListAVFoundation(a.ffmpeg)
	if err != nil {
		fail("录屏组件无法运行：" + err.Error())
		return
	}
	screen, ok := recorder.ScreenDevice(video, region.DisplayIndex)
	if !ok {
		fail("找不到可录制的屏幕（请确认已在“系统设置 → 隐私与安全性 → 屏幕录制”中允许“桌面工具箱”）")
		return
	}
	mic := -1
	if req.Microphone != "" {
		idx, ok := recorder.AudioIndex(audio, req.Microphone)
		if !ok {
			fail("找不到麦克风“" + req.Microphone + "”，请刷新设备列表")
			return
		}
		mic = idx
	}
	fps := req.FPS
	if fps <= 0 || fps > 60 {
		fps = 30
	}
	out := filepath.Join(desktopDir(), timestampName("录屏", ".mp4"))
	opts := recorder.MacOptions{
		FFmpeg: a.ffmpeg, Screen: screen, Audio: mic, Crop: cropFor(region), FPS: fps,
		DrawMouse: req.Cursor, Output: out,
	}
	outW := int(region.W*region.Scale) &^ 1
	outH := int(region.H*region.Scale) &^ 1
	if req.Mode == "preset" {
		opts.OutW, opts.OutH = req.Width, req.Height
		outW, outH = req.Width&^1, req.Height&^1
	}
	if outW < 16 || outH < 16 {
		fail("录制区域太小（至少 16×16 像素）")
		return
	}
	s, err := recorder.StartMac(opts)
	if err != nil {
		msg := "录屏启动失败：" + err.Error()
		if req.Microphone != "" && strings.Contains(strings.ToLower(err.Error()), "audio") {
			msg += "\n（如果是第一次录制声音，请在“系统设置 → 隐私与安全性 → 麦克风”中允许“桌面工具箱”）"
		}
		fail(msg)
		return
	}
	a.rec.mu.Lock()
	a.rec.starting = false
	a.rec.session = s
	a.rec.restore = restore
	a.rec.mu.Unlock()

	runtime.EventsEmit(a.ctx, "rec:state", RecordEvent{Recording: true, Started: s.Started.UnixMilli(), Output: out, Width: outW, Height: outH})
	mac.ShowRecordingBar(float64(s.Started.UnixNano())/1e9, func() { _ = a.StopRecording() })

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
			mac.HideRecordingBar()
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
	mac.HideRecordingBar()
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

func (a *App) stopRecordingSync() {
	a.rec.mu.Lock()
	s := a.rec.session
	a.rec.session = nil
	a.rec.mu.Unlock()
	if s != nil {
		mac.HideRecordingBar()
		_ = s.Stop(10 * time.Second)
	}
}
