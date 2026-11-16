package recorder

import (
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestBuildArgs(t *testing.T) {
	a, err := BuildArgs(Options{Rect: Rect{-1920, 10, 1281, 721}, FPS: 30, Output: `C:\a b\x.mp4`, Microphone: "麦克风 (Realtek(R) Audio)", DrawMouse: true})
	if err != nil {
		t.Fatal(err)
	}
	s := strings.Join(a, " ")
	for _, w := range []string{"-offset_x -1920", "-offset_y 10", "-video_size 1280x720", "audio=麦克风 (Realtek(R) Audio)", "-map 1:a:0", "-c:a aac", "yuv420p", "libx264", `C:\a b\x.mp4`} {
		if !strings.Contains(s, w) {
			t.Errorf("missing %q in %s", w, s)
		}
	}
	if a[len(a)-1] != `C:\a b\x.mp4` {
		t.Errorf("output must be last")
	}
	a, _ = BuildArgs(Options{Rect: Rect{0, 0, 1920, 1080}, Output: "o.mp4"})
	if strings.Contains(strings.Join(a, " "), "dshow") {
		t.Errorf("no mic expected")
	}
	if _, err := BuildArgs(Options{Rect: Rect{0, 0, 5, 5}, Output: "o.mp4"}); err == nil {
		t.Errorf("tiny rect should fail")
	}
}

func TestParseDshow(t *testing.T) {
	newFmt := `[in#0 @ 000001] "Integrated Camera" (video)
[in#0 @ 000001]   Alternative name "@device_pnp_\\?\usb#vid"
[in#0 @ 000001] "麦克风阵列 (Realtek(R) Audio)" (audio)
[in#0 @ 000001]   Alternative name "@device_cm_{33D9A762}\wave_{A1}"
[in#0 @ 000001] "Stereo Mix (Realtek(R) Audio)" (audio)
dummy: Immediate exit requested`
	got := ParseDshowAudio(newFmt)
	if len(got) != 2 || got[0] != "麦克风阵列 (Realtek(R) Audio)" {
		t.Fatalf("new fmt %q", got)
	}
	oldFmt := `[dshow @ 0x1] DirectShow video devices (some may be both video and audio devices)
[dshow @ 0x1]  "USB Camera"
[dshow @ 0x1]     Alternative name "@device_pnp"
[dshow @ 0x1] DirectShow audio devices
[dshow @ 0x1]  "Microphone (USB Audio)"
[dshow @ 0x1]     Alternative name "@device_cm"`
	got = ParseDshowAudio(oldFmt)
	if len(got) != 1 || got[0] != "Microphone (USB Audio)" {
		t.Fatalf("old fmt %q", got)
	}
}

// 用 Linux 上的 ffmpeg + lavfi 测试源验证“写入 q 正常收尾”的停止逻辑。
func TestStopWithQ(t *testing.T) {
	ff, err := exec.LookPath("ffmpeg")
	if err != nil {
		t.Skip("ffmpeg not installed")
	}
	out := filepath.Join(t.TempDir(), "x.mp4")
	args := []string{"-hide_banner", "-loglevel", "warning", "-y", "-re", "-f", "lavfi", "-i", "testsrc=size=320x240:rate=30",
		"-c:v", "libx264", "-preset", "ultrafast", "-pix_fmt", "yuv420p", "-movflags", "+faststart", out}
	s, err := startProcess(ff, args, out)
	if err != nil {
		t.Fatal(err)
	}
	time.Sleep(1500 * time.Millisecond)
	if err := s.Stop(10 * time.Second); err != nil {
		t.Fatalf("stop: %v", err)
	}
	probe, err := exec.Command("ffprobe", "-v", "error", "-show_entries", "format=duration", "-of", "csv=p=0", out).Output()
	if err != nil || len(strings.TrimSpace(string(probe))) == 0 {
		t.Fatalf("ffprobe failed: %v %s", err, probe)
	}
	t.Logf("duration %s", strings.TrimSpace(string(probe)))
}
