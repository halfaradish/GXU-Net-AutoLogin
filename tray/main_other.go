//go:build !windows

// 托盘版只在 Windows 上有意义。这个文件让 "go build ./..." 在 Linux/macOS 上
// 也能通过——命令行版照样是那边的主力。
package main

import (
	"fmt"
	"os"
)

func main() {
	fmt.Fprintln(os.Stderr, "托盘版仅支持 Windows；Linux/macOS 请使用命令行版 GXU_Net_AutoLogin")
	os.Exit(1)
}
