package recorder

import (
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

const avfList = `[AVFoundation indev @ 0x13a604a40] AVFoundation video devices:
[AVFoundation indev @ 0x13a604a40] [0] FaceTime HD Camera
[AVFoundation indev @ 0x13a604a40] [1] OBS Virtual Camera
[AVFoundation indev @ 0x13a604a40] [2] Capture screen 0
[AVFoundation indev @ 0x13a604a40] [3] Capture screen 1
[AVFoundation indev @ 0x13a604a40] AVFoundation audio devices:
[AVFoundation indev @ 0x13a604a40] [0] MacBook Pro 麦克风
[AVFoundation indev @ 0x13a604a40] [1] Microsoft Teams Audio
[in#0 @ 0x600000] Error opening input: Input/output error
: Input/output error`

func TestParseAVFoundation(t *testing.T) {
	v, a := ParseAVFoundation(avfList)
	if len(v) != 4 || len(a) != 2 {
		t.Fatalf("video %v audio %v", v, a)
	}
	if a[0].Name != "MacBook Pro 麦克风" || a[1].Index != 1 {
		t.Fatalf("audio %v", a)
	}
	if i, ok := ScreenDevice(v, 1); !ok || i != 3 {
		t.Fatalf("screen 1 -> %d %v", i, ok)
	}
	if _, ok := ScreenDevice(v, 5); ok {
		t.Fatal("screen 5 should not exist")
	}
	if i, ok := AudioIndex(a, "Microsoft Teams Audio"); !ok || i != 1 {
		t.Fatal("audio index")
	}
}

func TestBuildArgsMac(t *testing.T) {
	a, err := BuildArgsMac(MacOptions{Screen: 2, Audio: 0, Crop: &CropFrac{0.1, 0.2, 0.5, 0.25}, FPS: 30, DrawMouse: true, Output: "/Users/me/Desktop/录屏 1.mp4"})
	if err != nil {
		t.Fatal(err)
	}
	s := strings.Join(a, " ")
	for _, w := range []string{"-f avfoundation", "-capture_cursor 1", "-i 2:0", "crop=w=iw*0.500000:h=ih*0.250000:x=iw*0.100000:y=ih*0.200000", "-c:a aac", "libx264", "yuv420p"} {
		if !strings.Contains(s, w) {
			t.Errorf("missing %q in %s", w, s)
		}
	}
	if a[len(a)-1] != "/Users/me/Desktop/录屏 1.mp4" {
		t.Error("output must be last")
	}
	a, _ = BuildArgsMac(MacOptions{Screen: 1, Audio: -1, OutW: 1920, OutH: 1080, Crop: &CropFrac{0, 0, 0.5, 0.5}, Output: "o.mp4"})
	s = strings.Join(a, " ")
	if !strings.Contains(s, "-i 1:none") || !strings.Contains(s, "scale=1920:1080") || strings.Contains(s, "-c:a") {
		t.Errorf("unexpected %s", s)
	}
	if _, err := BuildArgsMac(MacOptions{Screen: 0, Audio: -1, Crop: &CropFrac{0.8, 0, 0.5, 0.5}, Output: "o.mp4"}); err == nil {
		t.Error("crop outside frame should fail")
	}
}

// 用本机 ffmpeg 验证滤镜表达式有效、输出尺寸为偶数（模拟 Retina 2880×1800 采集画面）。
func TestVideoFilterWithFFmpeg(t *testing.T) {
	ffm, err := exec.LookPath("ffmpeg")
	if err != nil {
		t.Skip("ffmpeg not installed")
	}
	probe := func(vf string) string {
		out := filepath.Join(t.TempDir(), "x.mp4")
		cmd := exec.Command(ffm, "-hide_banner", "-loglevel", "error", "-f", "lavfi", "-i", "testsrc=size=2880x1800:rate=30",
			"-frames:v", "3", "-vf", vf, "-c:v", "libx264", "-pix_fmt", "yuv420p", out)
		if b, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("ffmpeg %s: %v %s", vf, err, b)
		}
		b, err := exec.Command("ffprobe", "-v", "error", "-select_streams", "v:0", "-show_entries", "stream=width,height", "-of", "csv=p=0", out).Output()
		if err != nil {
			t.Fatal(err)
		}
		return strings.TrimSpace(string(b))
	}
	// 区域 (101,57)-(101+333,57+211) 点，屏幕 1440×900 点 -> 2 倍像素
	c := &CropFrac{101.0 / 1440, 57.0 / 900, 333.0 / 1440, 211.0 / 900}
	if got := probe(VideoFilter(MacOptions{Crop: c})); got != "666,422" {
		t.Errorf("region size %s", got)
	}
	if got := probe(VideoFilter(MacOptions{Crop: &CropFrac{0.25, 0.2, 960.0 / 1440, 540.0 / 900}, OutW: 1920, OutH: 1080})); got != "1920,1080" {
		t.Errorf("preset size %s", got)
	}
	if got := probe(VideoFilter(MacOptions{})); got != "2880,1800" {
		t.Errorf("full size %s", got)
	}
}
