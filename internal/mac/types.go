// Package mac 是桌面工具箱的 macOS 原生层（cgo + Objective-C）：全局热键、菜单栏图标、
// 剪贴板、屏幕录制权限、区域选择遮罩、录屏悬浮条。
//
// 本文件只有平台无关的类型与常量（任何平台都能编译，便于测试）；实现见 mac_darwin.go / native_darwin.m，
// stub_nocgo.go 提供无 cgo 时的占位实现（仅用于在 Linux 上 `GOOS=darwin go vet`）。
package mac

// Handlers 原生事件回调（均在 goroutine 中调用）。
type Handlers struct {
	OnHotkey func(id int)
	OnTray   func(id int)
	OnReopen func() // 点击程序坞图标
}

// TrayItem 菜单栏菜单项；ID 为 0 表示分隔线。Key/KeyMods 仅用于在菜单中显示快捷键。
type TrayItem struct {
	ID      int
	Label   string
	Key     string // 如 " "、"a"、"v"
	KeyMods uint   // NSEventModifierFlag* 组合
}

// Region 区域选择结果（点坐标，原点为所在屏幕左上角）。
type Region struct {
	OK               bool
	X, Y, W, H       float64
	ScreenW, ScreenH float64
	Scale            float64 // backingScaleFactor（Retina 为 2）
	DisplayIndex     int     // CGGetActiveDisplayList 中的序号 = ffmpeg "Capture screen N"
}

// ClipKind 剪贴板内容类型。
type ClipKind int

// 剪贴板内容类型。
const (
	ClipNone   ClipKind = 0
	ClipText   ClipKind = 1
	ClipImage  ClipKind = 2
	ClipIgnore ClipKind = 3 // 密码管理器标记的内容、访达复制的文件等
)

// Carbon 虚拟键码（<HIToolbox/Events.h> kVK_*）。
const (
	KeyA     uint32 = 0x00
	KeyS     uint32 = 0x01
	KeyV     uint32 = 0x09
	KeySpace uint32 = 0x31
)

// Carbon 热键修饰键（cmdKey / shiftKey / optionKey / controlKey）。
const (
	ModCmd     uint32 = 1 << 8
	ModShift   uint32 = 1 << 9
	ModOption  uint32 = 1 << 11
	ModControl uint32 = 1 << 12
)

// 菜单快捷键显示用的 NSEventModifierFlags。
const (
	NSShift   uint = 1 << 17
	NSControl uint = 1 << 18
	NSOption  uint = 1 << 19
	NSCommand uint = 1 << 20
)
