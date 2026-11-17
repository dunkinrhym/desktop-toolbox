//go:build windows || darwin || linux

package main

import (
	"errors"

	"github.com/wailsapp/wails/v2/pkg/runtime"

	"desktoptoolbox/internal/cliphist"
)

func (a *App) onClipText(text string) {
	if a.clips == nil || !a.clipEnabled() {
		return
	}
	if a.clips.AddText(text) {
		runtime.EventsEmit(a.ctx, "clip:changed")
	}
}

func (a *App) onClipPNG(png []byte) {
	if a.clips == nil || !a.clipEnabled() {
		return
	}
	if ok, _ := a.clips.AddImage(png); ok {
		runtime.EventsEmit(a.ctx, "clip:changed")
	}
}

func (a *App) clipEnabled() bool {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.settings.ClipEnabled
}

var errNoClips = errors.New("剪贴板历史不可用")

// ClipList 列出历史。
func (a *App) ClipList(query string) []cliphist.View {
	if a.clips == nil {
		return []cliphist.View{}
	}
	l := a.clips.List(query)
	if l == nil {
		return []cliphist.View{}
	}
	return l
}

// ClipGetText 取出完整文本（用于展开查看）。
func (a *App) ClipGetText(id string) (string, error) {
	if a.clips == nil {
		return "", errNoClips
	}
	it, _, err := a.clips.Get(id)
	if err != nil {
		return "", err
	}
	return it.Text, nil
}

// ClipCopy 把一条记录复制回剪贴板。
func (a *App) ClipCopy(id string) error {
	if a.clips == nil {
		return errNoClips
	}
	it, png, err := a.clips.Get(id)
	if err != nil {
		return err
	}
	if it.Kind == "image" {
		if err := setClipboardPNG(png); err != nil {
			return errors.New("复制图片失败：" + err.Error())
		}
	} else if err := setClipboardText(it.Text); err != nil {
		return errors.New("复制失败：" + err.Error())
	}
	a.clips.Touch(id)
	runtime.EventsEmit(a.ctx, "clip:changed")
	return nil
}

// ClipCopyMany 把多条文本按选择顺序以换行连接后复制。返回实际合并的条数。
func (a *App) ClipCopyMany(ids []string) (int, error) {
	if a.clips == nil {
		return 0, errNoClips
	}
	text, n := a.clips.JoinTexts(ids)
	if n == 0 {
		return 0, errors.New("所选记录中没有文本（图片无法合并复制）")
	}
	if err := setClipboardText(text); err != nil {
		return 0, errors.New("复制失败：" + err.Error())
	}
	// 合并后的文本也记入历史，方便再次使用
	if n > 1 {
		a.clips.AddText(text)
		runtime.EventsEmit(a.ctx, "clip:changed")
	}
	return n, nil
}

// ClipPin 置顶/取消置顶。
func (a *App) ClipPin(id string, pinned bool) {
	if a.clips != nil {
		a.clips.SetPinned(id, pinned)
	}
}

// ClipDelete 删除一条。
func (a *App) ClipDelete(id string) {
	if a.clips != nil {
		a.clips.Delete(id)
	}
}

// ClipClear 清空历史（可保留置顶）。
func (a *App) ClipClear(keepPinned bool) {
	if a.clips != nil {
		a.clips.Clear(keepPinned)
	}
}

// ClipSetEnabled 开启/暂停剪贴板记录。
func (a *App) ClipSetEnabled(on bool) error {
	a.mu.Lock()
	a.settings.ClipEnabled = on
	a.saveSettings()
	a.mu.Unlock()
	return a.watchClipboard(on)
}
