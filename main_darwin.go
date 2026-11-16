//go:build darwin

package main

import (
	"embed"
	"fmt"
	"os"
	"os/exec"
	"strings"

	"github.com/wailsapp/wails/v2"
	"github.com/wailsapp/wails/v2/pkg/menu"
	"github.com/wailsapp/wails/v2/pkg/options"
	"github.com/wailsapp/wails/v2/pkg/options/assetserver"
	"github.com/wailsapp/wails/v2/pkg/options/mac"
)

//go:embed all:frontend/dist
var assets embed.FS

const (
	AppName    = "桌面工具箱"
	AppVersion = "0.1.0"
	BundleID   = "com.hark.desktoptoolbox"
	UniqueID   = "com.hark.desktoptoolbox.single"
)

func main() {
	background := false
	for _, a := range os.Args[1:] {
		if a == "--background" || a == "--hidden" {
			background = true
		}
	}
	app := NewApp()

	// macOS 必须有“编辑”菜单，输入框里的 ⌘C / ⌘V / ⌘A 才能用。
	appMenu := menu.NewMenu()
	appMenu.Append(menu.AppMenu())
	appMenu.Append(menu.EditMenu())
	appMenu.Append(menu.WindowMenu())

	err := wails.Run(&options.App{
		Title:             AppName,
		Width:             1120,
		Height:            740,
		MinWidth:          860,
		MinHeight:         560,
		StartHidden:       background,
		HideWindowOnClose: true,
		BackgroundColour:  &options.RGBA{R: 245, G: 245, B: 247, A: 255},
		AssetServer:       &assetserver.Options{Assets: assets},
		Menu:              appMenu,
		OnStartup:         app.startup,
		OnDomReady:        app.domReady,
		OnShutdown:        app.shutdown,
		Bind:              []interface{}{app},
		DragAndDrop:       &options.DragAndDrop{EnableFileDrop: true, DisableWebViewDrop: false},
		SingleInstanceLock: &options.SingleInstanceLock{
			UniqueId:               UniqueID,
			OnSecondInstanceLaunch: app.onSecondInstance,
		},
		Mac: &mac.Options{
			TitleBar:             mac.TitleBarDefault(),
			Appearance:           mac.NSAppearanceNameAqua,
			WebviewIsTransparent: false,
			WindowIsTranslucent:  false,
			About: &mac.AboutInfo{
				Title:   AppName + " " + AppVersion,
				Message: "文件搜索 · 截图 · 超级复制 · 格式转换 · 录屏\n完全离线运行",
			},
		},
	})
	if err != nil {
		startupFailed(err)
	}
}

// startupFailed Wails 启动失败时（此时没有 AppKit 事件循环）用 osascript 弹出提示。
func startupFailed(err error) {
	msg := "程序启动失败：\n" + err.Error()
	fmt.Fprintln(os.Stderr, msg)
	esc := strings.NewReplacer(`\`, `\\`, `"`, `\"`).Replace(msg)
	_ = exec.Command("/usr/bin/osascript", "-e", `display alert "`+AppName+`" message "`+esc+`" as critical`).Run()
}
