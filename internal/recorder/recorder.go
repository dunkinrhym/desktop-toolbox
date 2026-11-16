// Package recorder 通过 ffmpeg (gdigrab + 可选 dshow 麦克风) 录制屏幕为 MP4。
package recorder

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"

	"desktoptoolbox/internal/proc"
)

// Rect 物理像素坐标（虚拟桌面坐标系，可为负）。
type Rect struct {
	X int `json:"x"`
	Y int `json:"y"`
	W int `json:"w"`
	H int `json:"h"`
}

// Options 录制参数。
type Options struct {
	FFmpeg     string
	Rect       Rect
	FPS        int
	Microphone string // dshow 音频设备名，空表示不录音
	Output     string
	DrawMouse  bool
}

// Even 把宽高调整为偶数（libx264 yuv420p 要求）。
func Even(r Rect) Rect {
	r.W &^= 1
	r.H &^= 1
	return r
}

// BuildArgs 生成 ffmpeg 参数。
func BuildArgs(o Options) ([]string, error) {
	r := Even(o.Rect)
	if r.W < 16 || r.H < 16 {
		return nil, errors.New("录制区域太小（至少 16×16 像素）")
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
		"-f", "gdigrab", "-framerate", strconv.Itoa(fps), "-draw_mouse", mouse,
		"-offset_x", strconv.Itoa(r.X), "-offset_y", strconv.Itoa(r.Y),
		"-video_size", fmt.Sprintf("%dx%d", r.W, r.H),
		"-i", "desktop"}
	if o.Microphone != "" {
		args = append(args, "-thread_queue_size", "1024", "-f", "dshow", "-i", "audio="+o.Microphone)
	}
	args = append(args,
		"-map", "0:v:0")
	if o.Microphone != "" {
		args = append(args, "-map", "1:a:0")
	}
	args = append(args,
		"-c:v", "libx264", "-preset", "ultrafast", "-tune", "zerolatency", "-crf", "23",
		"-pix_fmt", "yuv420p", "-r", strconv.Itoa(fps))
	if o.Microphone != "" {
		args = append(args, "-c:a", "aac", "-b:a", "128k")
	}
	args = append(args, "-movflags", "+faststart", o.Output)
	return args, nil
}

var (
	reNewAudio = regexp.MustCompile(`"([^"]+)"\s+\(audio\)`)
	reQuoted   = regexp.MustCompile(`\]\s+"([^"]+)"\s*$`)
)

// ParseDshowAudio 解析 `ffmpeg -list_devices true -f dshow -i dummy` 的输出，返回音频设备名。
func ParseDshowAudio(out string) []string {
	var devs []string
	seen := map[string]bool{}
	add := func(n string) {
		if n != "" && !seen[n] {
			seen[n] = true
			devs = append(devs, n)
		}
	}
	inAudio := false
	sc := bufio.NewScanner(strings.NewReader(out))
	for sc.Scan() {
		line := sc.Text()
		if strings.Contains(line, "Alternative name") {
			continue
		}
		if m := reNewAudio.FindStringSubmatch(line); m != nil {
			add(m[1])
			continue
		}
		// 旧格式：先出现 "DirectShow audio devices" 标题，后面每行是设备名
		if strings.Contains(line, "DirectShow audio devices") {
			inAudio = true
			continue
		}
		if strings.Contains(line, "DirectShow video devices") {
			inAudio = false
			continue
		}
		if inAudio {
			if m := reQuoted.FindStringSubmatch(line); m != nil && !strings.Contains(line, "(video)") {
				add(m[1])
			}
		}
	}
	return devs
}

// ListMicrophones 列出麦克风设备。
func ListMicrophones(ffmpeg string) ([]string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, ffmpeg, "-hide_banner", "-list_devices", "true", "-f", "dshow", "-i", "dummy")
	proc.Hide(cmd)
	var buf bytes.Buffer
	cmd.Stderr = &buf
	cmd.Stdout = &buf
	_ = cmd.Run() // 该命令总是以错误码退出
	if buf.Len() == 0 {
		return nil, errors.New("无法运行 ffmpeg")
	}
	return ParseDshowAudio(buf.String()), nil
}

// Session 一次录制。
type Session struct {
	cmd     *exec.Cmd
	stdin   io.WriteCloser
	stderr  *tailBuffer
	done    chan struct{}
	waitErr error
	Started time.Time
	Output  string
	once    sync.Once
}

// Start 启动录制。
func Start(o Options) (*Session, error) {
	if _, err := os.Stat(o.FFmpeg); err != nil {
		return nil, fmt.Errorf("找不到录屏组件 ffmpeg.exe（%s），请重新安装", o.FFmpeg)
	}
	args, err := BuildArgs(o)
	if err != nil {
		return nil, err
	}
	return startProcess(o.FFmpeg, args, o.Output)
}

func startProcess(ffmpeg string, args []string, output string) (*Session, error) {
	cmd := exec.Command(ffmpeg, args...)
	proc.Hide(cmd)
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return nil, err
	}
	tb := &tailBuffer{max: 8192}
	cmd.Stderr = tb
	cmd.Stdout = tb
	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("启动 ffmpeg 失败：%w", err)
	}
	s := &Session{cmd: cmd, stdin: stdin, stderr: tb, done: make(chan struct{}), Started: time.Now(), Output: output}
	go func() {
		s.waitErr = cmd.Wait()
		close(s.done)
	}()
	// 若 ffmpeg 立即退出（参数/设备错误），尽早报告
	select {
	case <-s.done:
		return nil, fmt.Errorf("ffmpeg 启动后立即退出：%s", s.Log())
	case <-time.After(700 * time.Millisecond):
	}
	return s, nil
}

// Done 录制进程结束时关闭。
func (s *Session) Done() <-chan struct{} { return s.done }

// Log 返回 ffmpeg 最近的输出。
func (s *Session) Log() string { return strings.TrimSpace(s.stderr.String()) }

// Stop 发送 q 让 ffmpeg 正常收尾；超时后强制结束。
func (s *Session) Stop(timeout time.Duration) error {
	s.once.Do(func() {
		_, _ = s.stdin.Write([]byte("q"))
		_ = s.stdin.Close()
	})
	select {
	case <-s.done:
	case <-time.After(timeout):
		_ = s.cmd.Process.Kill()
		<-s.done
		return errors.New("ffmpeg 未能及时结束，已强制停止，视频可能不完整")
	}
	if st, err := os.Stat(s.Output); err != nil || st.Size() == 0 {
		return fmt.Errorf("录制失败：%s", s.Log())
	}
	return nil
}

type tailBuffer struct {
	mu  sync.Mutex
	buf []byte
	max int
}

func (t *tailBuffer) Write(p []byte) (int, error) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.buf = append(t.buf, p...)
	if len(t.buf) > t.max {
		t.buf = t.buf[len(t.buf)-t.max:]
	}
	return len(p), nil
}

func (t *tailBuffer) String() string {
	t.mu.Lock()
	defer t.mu.Unlock()
	return string(t.buf)
}
