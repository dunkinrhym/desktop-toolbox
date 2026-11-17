//go:build windows || darwin || linux

package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/wailsapp/wails/v2/pkg/runtime"
)

// 本文件为 Windows、macOS 与 Linux 共用的绑定类型与工具函数；平台相关部分见 *_windows.go / *_darwin.go / *_linux.go。

// Settings 持久化的用户设置。
type Settings struct {
	ClipEnabled bool `json:"clipEnabled"`
}

func (a *App) settingsPath() string { return filepath.Join(a.dataDir, "settings.json") }

func (a *App) loadSettings() {
	b, err := os.ReadFile(a.settingsPath())
	if err != nil {
		return
	}
	_ = json.Unmarshal(b, &a.settings)
}

func (a *App) saveSettings() {
	b, _ := json.MarshalIndent(a.settings, "", "  ")
	_ = os.WriteFile(a.settingsPath(), b, 0o644)
}

func (a *App) isPopup() bool {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.popup
}

// Quit 退出程序。
func (a *App) Quit() {
	runtime.Quit(a.ctx)
}

// HotkeyInfo 热键状态。
type HotkeyInfo struct {
	Name   string `json:"name"`
	Keys   string `json:"keys"`
	OK     bool   `json:"ok"`
	Action string `json:"action"`
}

// AppInfo 应用信息。
type AppInfo struct {
	Name       string       `json:"name"`
	Version    string       `json:"version"`
	Hotkeys    []HotkeyInfo `json:"hotkeys"`
	DataDir    string       `json:"dataDir"`
	Desktop    string       `json:"desktop"`
	Autostart  bool         `json:"autostart"`
	HasPython  bool         `json:"hasPython"`
	HasFFmpeg  bool         `json:"hasFfmpeg"`
	ClipOn     bool         `json:"clipOn"`
	InitErrors []string     `json:"initErrors"`
	Platform   string       `json:"platform"` // windows | darwin | linux
	ScreenOK   bool         `json:"screenOk"` // macOS：是否已获得“屏幕录制”权限
}

// RecordRequest 录屏请求。
type RecordRequest struct {
	Mode       string `json:"mode"` // full | region | preset
	Width      int    `json:"width"`
	Height     int    `json:"height"`
	Microphone string `json:"microphone"`
	Cursor     bool   `json:"cursor"`
	FPS        int    `json:"fps"`
}

// RecordEvent 录屏状态事件。
type RecordEvent struct {
	Recording bool   `json:"recording"`
	Started   int64  `json:"started"`
	Output    string `json:"output"`
	Error     string `json:"error"`
	Width     int    `json:"width"`
	Height    int    `json:"height"`
}

// ShotEvent 截图结果事件。
type ShotEvent struct {
	Action  string `json:"action"`
	Path    string `json:"path"`
	Width   int    `json:"width"`
	Height  int    `json:"height"`
	Error   string `json:"error"`
	CanSave bool   `json:"canSave,omitempty"` // macOS：复制后可“另存为”
}

func errString(err error) string {
	if err == nil {
		return ""
	}
	return err.Error()
}

func timestampName(prefix, ext string) string {
	return fmt.Sprintf("%s_%s%s", prefix, time.Now().Format("20060102_150405"), ext)
}
