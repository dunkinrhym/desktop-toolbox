//go:build darwin

package main

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/wailsapp/wails/v2/pkg/runtime"

	"desktoptoolbox/internal/mac"
	"desktoptoolbox/internal/search"
)

// 共用绑定（app_clip.go / app_search.go）所需的 macOS 平台实现。

// searchRoots “整台电脑”：用户主目录、应用程序、/Volumes 下挂载的其他磁盘（U 盘、移动硬盘、网络盘）。
// 系统目录（/System、/Library、/usr、/private…）和 ~/Library（隐藏标记）不搜索。
func searchRoots() []string {
	var roots []string
	if home, err := os.UserHomeDir(); err == nil {
		roots = append(roots, home)
	}
	roots = append(roots, "/Applications", "/Users/Shared")
	if ents, err := os.ReadDir("/Volumes"); err == nil {
		for _, e := range ents {
			name := e.Name()
			if strings.HasPrefix(name, ".") || strings.HasPrefix(name, "com.apple.TimeMachine") {
				continue
			}
			p := filepath.Join("/Volumes", name)
			// “Macintosh HD” 是指向 / 的符号链接，跳过；只保留真实挂载的目录
			if fi, err := os.Lstat(p); err != nil || fi.Mode()&os.ModeSymlink != 0 || !fi.IsDir() {
				continue
			}
			roots = append(roots, p)
		}
	}
	var out []string
	for _, r := range roots {
		if st, err := os.Stat(r); err == nil && st.IsDir() {
			out = append(out, r)
		}
	}
	return out
}

// fastNameSearch “整台电脑 + 仅文件名”时使用 Spotlight（mdfind）索引，秒出结果；
// Spotlight 不可用（被关闭/命令失败）时返回 ok=false，调用方回退到实时遍历。
func fastNameSearch(ctx context.Context, wholeDisk bool, opt search.Options, emit func(search.Result)) (search.Stats, bool) {
	if !wholeDisk || opt.Content {
		return search.Stats{}, false
	}
	home, _ := os.UserHomeDir()
	st, err := search.RunMdfind(ctx, "/usr/bin/mdfind", home, opt, emit)
	if errors.Is(err, search.ErrNoIndex) {
		return search.Stats{}, false
	}
	return st, true
}

func setClipboardText(text string) error { return mac.SetClipboardText(text) }

func setClipboardPNG(png []byte) error { return mac.SetClipboardPNG(png) }

// watchClipboard 轮询 NSPasteboard.changeCount（macOS 没有剪贴板变化通知）。
func (a *App) watchClipboard(on bool) error {
	a.mu.Lock()
	defer a.mu.Unlock()
	if !on {
		if a.clipStop != nil {
			close(a.clipStop)
			a.clipStop = nil
		}
		return nil
	}
	if a.clipStop != nil {
		return nil
	}
	if a.clips == nil {
		return errors.New("剪贴板历史不可用")
	}
	stop := make(chan struct{})
	a.clipStop = stop
	go a.clipLoop(stop)
	return nil
}

func (a *App) clipLoop(stop chan struct{}) {
	last := mac.ClipboardChangeCount()
	t := time.NewTicker(500 * time.Millisecond)
	defer t.Stop()
	for {
		select {
		case <-stop:
			return
		case <-t.C:
		}
		n := mac.ClipboardChangeCount()
		if n == last {
			continue
		}
		last = n
		kind, text, png := mac.ReadClipboard()
		switch kind {
		case mac.ClipText:
			a.onClipText(text)
		case mac.ClipImage:
			a.onClipPNG(png)
		}
	}
}

// emitClipChanged 供截图等调用。
func (a *App) emitClipChanged() { runtime.EventsEmit(a.ctx, "clip:changed") }
