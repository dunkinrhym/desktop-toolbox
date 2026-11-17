//go:build linux

package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"image"
	"image/color"
	"image/png"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"time"

	"desktoptoolbox/internal/cliphist"
	"desktoptoolbox/internal/lnx"
	"desktoptoolbox/internal/recorder"
	"desktoptoolbox/internal/search"
)

// 隐藏的自检模式：desktop-toolbox --selftest [--selftest-out=<目录>]
// 不启动主界面，依次检查：GTK / X11、全局热键注册（并用 XTest 触发一次）、剪贴板读写（文本 + 图片）与监听
// （由子进程充当“其他程序”写入）、截屏为 PNG、截图框选遮罩（XTest 拖动 + 回车 / Esc / 固定尺寸单击）、
// 托盘图标、文件名/内容搜索、内置 Python 转换组件、FFmpeg x11grab（+ PulseAudio）录屏 4 秒并用 ffprobe 校验。
// 结果打印到标准输出并写入 <目录>/selftest.log。全部通过返回退出码 0，否则 1。

func selfTestRequested() (bool, string) {
	on, out := false, ""
	for _, a := range os.Args[1:] {
		switch {
		case a == "--selftest":
			on = true
		case strings.HasPrefix(a, "--selftest-out="):
			out = strings.TrimPrefix(a, "--selftest-out=")
		}
	}
	return on, out
}

// clipOwnerRequested 自检辅助：--selftest-clip-owner=text:<文本> | png:<文件>，写入剪贴板后保持 4 秒再退出。
func clipOwnerRequested() (string, bool) {
	for _, a := range os.Args[1:] {
		if strings.HasPrefix(a, "--selftest-clip-owner=") {
			return strings.TrimPrefix(a, "--selftest-clip-owner="), true
		}
	}
	return "", false
}

func runClipOwner(v string) int {
	runtime.LockOSThread()
	if !lnx.GtkInit() {
		fmt.Fprintln(os.Stderr, "GTK 初始化失败")
		return 1
	}
	go func() {
		switch {
		case strings.HasPrefix(v, "text:"):
			_ = lnx.SetClipboardText(strings.TrimPrefix(v, "text:"))
		case strings.HasPrefix(v, "png:"):
			b, err := os.ReadFile(strings.TrimPrefix(v, "png:"))
			if err == nil {
				_ = lnx.SetClipboardPNG(b)
			}
		}
		time.Sleep(4 * time.Second)
		lnx.GtkQuit()
	}()
	lnx.GtkMain()
	return 0
}

type selfTest struct {
	w      io.Writer
	failed int
}

func (s *selfTest) logf(format string, args ...any) { fmt.Fprintf(s.w, format+"\n", args...) }

func (s *selfTest) check(name string, fn func() (string, error)) {
	start := time.Now()
	detail, err := func() (d string, e error) {
		defer func() {
			if r := recover(); r != nil {
				e = fmt.Errorf("panic: %v", r)
			}
		}()
		return fn()
	}()
	ms := time.Since(start).Milliseconds()
	if err != nil {
		s.failed++
		s.logf("FAIL  %-22s %v (%dms)", name, err, ms)
		return
	}
	s.logf("PASS  %-22s %s (%dms)", name, detail, ms)
}

func runSelfTest(outDir string) int {
	runtime.LockOSThread() // GTK 主循环必须在主线程
	if outDir == "" {
		outDir = filepath.Join(os.TempDir(), "DesktopToolbox-selftest")
	}
	_ = os.MkdirAll(outDir, 0o755)
	sinks := bestEffort{os.Stdout}
	if f, err := os.Create(filepath.Join(outDir, "selftest.log")); err == nil {
		defer f.Close()
		sinks = append(sinks, f)
	}
	s := &selfTest{w: sinks}
	app := NewApp()
	s.logf("桌面工具箱 %s 自检（Linux %s）  exe=%s  DISPLAY=%s", AppVersion, runtime.GOARCH, app.exeDir, os.Getenv("DISPLAY"))
	if os.Getenv("GDK_BACKEND") == "" {
		os.Setenv("GDK_BACKEND", "x11")
	}
	if !lnx.GtkInit() {
		s.logf("FAIL  gtk-init             无法初始化 GTK（没有 X11 显示？）")
		s.logf("RESULT: 1 check(s) FAILED")
		return 1
	}
	code := make(chan int, 1)
	go func() {
		code <- s.run(app, outDir)
		lnx.GtkQuit()
	}()
	lnx.GtkMain()
	return <-code
}

