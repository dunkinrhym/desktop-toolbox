//go:build windows

// Package proc 创建无控制台窗口的子进程。
package proc

import (
	"os/exec"
	"syscall"
)

const createNoWindow = 0x08000000

// Hide 让子进程不弹出控制台窗口。
func Hide(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{HideWindow: true, CreationFlags: createNoWindow}
}
