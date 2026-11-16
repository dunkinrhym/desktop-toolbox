//go:build windows

package main

import (
	"bytes"
	"context"
	"fmt"
	"image"
	"image/color"
	"image/png"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"desktoptoolbox/internal/cliphist"
	"desktoptoolbox/internal/search"
	"desktoptoolbox/internal/win"
)

// 隐藏的自检模式：DesktopToolbox.exe --selftest [--selftest-out=<目录>]
// 不启动界面，依次检查：原生 UI 线程、全局热键注册（并用 SendInput 触发一次）、剪贴板读写（文本 + 图片）
// 与监听、截屏为 PNG、文件名/内容搜索、内置 Python 转换组件、FFmpeg 是否存在。
// 结果打印到父控制台（若有）并写入 <目录>\selftest.log；截图保存为 <目录>\selftest-capture.png。
// 全部通过返回退出码 0，否则 1。

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
	attached := win.AttachParentConsole()
	if outDir == "" {
		outDir = filepath.Join(os.TempDir(), "DesktopToolbox-selftest")
	}
	_ = os.MkdirAll(outDir, 0o755)
	var sinks []io.Writer
	if attached {
		sinks = append(sinks, os.Stdout)
	}
	if f, err := os.Create(filepath.Join(outDir, "selftest.log")); err == nil {
		defer f.Close()
		sinks = append(sinks, f)
	}
	s := &selfTest{w: io.MultiWriter(sinks...)}
	app := NewApp()
	s.logf("桌面工具箱 %s 自检  exe=%s", AppVersion, app.exeDir)

	hotkeyHit := make(chan int, 8)
	clipText := make(chan string, 8)
	clipPNG := make(chan []byte, 8)

	s.check("native-ui-thread", func() (string, error) {
		err := win.Start(win.Handlers{
			OnHotkey:        func(id int) { hotkeyHit <- id },
			OnClipboardText: func(t string) { clipText <- t },
			OnClipboardPNG:  func(b []byte) { clipPNG <- b },
		}, AppName+" selftest")
		return "host window created", err
	})

	s.check("hotkeys-register", func() (string, error) {
		failed := win.RegisterHotkeys(hotkeys)
		if len(failed) > 0 {
			return "", fmt.Errorf("注册失败：%s", strings.Join(failed, ", "))
		}
		return "Alt+Space, Ctrl+Alt+A, Ctrl+Shift+V registered", nil
	})
	s.check("hotkey-sendinput", func() (string, error) {
		// 用一个不常用的组合键测试 WM_HOTKEY 投递，避免触发系统菜单等副作用
		test := []win.Hotkey{{ID: 77, Mods: win.MOD_CONTROL | win.MOD_ALT | win.MOD_SHIFT, VK: 0x7B, Name: "Ctrl+Alt+Shift+F12"}}
		if f := win.RegisterHotkeys(test); len(f) > 0 {
			return "", fmt.Errorf("注册失败：%v", f)
		}
		defer win.UnregisterHotkeys(test)
		n := win.SendKeyCombo(win.VK_CONTROL, win.VK_MENU, 0x10 /*VK_SHIFT*/, 0x7B /*VK_F12*/)
		select {
		case id := <-hotkeyHit:
			if id != 77 {
				return "", fmt.Errorf("收到意外热键 id=%d", id)
			}
			return fmt.Sprintf("WM_HOTKEY id=77 received (SendInput injected %d events)", n), nil
		case <-time.After(3 * time.Second):
			return "", fmt.Errorf("3 秒内未收到 WM_HOTKEY（SendInput 注入 %d 个事件；无交互桌面时会失败）", n)
		}
	})
	win.UnregisterHotkeys(hotkeys)

	s.check("clipboard-text", func() (string, error) {
		want := "桌面工具箱 selftest ✓ " + time.Now().Format("150405.000")
		if err := setClipboardText(want); err != nil {
			return "", err
		}
		got, err := win.ReadClipboardText()
		if err != nil {
			return "", err
		}
		if got != want {
			return "", fmt.Errorf("读回 %q，期望 %q", got, want)
		}
		return fmt.Sprintf("round-trip %d chars (CJK + emoji)", len([]rune(want))), nil
	})

	testImg := image.NewRGBA(image.Rect(0, 0, 37, 23)) // 奇数宽度，验证 DIB 行对齐
	for y := 0; y < 23; y++ {
		for x := 0; x < 37; x++ {
			testImg.Set(x, y, color.RGBA{uint8(x * 6), uint8(y * 10), 200, 255})
		}
	}
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
		var buf bytes.Buffer
		_ = png.Encode(&buf, testImg)
		if err := setClipboardPNG(buf.Bytes()); err != nil {
			return "", err
		}
		p, d, err := win.ClipboardImageFormats()
		if err != nil {
			return "", err
		}
		if err := samePixels(p); err != nil {
			return "", fmt.Errorf("PNG 格式：%v", err)
		}
		if err := samePixels(d); err != nil {
			return "", fmt.Errorf("CF_DIB 格式：%v", err)
		}
		return "PNG + CF_DIB written and read back pixel-exact (37x23)", nil
	})

	s.check("clipboard-watch", func() (string, error) {
		if err := win.WatchClipboard(true); err != nil {
			return "", err
		}
		defer win.WatchClipboard(false)
		drain := func() {
			for len(clipText) > 0 {
				<-clipText
			}
			for len(clipPNG) > 0 {
				<-clipPNG
			}
		}
		drain()
		want := "foreign-copy " + time.Now().Format("150405.000")
		if err := win.WriteForeignText(want); err != nil {
			return "", err
		}
		select {
		case got := <-clipText:
			if got != want {
				return "", fmt.Errorf("监听到 %q", got)
			}
		case <-time.After(3 * time.Second):
			return "", fmt.Errorf("3 秒内未收到文本剪贴板通知")
		}
		if err := win.WriteForeignDIB(testImg); err != nil {
			return "", err
		}
		var pngData []byte
		select {
		case pngData = <-clipPNG:
		case <-time.After(3 * time.Second):
			return "", fmt.Errorf("3 秒内未收到图片剪贴板通知")
		}
		img, err := png.Decode(bytes.NewReader(pngData))
		if err != nil {
			return "", err
		}
		if err := samePixels(img); err != nil {
			return "", fmt.Errorf("DIB→PNG：%v", err)
		}
		// 自己写入的内容不应被记录
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
		return "foreign text + DIB-only image captured, own writes ignored, stored in history", nil
	})

	s.check("screen-capture", func() (string, error) {
		vs := win.VirtualScreen()
		img, err := win.CaptureScreen(vs)
		if err != nil {
			return "", err
		}
		var buf bytes.Buffer
		if err := png.Encode(&buf, img); err != nil {
			return "", err
		}
		out := filepath.Join(outDir, "selftest-capture.png")
		if err := os.WriteFile(out, buf.Bytes(), 0o644); err != nil {
			return "", err
		}
		colors := map[[3]uint8]bool{}
		for y := 0; y < img.Bounds().Dy(); y += 17 {
			for x := 0; x < img.Bounds().Dx(); x += 17 {
				i := img.PixOffset(x, y)
				colors[[3]uint8{img.Pix[i], img.Pix[i+1], img.Pix[i+2]}] = true
				if len(colors) > 50 {
					break
				}
			}
		}
		if len(colors) < 2 {
			return "", fmt.Errorf("截图为纯色（%v），可能没有可见桌面", vs)
		}
		return fmt.Sprintf("virtual screen %v -> %s (%d KB, %d+ colors)", vs, out, buf.Len()/1024, len(colors)), nil
	})

	s.check("search", func() (string, error) {
		dir, err := os.MkdirTemp("", "dt-search")
		if err != nil {
			return "", err
		}
		defer os.RemoveAll(dir)
		_ = os.MkdirAll(filepath.Join(dir, "子目录", "node_modules"), 0o755)
		files := map[string]string{
			"报告 Alpha.txt":                 "nothing",
			"子目录/beta 报告.md":               "内容含有 关键字XYZ 的一行",
			"子目录/node_modules/报告 skip.txt": "关键字XYZ",
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
			return "", fmt.Errorf("文件名搜索“报告”得到 %d 条（期望 2，node_modules 应跳过）", len(byName))
		}
		byContent := run("关键字xyz", true)
		if len(byContent) != 3 {
			var names []string
			for _, r := range byContent {
				names = append(names, r.Name)
			}
			return "", fmt.Errorf("内容搜索得到 %d 条 %v（期望 3）", len(byContent), names)
		}
		return "name search 2 hits, content search 3 hits (UTF-8/UTF-16, node_modules skipped)", nil
	})

	s.check("converter-python", func() (string, error) {
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
		defer cancel()
		if err := app.conv.SelfTest(ctx); err != nil {
			return "", err
		}
		return "convert.py --mode selftest OK via " + app.conv.Python, nil
	})

	s.check("ffmpeg-present", func() (string, error) {
		st, err := os.Stat(app.ffmpeg)
		if err != nil {
			return "", err
		}
		return fmt.Sprintf("%s (%d MB)", app.ffmpeg, st.Size()>>20), nil
	})

	if s.failed > 0 {
		s.logf("RESULT: %d check(s) FAILED", s.failed)
		return 1
	}
	s.logf("RESULT: ALL PASSED")
	return 0
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
