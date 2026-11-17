//go:build linux

package search

import (
	"io/fs"
	"strings"
)

// isHiddenOrSystem Linux 约定：以“.”开头的文件 / 目录为隐藏。
func isHiddenOrSystem(info fs.FileInfo) bool { return strings.HasPrefix(info.Name(), ".") }
