//go:build !windows && !darwin

package search

import "io/fs"

func isHiddenOrSystem(info fs.FileInfo) bool { return false }