func (s *selfTest) run(app *App, outDir string) int {
	hotkeyHit := make(chan int, 8)
	clipText := make(chan string, 8)
	clipPNG := make(chan []byte, 8)
	lnx.SetHandlers(lnx.Handlers{
		OnClipText: func(t string) { clipText <- t },
		OnClipPNG:  func(b []byte) { clipPNG <- b },
	})
	s.check("gtk-x11", func() (string, error) {
		w, h, sc := lnx.ScreenSize()
		if w <= 0 || h <= 0 {
			return "", fmt.Errorf("屏幕尺寸 %dx%d", w, h)
		}
		return fmt.Sprintf("screen %dx%d scale %.0f", w, h, sc), nil
	})

	s.check("hotkeys-register", func() (string, error) {
		if err := lnx.StartHotkeys(func(id int) { hotkeyHit <- id }); err != nil {
			return "", err
		}
		app.registerHotkeys()
		var names, failed []string
		for _, h := range hotkeys {
			if app.hkFailed[h.ID] {
				failed = append(failed, h.Cands[0].Name)
			} else {
				names = append(names, app.hotkeyName(h.ID))
			}
		}
		if len(failed) > 0 {
			return "", fmt.Errorf("注册失败：%s", strings.Join(failed, ", "))
		}
		return strings.Join(names, ", ") + " registered (XGrabKey)", nil
	})
	s.check("hotkey-xtest", func() (string, error) {
		test := lnx.Combo{Keysym: lnx.KeyF12, Mods: lnx.ModControl | lnx.ModAlt | lnx.ModShift, Name: "Ctrl+Alt+Shift+F12"}
		if err := lnx.GrabHotkey(77, test); err != nil {
			return "", err
		}
		defer lnx.UngrabHotkey(77, test)
		for len(hotkeyHit) > 0 {
			<-hotkeyHit
		}
		if !lnx.SendKeys(lnx.KeyControlL, lnx.KeyAltL, lnx.KeyShiftL, lnx.KeyF12) {
			return "", fmt.Errorf("XTest 扩展不可用")
		}
		select {
		case id := <-hotkeyHit:
			if id != 77 {
				return "", fmt.Errorf("收到意外热键 id=%d", id)
			}
			return "KeyPress for grabbed Ctrl+Alt+Shift+F12 received", nil
		case <-time.After(3 * time.Second):
			return "", fmt.Errorf("3 秒内未收到热键事件")
		}
	})
	app.unregisterHotkeys()

	s.check("clipboard-text", func() (string, error) {
		want := "桌面工具箱 selftest ✓ " + time.Now().Format("150405.000")
		if err := setClipboardText(want); err != nil {
			return "", err
		}
		got, ok := lnx.ClipboardText()
		if !ok || got != want {
			return "", fmt.Errorf("读回 %q，期望 %q", got, want)
		}
		return fmt.Sprintf("round-trip %d chars (CJK + symbol)", len([]rune(want))), nil
	})

	testImg := image.NewRGBA(image.Rect(0, 0, 37, 23))
	for y := 0; y < 23; y++ {
		for x := 0; x < 37; x++ {
			testImg.Set(x, y, color.RGBA{uint8(x * 6), uint8(y * 10), 200, 255})
		}
	}
	var testPNG bytes.Buffer
	_ = png.Encode(&testPNG, testImg)
	samePixels := func(a image.Image) error {
		if a == nil {
			return fmt.Errorf("缺少图片")
		}
		if a.Bounds().Dx() != 37 || a.Bounds().Dy() != 23 {
			return fmt.Errorf("尺寸 %v", a.Bounds())
		}
		for _, p := range []image.Point{{0, 0}, {36, 0}, {0, 22}, {36, 22}, {18, 11}} {
			r1, g1, b1, _ := a.At(a.Bounds().Min.X+p.X, a.Bounds().Min.Y+p.Y).RGBA()
			r2, g2, b2, _ := testImg.At(p.X, p.Y).RGBA()
			if r1>>8 != r2>>8 || g1>>8 != g2>>8 || b1>>8 != b2>>8 {
				return fmt.Errorf("像素 %v 不一致", p)
			}
		}
		return nil
	}
	s.check("clipboard-image", func() (string, error) {
		if err := setClipboardPNG(testPNG.Bytes()); err != nil {
			return "", err
		}
		b, ok := lnx.ClipboardPNG()
		if !ok {
			return "", fmt.Errorf("读不到剪贴板图片")
		}
		img, err := png.Decode(bytes.NewReader(b))
		if err != nil {
			return "", err
		}
		if err := samePixels(img); err != nil {
			return "", err
		}
		return "image/png written and read back pixel-exact (37x23)", nil
	})

	s.check("clipboard-watch", func() (string, error) {
		lnx.WatchClipboard(true)
		defer lnx.WatchClipboard(false)
		drain := func() {
			for len(clipText) > 0 {
				<-clipText
			}
			for len(clipPNG) > 0 {
				<-clipPNG
			}
		}
		drain()
		exe, _ := os.Executable()
		want := "foreign-copy 其他程序 " + time.Now().Format("150405.000")
		c1 := exec.Command(exe, "--selftest-clip-owner=text:"+want)
		if err := c1.Start(); err != nil {
			return "", err
		}
		defer c1.Wait()
		select {
		case got := <-clipText:
			if got != want {
				return "", fmt.Errorf("监听到 %q", got)
			}
		case <-time.After(5 * time.Second):
			return "", fmt.Errorf("5 秒内未收到文本剪贴板通知")
		}
		pngFile := filepath.Join(outDir, "clip-test.png")
		_ = os.WriteFile(pngFile, testPNG.Bytes(), 0o644)
		c2 := exec.Command(exe, "--selftest-clip-owner=png:"+pngFile)
		if err := c2.Start(); err != nil {
			return "", err
		}
		defer c2.Wait()
		var pngData []byte
		select {
		case pngData = <-clipPNG:
		case <-time.After(5 * time.Second):
			return "", fmt.Errorf("5 秒内未收到图片剪贴板通知")
		}
		img, err := png.Decode(bytes.NewReader(pngData))
		if err != nil {
			return "", err
		}
		if err := samePixels(img); err != nil {
			return "", fmt.Errorf("图片：%v", err)
		}
		drain()
		_ = setClipboardText("own write")
		select {
		case t := <-clipText:
			return "", fmt.Errorf("本程序写入的内容被记录：%q", t)
		case <-time.After(800 * time.Millisecond):
		}
		dir, _ := os.MkdirTemp("", "dt-clip")
		defer os.RemoveAll(dir)
		st, err := cliphist.Open(dir, cliphist.DefaultLimit)
		if err != nil {
			return "", err
		}
		st.AddText(want)
		if _, err := st.AddImage(pngData); err != nil {
			return "", err
		}
		if n := len(st.List("")); n != 2 {
			return "", fmt.Errorf("历史条目数 %d", n)
		}
		return "foreign text + image (other X client) captured, own writes ignored, stored in history", nil
	})

	lnx.TestWindow(true)
	time.Sleep(700 * time.Millisecond)
	s.check("screen-capture", func() (string, error) {
		data, w, h, err := lnx.CapturePNG()
		if err != nil {
			return "", err
		}
		out := filepath.Join(outDir, "selftest-capture.png")
		if err := os.WriteFile(out, data, 0o644); err != nil {
			return "", err
		}
		img, err := png.Decode(bytes.NewReader(data))
		if err != nil {
			return "", err
		}
		colors := map[[3]uint32]bool{}
		for y := 0; y < img.Bounds().Dy(); y += 17 {
			for x := 0; x < img.Bounds().Dx(); x += 17 {
				r, g, b, _ := img.At(x, y).RGBA()
				colors[[3]uint32{r >> 8, g >> 8, b >> 8}] = true
			}
			if len(colors) > 50 {
				break
			}
		}
		if len(colors) < 2 {
			return "", fmt.Errorf("截图为纯色，可能没有可见桌面")
		}
		return fmt.Sprintf("X screen %dx%d -> %s (%d KB, %d+ colors)", w, h, out, len(data)/1024, len(colors)), nil
	})

	overlay := func(mode, fw, fh int, act func()) (lnx.OverlayResult, error) {
		done := make(chan lnx.OverlayResult, 1)
		if err := lnx.ShowOverlay(mode, fw, fh, func(r lnx.OverlayResult) { done <- r }); err != nil {
			return lnx.OverlayResult{}, err
		}
		time.Sleep(600 * time.Millisecond) // 等遮罩映射并抓取键盘鼠标
		act()
		select {
		case r := <-done:
			return r, nil
		case <-time.After(5 * time.Second):
			lnx.SendKeys(lnx.KeyEscape)
			return lnx.OverlayResult{}, fmt.Errorf("遮罩 5 秒内没有结束")
		}
	}
	s.check("screenshot-overlay", func() (string, error) {
		lg := func(px int) int { return px } // XTest 使用 X 屏幕物理像素
		r, err := overlay(lnx.ModeScreenshot, 0, 0, func() {
			lnx.PointerTo(lg(60), lg(60), 0, false)
			time.Sleep(80 * time.Millisecond)
			lnx.PointerTo(lg(60), lg(60), 1, true)
			for i := 1; i <= 10; i++ {
				lnx.PointerTo(lg(60+20*i), lg(60+12*i), 0, false)
				time.Sleep(20 * time.Millisecond)
			}
			lnx.PointerTo(lg(260), lg(180), 1, false)
			time.Sleep(200 * time.Millisecond)
			lnx.SendKeys(lnx.KeyReturn)
		})
		if err != nil {
			return "", err
		}
		if r.Action != "copy" {
			return "", fmt.Errorf("动作 %q（期望 copy）", r.Action)
		}
		img, err := png.Decode(bytes.NewReader(r.PNG))
		if err != nil {
			return "", err
		}
		if d := img.Bounds().Dx() - r.Rect.W; d != 0 || img.Bounds().Dy() != r.Rect.H || r.Rect.W < 150 || r.Rect.H < 90 {
			return "", fmt.Errorf("选区 %+v，图片 %v", r.Rect, img.Bounds())
		}
		_ = os.WriteFile(filepath.Join(outDir, "selftest-overlay.png"), r.PNG, 0o644)
		r2, err := overlay(lnx.ModeScreenshot, 0, 0, func() { lnx.SendKeys(lnx.KeyEscape) })
		if err != nil {
			return "", err
		}
		if r2.Action != "cancel" {
			return "", fmt.Errorf("Esc 后动作 %q", r2.Action)
		}
		r3, err := overlay(lnx.ModeRecordFixed, 320, 240, func() {
			lnx.PointerTo(lg(300), lg(300), 0, false)
			time.Sleep(100 * time.Millisecond)
			lnx.PointerTo(lg(300), lg(300), 1, true)
			lnx.PointerTo(lg(300), lg(300), 1, false)
		})
		if err != nil {
			return "", err
		}
		if r3.Action != "record" || r3.Rect.W != 320 || r3.Rect.H != 240 {
			return "", fmt.Errorf("固定尺寸框：%q %+v", r3.Action, r3.Rect)
		}
		return fmt.Sprintf("drag+Enter -> copy %dx%d PNG; Esc -> cancel; fixed 320x240 click -> record at %d,%d", r.Rect.W, r.Rect.H, r3.Rect.X, r3.Rect.Y), nil
	})

	s.check("tray-icon", func() (string, error) {
		lnx.ShowTray(AppName+" selftest", app.icon, []lnx.TrayItem{{ID: trayShow, Label: "显示主窗口"}, {}, {ID: trayQuit, Label: "退出"}})
		time.Sleep(300 * time.Millisecond)
		emb := lnx.TrayEmbedded()
		lnx.RemoveTray()
		if !fileExists(app.icon) {
			return "", fmt.Errorf("托盘图标文件不存在：%s", app.icon)
		}
		return fmt.Sprintf("GtkStatusIcon created (icon %s, embedded in system tray: %v)", app.icon, emb), nil
	})

	s.check("search", func() (string, error) {
		dir, err := os.MkdirTemp("", "dt-search")
		if err != nil {
			return "", err
		}
		defer os.RemoveAll(dir)
		_ = os.MkdirAll(filepath.Join(dir, "子目录", "node_modules"), 0o755)
		_ = os.MkdirAll(filepath.Join(dir, ".隐藏"), 0o755)
		files := map[string]string{
			"报告 Alpha.txt":                 "nothing",
			"子目录/beta 报告.md":               "内容含有 关键字XYZ 的一行",
			"子目录/node_modules/报告 skip.txt": "关键字XYZ",
			".隐藏/报告 hidden.txt":             "关键字XYZ",
			"子目录/other.log":                "line\n关键字xyz here",
			"子目录/utf16.txt":                "\xff\xfe" + utf16le("UTF16 关键字XYZ"),
			"unrelated.bin":                "x",
		}
		for n, c := range files {
			if err := os.WriteFile(filepath.Join(dir, filepath.FromSlash(n)), []byte(c), 0o644); err != nil {
				return "", err
			}
		}
		run := func(kw string, content bool) []search.Result {
			var res []search.Result
			search.Run(context.Background(), search.Options{Roots: []string{dir}, Keyword: kw, Content: content,
				MaxResults: 100, MaxFileSize: 50 << 20, Workers: 4}, func(r search.Result) { res = append(res, r) })
			return res
		}
		byName := run("报告", false)
		if len(byName) != 2 {
			return "", fmt.Errorf("文件名搜索“报告”得到 %d 条（期望 2，node_modules 与隐藏目录应跳过）", len(byName))
		}
		byContent := run("关键字xyz", true)
		if len(byContent) != 3 {
			var names []string
			for _, r := range byContent {
				names = append(names, r.Name)
			}
			return "", fmt.Errorf("内容搜索得到 %d 条 %v（期望 3）", len(byContent), names)
		}
		return fmt.Sprintf("name 2 hits, content 3 hits (UTF-8/UTF-16, node_modules + dot dirs skipped); roots=%v", searchRoots()), nil
	})

	s.check("converter-python", func() (string, error) {
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
		defer cancel()
		if err := app.conv.SelfTest(ctx); err != nil {
			return "", err
		}
		return "convert.py --mode selftest OK via " + app.conv.Python, nil
	})

	s.check("ai-engine-files", func() (string, error) {
		p := filepath.Join(app.resDir, "ai-engine", "dtai_main.py")
		if !fileExists(p) {
			return "", fmt.Errorf("缺少 %s", p)
		}
		return p, nil
	})

	s.check("ffmpeg-x11grab", func() (string, error) {
		if !fileExists(app.ffmpeg) {
			return "", fmt.Errorf("找不到 ffmpeg：%s", app.ffmpeg)
		}
		pulse, note := "", "no audio"
		if srcs, err := recorder.ListPulseSources(app.ffmpeg); err == nil && len(srcs) > 0 {
			pulse = srcs[0].Name
			for _, x := range srcs {
				if x.Default {
					pulse = x.Name
				}
			}
			note = "pulse=" + pulse
		} else if err != nil {
			note = "no PulseAudio: " + strings.SplitN(err.Error(), "\n", 2)[0]
		}
		out := filepath.Join(outDir, "selftest-record.mp4")
		_ = os.Remove(out)
		sess, err := recorder.StartX11(recorder.X11Options{FFmpeg: app.ffmpeg, Rect: recorder.Rect{X: 0, Y: 0, W: 640, H: 480}, FPS: 15, Pulse: pulse, Output: out, DrawMouse: true})
		if err != nil {
			return "", err
		}
		time.Sleep(4 * time.Second)
		if err := sess.Stop(15 * time.Second); err != nil {
			return "", err
		}
		dur, streams, err := probe(app.ffmpeg, out)
		if err != nil {
			return "", err
		}
		if dur < 2 {
			return "", fmt.Errorf("视频时长 %.1fs（期望约 4s）", dur)
		}
		if !strings.Contains(streams, "h264") || (pulse != "" && !strings.Contains(streams, "aac")) {
			return "", fmt.Errorf("流：%s", streams)
		}
		return fmt.Sprintf("%s %.1fs [%s] (%s)", app.ffmpeg, dur, streams, note), nil
	})
	lnx.TestWindow(false)

	if s.failed > 0 {
		s.logf("RESULT: %d check(s) FAILED", s.failed)
		return 1
	}
	s.logf("RESULT: ALL PASSED")
	return 0
}

