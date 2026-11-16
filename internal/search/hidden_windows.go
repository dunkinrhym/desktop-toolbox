//go:build windows

package search

import (
	"io/fs"
	"syscall"
)

func isHiddenOrSystem(info fs.FileInfo) bool {
	if d, ok := info.Sys().(*syscall.Win32FileAttributeData); ok {
		const hidden, system, reparse = 0x2, 0x4, 0x400
		return d.FileAttributes&(hidden|system) != 0 || d.FileAttributes&reparse != 0
	}
	return false
}
