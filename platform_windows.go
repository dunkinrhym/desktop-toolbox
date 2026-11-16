//go:build windows

package main

import (
	"context"

	"desktoptoolbox/internal/search"
	"desktoptoolbox/internal/win"
)

// 共用绑定（app_clip.go / app_search.go）所需的 Windows 平台实现。

// searchRoots “所有磁盘”：固定盘 + 可移动盘。
func searchRoots() []string { return win.Drives() }

// fastNameSearch Windows 没有快速索引通道，始终使用实时遍历。
func fastNameSearch(ctx context.Context, wholeDisk bool, opt search.Options, emit func(search.Result)) (search.Stats, bool) {
	return search.Stats{}, false
}

func setClipboardText(text string) error { return win.SetClipboardText(text) }

func setClipboardPNG(png []byte) error { return win.SetClipboardImage(nil, png) }

func (a *App) watchClipboard(on bool) error { return win.WatchClipboard(on) }