// probe 用 ffprobe（与 ffmpeg 同目录或 PATH）读取时长与流编码。
func probe(ffmpeg, file string) (float64, string, error) {
	fp := filepath.Join(filepath.Dir(ffmpeg), "ffprobe")
	if !fileExists(fp) {
		if p, err := exec.LookPath("ffprobe"); err == nil {
			fp = p
		} else {
			return 0, "", fmt.Errorf("找不到 ffprobe")
		}
	}
	out, err := exec.Command(fp, "-v", "error", "-print_format", "json", "-show_format", "-show_streams", file).Output()
	if err != nil {
		return 0, "", fmt.Errorf("ffprobe：%v", err)
	}
	var r struct {
		Format  struct{ Duration string }
		Streams []struct {
			CodecName string `json:"codec_name"`
		}
	}
	if err := json.Unmarshal(out, &r); err != nil {
		return 0, "", err
	}
	d, _ := strconv.ParseFloat(r.Format.Duration, 64)
	var names []string
	for _, s := range r.Streams {
		names = append(names, s.CodecName)
	}
	return d, strings.Join(names, "+"), nil
}

func utf16le(s string) string {
	var b []byte
	for _, r := range s {
		if r >= 0x10000 {
			r = '?'
		}
		b = append(b, byte(r), byte(r>>8))
	}
	return string(b)
}

// bestEffort 写入所有目标，忽略单个目标的错误。
type bestEffort []io.Writer

func (b bestEffort) Write(p []byte) (int, error) {
	for _, w := range b {
		_, _ = w.Write(p)
	}
	return len(p), nil
}
