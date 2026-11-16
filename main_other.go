//go:build !windows && !darwin

package main

import "fmt"

// 本程序只支持 Windows 和 macOS。此文件让 `go build ./...` 在其他平台上也能通过（便于运行单元测试）。
func main() {
	fmt.Println("桌面工具箱 仅支持 Windows 和 macOS")
}
