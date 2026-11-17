//go:build linux

package main

import (
	"context"
	"os"
	"os/user"
	"path/filepath"
	"strings"

	"github.com/wailsapp/wails/v2/pkg/runtime"

	"desktoptoolbox/internal/lnx"
	"desktoptoolbox/internal/search"
)

// 共用绑定（app_clip.go / app_search.go）所需的 Linux 平台实现。

// searchRoots “整台电脑”：用户主目录、/data（统信 UOS 的数据盘）以及挂载的 U 盘 / 移动硬盘 / 其他分区
// （/media/<用户>/*、/run/media/<用户>/*、/mnt/*）。系统目录（/usr、/etc、/proc…）不搜索。
func searchRoots() []string {
	var roots []string
	home, _ := os.UserHomeDir()
	if home != "" {
		roots = append(roots, home)
	}
	if st, err := os.Stat("/data"); err == nil && st.IsDir() && !strings.HasPrefix(home, "/data/") {
		roots = append(roots, "/data")
	}
	uname := ""
	if u, err := user.Current(); err == nil {
		uname = u.Username
	}
	var parents []string
	if uname != "" {
		parents = append(parents, filepath.Join("/media", uname), filepath.Join("/run/media", uname))
	}
	parents = append(parents, "/mnt")
	for _, p := range parents {
		ents, err := os.ReadDir(p)
		if err != nil {
			continue
		}
		for _, e := range ents {
			if strings.HasPrefix(e.Name(), ".") {
				continue
			}
			roots = append(roots, filepath.Join(p, e.Name()))
		}
	}
	var out []string
	seen := map[string]bool{}
	for _, r := range roots {
		real, err := filepath.EvalSymlinks(r)
		if err != nil {
			continue
		}
		if st, err := os.Stat(real); err != nil || !st.IsDir() || seen[real] {
			continue
		}
		// 已被其他根目录包含的跳过（如 /data/home 与主目录）
		dup := false
		for s := range seen {
			if strings.HasPrefix(real+"/", s+"/") || strings.HasPrefix(s+"/", real+"/") {
				dup = true
				break
			}
		}
		if dup {
			continue
		}
		seen[real] = true
		out = append(out, r)
	}
	return out
}

// fastNameSearch Linux 上始终实时遍历（locate 数据库通常未安装或已过期，结果不可靠）。
func fastNameSearch(ctx context.Context, wholeDisk bool, opt search.Options, emit func(search.Result)) (search.Stats, bool) {
	return search.Stats{}, false
}

func setClipboardText(text string) error { return lnx.SetClipboardText(text) }

func setClipboardPNG(png []byte) error { return lnx.SetClipboardPNG(png) }

// watchClipboard 监听剪贴板变化（GTK owner-change / XFixes）。
func (a *App) watchClipboard(on bool) error {
	lnx.WatchClipboard(on)
	return nil
}

// emitClipChanged 供截图等调用。
func (a *App) emitClipChanged() { runtime.EventsEmit(a.ctx, "clip:changed") }
