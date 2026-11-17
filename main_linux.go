//go:build linux

package main

import (
	"embed"
	"encoding/json"
	"fmt"
	"os"
	"strings"

	"github.com/godbus/dbus/v5"
	"github.com/wailsapp/wails/v2"
	"github.com/wailsapp/wails/v2/pkg/options"
	"github.com/wailsapp/wails/v2/pkg/options/assetserver"
	"github.com/wailsapp/wails/v2/pkg/options/linux"
)

//go:embed all:frontend/dist
var assets embed.FS

//go:embed build/appicon.png
var appIcon []byte

const (
	AppName    = "桌面工具箱"
	AppVersion = "0.2.0"
	PkgName    = "desktop-toolbox" // deb 包名 / WM_CLASS / 图标名
	UniqueID   = "com.desktoptoolbox.app.7f3c2a"
)

func main() {
	if on, out := selfTestRequested(); on {
		os.Exit(runSelfTest(out))
	}
	if v, ok := clipOwnerRequested(); ok {
		os.Exit(runClipOwner(v))
	}
	background := false
	for _, a := range os.Args[1:] {
		if a == "--background" || a == "--hidden" {
			background = true
		}
	}
	if os.Getenv("DISPLAY") == "" {
		if os.Getenv("WAYLAND_DISPLAY") != "" {
			fmt.Fprintln(os.Stderr, "桌面工具箱需要 X11 会话（检测到 Wayland 且没有 XWayland 的 DISPLAY）")
		} else {
			fmt.Fprintln(os.Stderr, "桌面工具箱需要图形界面（DISPLAY 为空）")
		}
		os.Exit(2)
	}
	// 已有实例在运行：把参数（如 --show=<页面>、--quit）交给它，本进程以 0 退出。
	// Wails 的 Linux 单实例实现会在 OnStartup 已开始执行后才检查，所以这里提前处理。
	if forwardToRunningInstance() {
		os.Exit(0)
	}
	// 统信 / 深度 DDE 运行在 X11；若在 Wayland 会话中启动，强制 GTK 走 XWayland，保证截图、热键可用。
	if os.Getenv("GDK_BACKEND") == "" {
		os.Setenv("GDK_BACKEND", "x11")
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
		Linux: &linux.Options{
			Icon:             appIcon,
			ProgramName:      PkgName,
			WebviewGpuPolicy: gpuPolicy(),
		},
	})
	if err != nil {
		fmt.Fprintln(os.Stderr, "程序启动失败："+err.Error())
		os.Exit(1)
	}
}

// gpuPolicy 默认关闭 WebView 硬件加速（国产显卡 / 虚拟机上的 WebKitGTK 加速经常白屏）；
// 设置环境变量 DT_GPU=1 可开启。
func gpuPolicy() linux.WebviewGpuPolicy {
	if os.Getenv("DT_GPU") == "1" {
		return linux.WebviewGpuPolicyOnDemand
	}
	return linux.WebviewGpuPolicyNever
}

// forwardToRunningInstance 与 Wails 使用相同的 D-Bus 名称与接口：已有实例时发送参数并返回 true。
func forwardToRunningInstance() bool {
	id := "wails_app_" + strings.ReplaceAll(strings.ReplaceAll(UniqueID, "-", "_"), ".", "_")
	name := "org." + id + ".SingleInstance"
	path := dbus.ObjectPath("/org/" + id + "/SingleInstance")
	conn, err := dbus.ConnectSessionBus()
	if err != nil {
		return false
	}
	defer conn.Close()
	var has bool
	if err := conn.BusObject().Call("org.freedesktop.DBus.NameHasOwner", 0, name).Store(&has); err != nil || !has {
		return false
	}
	data := options.SecondInstanceData{Args: os.Args[1:]}
	data.WorkingDirectory, _ = os.Getwd()
	b, _ := json.Marshal(data)
	if err := conn.Object(name, path).Call(name+".SendMessage", 0, string(b)).Store(); err != nil {
		return false
	}
	return true
}
