//go:build !windows

// Package proc 创建无控制台窗口的子进程。
package proc

import "os/exec"

// Hide 在非 Windows 平台上无操作。
func Hide(cmd *exec.Cmd) {}
