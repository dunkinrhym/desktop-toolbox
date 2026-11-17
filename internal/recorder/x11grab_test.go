package recorder

import (
	"strings"
	"testing"
)

func TestBuildX11Args(t *testing.T) {
	args, err := BuildX11Args(X11Options{FFmpeg: "ffmpeg", Display: ":1", Rect: Rect{X: 10, Y: 20, W: 641, H: 481}, FPS: 25, Pulse: "default", Output: "/tmp/a.mp4", DrawMouse: true})
	if err != nil {
		t.Fatal(err)
	}
	s := strings.Join(args, " ")
	for _, want := range []string{"-f x11grab", "-video_size 640x480", "-i :1+10,20", "-f pulse -i default", "-map 1:a:0", "-draw_mouse 1", "-framerate 25"} {
		if !strings.Contains(s, want) {
			t.Errorf("缺少 %q：%s", want, s)
		}
	}
	if _, err := BuildX11Args(X11Options{Display: ":0", Rect: Rect{W: 8, H: 8}}); err == nil {
		t.Error("区域太小应报错")
	}
}

func TestParsePulseSources(t *testing.T) {
	out := `Auto-detected sources for pulse:
  alsa_output.pci-0000_00_1f.3.analog-stereo.monitor [Monitor of 内置音频 模拟立体声]
* alsa_input.pci-0000_00_1f.3.analog-stereo [内置音频 模拟立体声] (audio)
`
	s := ParsePulseSources(out)
	if len(s) != 2 || !s[0].Monitor || s[1].Monitor || !s[1].Default || s[1].Name != "alsa_input.pci-0000_00_1f.3.analog-stereo" {
		t.Fatalf("%+v", s)
	}
	if l := SourceLabel(s[0]); l != "电脑播放的声音（内置音频 模拟立体声）" {
		t.Fatal(l)
	}
}
