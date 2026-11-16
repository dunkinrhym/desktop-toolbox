//go:build !windows

package search

import "io/fs"

func isHiddenOrSystem(info fs.FileInfo) bool { return false }
