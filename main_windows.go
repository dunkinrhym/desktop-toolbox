//go:build windows

package main

import (
	"embed"
	"os"

	"github.com/wailsapp/wails/v2"
	"github.com/wailsapp/wails/v2/pkg/options"
	"github.com/wailsapp/wails/v2/pkg/options/assetserver"
	"github.com/wailsapp/wails/v2/pkg/options/windows"

	"desktoptoolbox/internal/win"
)

//go:embed all:frontend/dist
var assets embed.FS

const (
	AppName      = "桌面工具箱"
	AppVersion   = "0.2.0"
	MainWinClass = "DesktopToolboxMain"
	UniqueID     = "com.desktoptoolbox.app.7f3c2a"
)

func main() {
	win.InitDPI()
	if on, out := selfTestRequested(); on {
		os.Exit(runSelfTest(out))
	}
	background := false
	for _, a := range os.Args[1:] {
		if a == "--background" || a == "--hidden" {
			background = true
		}
	}
	app := NewApp()
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
		OnStartup:         app.startup,
		OnDomReady:        app.domReady,
		OnShutdown:        app.shutdown,
		Bind:              []interface{}{app},
		DragAndDrop:       &options.DragAndDrop{EnableFileDrop: true, DisableWebViewDrop: false},
		SingleInstanceLock: &options.SingleInstanceLock{
			UniqueId:               UniqueID,
			OnSecondInstanceLaunch: app.onSecondInstance,
		},
		Windows: &windows.Options{
			Theme:                windows.Light,
			WindowClassName:      MainWinClass,
			WebviewIsTransparent: false,
			WindowIsTranslucent:  false,
			DisableWindowIcon:    false,
			Messages: &windows.Messages{
				InstallationRequired: "本程序需要 Microsoft Edge WebView2 运行时。现在下载并安装吗？",
				UpdateRequired:       "本程序需要更新版本的 Microsoft Edge WebView2 运行时。现在下载并安装吗？",
				MissingRequirements:  "缺少运行组件",
				Webview2NotInstalled: "未安装 WebView2 运行时",
				Error:                "错误",
				FailedToInstall:      "WebView2 运行时安装失败",
				DownloadPage:         "即将打开下载页面：",
				PressOKToInstall:     "点击“确定”开始安装。",
				ContactAdmin:         "需要管理员权限安装 WebView2 运行时，请联系系统管理员。",
				InvalidFixedWebview2: "指定的 WebView2 运行时无效。",
				WebView2ProcessCrash: "WebView2 进程崩溃，程序需要重新启动。",
			},
		},
	})
	if err != nil {
		win.MessageBox(AppName, "程序启动失败：\n"+err.Error(), false)
	}
}
