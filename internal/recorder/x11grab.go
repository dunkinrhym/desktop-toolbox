package recorder

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"regexp"
	"strconv"
	"strings"
	"time"
)

// Linux（X11）录屏：ffmpeg x11grab 采集屏幕区域，可选 PulseAudio 音源（麦克风或“电脑播放的声音”）。

// X11Options Linux 录制参数。Rect 为 X 屏幕物理像素坐标。
type X11Options struct {
	FFmpeg    string
	Display   string // 如 ":0"；空时取 $DISPLAY
	Rect      Rect
	FPS       int
	Pulse     string // PulseAudio 音源名（pactl / ffmpeg -sources pulse 中的名称），空表示不录音
	Output    string
	DrawMouse bool
}

// BuildX11Args 生成 ffmpeg 参数。
func BuildX11Args(o X11Options) ([]string, error) {
	r := Even(o.Rect)
	if r.W < 16 || r.H < 16 {
		return nil, errors.New("录制区域太小（至少 16×16 像素）")
	}
	if r.X < 0 || r.Y < 0 {
		return nil, errors.New("录制区域超出屏幕")
	}
	disp := o.Display
	if disp == "" {
		disp = os.Getenv("DISPLAY")
	}
	if disp == "" {
		return nil, errors.New("没有 X11 显示（DISPLAY 为空）：录屏需要 X11 会话")
	}
	fps := o.FPS
	if fps <= 0 {
		fps = 30
	}
	mouse := "0"
	if o.DrawMouse {
		mouse = "1"
	}
	args := []string{"-hide_banner", "-loglevel", "warning", "-y",
		"-thread_queue_size", "512",
		"-f", "x11grab", "-framerate", strconv.Itoa(fps), "-draw_mouse", mouse,
		"-video_size", fmt.Sprintf("%dx%d", r.W, r.H),
		"-i", fmt.Sprintf("%s+%d,%d", disp, r.X, r.Y)}
	if o.Pulse != "" {
		args = append(args, "-thread_queue_size", "1024", "-f", "pulse", "-i", o.Pulse)
	}
	args = append(args, "-map", "0:v:0")
	if o.Pulse != "" {
		args = append(args, "-map", "1:a:0")
	}
	args = append(args,
		"-c:v", "libx264", "-preset", "ultrafast", "-tune", "zerolatency", "-crf", "23",
		"-pix_fmt", "yuv420p", "-r", strconv.Itoa(fps))
	if o.Pulse != "" {
		args = append(args, "-c:a", "aac", "-b:a", "128k")
	}
	args = append(args, "-movflags", "+faststart", o.Output)
	return args, nil
}

// StartX11 启动 Linux 录制。
func StartX11(o X11Options) (*Session, error) {
	if _, err := os.Stat(o.FFmpeg); err != nil {
		return nil, fmt.Errorf("找不到 ffmpeg（%s），请安装 ffmpeg 软件包", o.FFmpeg)
	}
	args, err := BuildX11Args(o)
	if err != nil {
		return nil, err
	}
	return startProcess(o.FFmpeg, args, o.Output)
}

// PulseSource 一个 PulseAudio 音源。
type PulseSource struct {
	Name    string // 设备名（传给 ffmpeg -f pulse -i）
	Desc    string // 描述
	Monitor bool   // 是否为“电脑播放的声音”（输出设备的监视源）
	Default bool
}

// 行格式：“* 名称 [描述]”，新版 ffmpeg 末尾还有“(audio)”之类的媒体类型。
var rePulseSource = regexp.MustCompile(`^\s*(\*?)\s*(\S+)\s+\[(.*)\](?:\s*\([^()]*\))?\s*$`)

// ParsePulseSources 解析 `ffmpeg -sources pulse` 的输出。
func ParsePulseSources(out string) []PulseSource {
	var res []PulseSource
	sc := bufio.NewScanner(strings.NewReader(out))
	for sc.Scan() {
		line := sc.Text()
		if strings.Contains(line, "Auto-detected sources") {
			continue
		}
		m := rePulseSource.FindStringSubmatch(line)
		if m == nil {
			continue
		}
		res = append(res, PulseSource{Name: m[2], Desc: strings.TrimSpace(m[3]), Monitor: strings.HasSuffix(m[2], ".monitor"), Default: m[1] == "*"})
	}
	return res
}

// ListPulseSources 列出 PulseAudio 音源（需要 PulseAudio / PipeWire-Pulse 服务）。
func ListPulseSources(ffmpeg string) ([]PulseSource, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, ffmpeg, "-hide_banner", "-sources", "pulse")
	var buf bytes.Buffer
	cmd.Stdout = &buf
	cmd.Stderr = &buf
	err := cmd.Run()
	srcs := ParsePulseSources(buf.String())
	if len(srcs) == 0 && err != nil {
		msg := strings.TrimSpace(buf.String())
		if msg == "" {
			msg = err.Error()
		}
		return nil, errors.New(msg)
	}
	return srcs, nil
}

// SourceLabel 界面上显示的音源名称。
func SourceLabel(s PulseSource) string {
	d := s.Desc
	if d == "" {
		d = s.Name
	}
	if s.Monitor {
		d = strings.TrimPrefix(d, "Monitor of ")
		return "电脑播放的声音（" + d + "）"
	}
	return d
}
