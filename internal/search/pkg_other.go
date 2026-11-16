//go:build !darwin

package search

// isPackageDir 只有 macOS 有“包”目录的概念。
func isPackageDir(name string) bool { return false }
