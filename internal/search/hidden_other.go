//go:build !windows && !darwin && !linux

package search

import "io/fs"

func isHiddenOrSystem(info fs.FileInfo) bool { return false }
