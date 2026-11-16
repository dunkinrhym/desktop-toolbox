//go:build darwin && !cgo

package mac

// 无 cgo 时的占位实现：只用于在非 macOS 机器上执行 `GOOS=darwin CGO_ENABLED=0 go vet` 做类型检查。
// 真正的 macOS 构建必须开启 cgo（CI 中 wails build 会自动开启）。

import "errors"

var errNoCgo = errors.New("macOS 原生组件需要 cgo 构建")

func Init(h Handlers)                                         {}
func ActivateApp()                                            {}
func HideApp()                                                {}
func IsAppActive() bool                                       { return false }
func IsMainWindowVisible() bool                               { return false }
func Alert(title, msg string)                                 {}
func RegisterHotkey(id int, keyCode, mods uint32) error       { return errNoCgo }
func UnregisterHotkeys()                                      {}
func ShowTray(tooltip string, items []TrayItem) error         { return errNoCgo }
func SetTrayTitle(t string)                                   {}
func RemoveTray()                                             {}
func ClipboardChangeCount() int64                             { return 0 }
func ReadClipboard() (kind ClipKind, text string, png []byte) { return ClipNone, "", nil }
func SetClipboardText(s string) error                         { return errNoCgo }
func SetClipboardPNG(png []byte) error                        { return errNoCgo }
func ScreenCaptureAllowed() bool                              { return false }
func RequestScreenCapture() bool                              { return false }
func SelectRegion(fixedW, fixedH int, done func(Region))      { go done(Region{}) }
func ScreenUnderMouse() Region                                { return Region{} }
func ShowRecordingBar(startedUnix float64, onStop func())     {}
func HideRecordingBar()                                       {}
