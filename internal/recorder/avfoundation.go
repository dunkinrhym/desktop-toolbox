package recorder

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"

	"desktoptoolbox/internal/proc"
)

// 本文件是 macOS 录屏（ffmpeg avfoundation 输入）的参数构建与设备解析。纯 Go，可在 Linux 上测试。

// Device avfoundation 设备（索引 + 名称）。
type Device struct {
	Index int
	Name  string
}

// CropFrac 以采集画面宽高为 1 的比例表示的裁剪区域（与 Retina 缩放无关）。
type CropFrac struct {
	X, Y, W, H float64
}

// MacOptions macOS 录制参数。
type MacOptions struct {
	FFmpeg    string
	Screen    int       // avfoundation 视频设备索引（"Capture screen N" 的索引）
	Audio     int       // avfoundation 音频设备索引，<0 表示不录音
	Crop      *CropFrac // nil 表示整个屏幕
	OutW      int       // >0 时把画面缩放为固定输出尺寸（预设 1920×1080 等）
	OutH      int
	FPS       int
	DrawMouse bool
	Output    string
}

var (
	reAVFIndex  = regexp.MustCompile(`\]\s*\[(\d+)\]\s+(.+?)\s*$`)
	reCapScreen = regexp.MustCompile(`^Capture screen (\d+)$`)
)

// ParseAVFoundation 解析 `ffmpeg -f avfoundation -list_devices true -i ""` 的输出。
func ParseAVFoundation(out string) (video, audio []Device) {
	section := ""
	sc := bufio.NewScanner(strings.NewReader(out))
	for sc.Scan() {
		line := sc.Text()
		switch {
		case strings.Contains(line, "AVFoundation video devices"):
			section = "v"
			continue
		case strings.Contains(line, "AVFoundation audio devices"):
			section = "a"
			continue
		}
		m := reAVFIndex.FindStringSubmatch(line)
		if m == nil || section == "" {
			continue
		}
		idx, _ := strconv.Atoi(m[1])
		d := Device{Index: idx, Name: m[2]}
		if section == "v" {
			video = append(video, d)
		} else {
			audio = append(audio, d)
		}
	}
	return
}

// ScreenDevice 返回第 displayIndex 个显示器（CGGetActiveDisplayList 顺序）对应的 avfoundation 设备索引。
func ScreenDevice(video []Device, displayIndex int) (int, bool) {
	for _, d := range video {
		if m := reCapScreen.FindStringSubmatch(d.Name); m != nil {
			if n, _ := strconv.Atoi(m[1]); n == displayIndex {
				return d.Index, true
			}
		}
	}
	return 0, false
}

// AudioIndex 按名称查找麦克风索引。
func AudioIndex(audio []Device, name string) (int, bool) {
	for _, d := range audio {
		if d.Name == name {
			return d.Index, true
		}
	}
	return 0, false
}

// ListAVFoundation 列出 avfoundation 视频（含屏幕）与音频设备。
func ListAVFoundation(ffmpeg string) (video, audio []Device, err error) {
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, ffmpeg, "-hide_banner", "-f", "avfoundation", "-list_devices", "true", "-i", "")
	proc.Hide(cmd)
	var buf bytes.Buffer
	cmd.Stderr = &buf
	cmd.Stdout = &buf
	_ = cmd.Run() // 该命令总是以错误码退出
	if buf.Len() == 0 {
		return nil, nil, errors.New("无法运行 ffmpeg")
	}
	video, audio = ParseAVFoundation(buf.String())
	return video, audio, nil
}

func ff(v float64) string { return strconv.FormatFloat(v, 'f', 6, 64) }

// VideoFilter 生成裁剪/缩放滤镜：裁剪用比例表达式（iw/ih），输出宽高保证为偶数（libx264 yuv420p 要求）。
func VideoFilter(o MacOptions) string {
	var f []string
	if c := o.Crop; c != nil {
		f = append(f, fmt.Sprintf("crop=w=iw*%s:h=ih*%s:x=iw*%s:y=ih*%s", ff(c.W), ff(c.H), ff(c.X), ff(c.Y)))
	}
	if o.OutW > 0 && o.OutH > 0 {
		f = append(f, fmt.Sprintf("scale=%d:%d", o.OutW&^1, o.OutH&^1))
	} else {
		f = append(f, "scale=trunc(iw/2)*2:trunc(ih/2)*2")
	}
	return strings.Join(f, ",")
}

// BuildArgsMac 生成 macOS avfoundation 录屏的 ffmpeg 参数。
func BuildArgsMac(o MacOptions) ([]string, error) {
	if o.Screen < 0 {
		return nil, errors.New("无效的屏幕设备")
	}
	if c := o.Crop; c != nil {
		if c.W <= 0 || c.H <= 0 || c.X < 0 || c.Y < 0 || c.X+c.W > 1.0001 || c.Y+c.H > 1.0001 {
			return nil, errors.New("录制区域无效")
		}
	}
	fps := o.FPS
	if fps <= 0 || fps > 60 {
		fps = 30
	}
	cursor := "0"
	if o.DrawMouse {
		cursor = "1"
	}
	audio := "none"
	if o.Audio >= 0 {
		audio = strconv.Itoa(o.Audio)
	}
	args := []string{"-hide_banner", "-loglevel", "warning", "-y",
		"-thread_queue_size", "1024",
		"-f", "avfoundation", "-capture_cursor", cursor, "-framerate", strconv.Itoa(fps), "-pixel_format", "uyvy422",
		"-i", strconv.Itoa(o.Screen) + ":" + audio,
		"-vf", VideoFilter(o),
		"-c:v", "libx264", "-preset", "ultrafast", "-tune", "zerolatency", "-crf", "23",
		"-pix_fmt", "yuv420p", "-r", strconv.Itoa(fps)}
	if o.Audio >= 0 {
		args = append(args, "-c:a", "aac", "-b:a", "128k")
	}
	args = append(args, "-movflags", "+faststart", o.Output)
	return args, nil
}

// StartMac 启动 macOS 录制。
func StartMac(o MacOptions) (*Session, error) {
	if _, err := os.Stat(o.FFmpeg); err != nil {
		return nil, fmt.Errorf("找不到录屏组件 %s（%s），请重新安装", filepath.Base(o.FFmpeg), o.FFmpeg)
	}
	args, err := BuildArgsMac(o)
	if err != nil {
		return nil, err
	}
	return startProcess(o.FFmpeg, args, o.Output)
}
