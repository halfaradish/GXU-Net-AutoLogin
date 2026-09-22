//go:build windows

package main

import (
	"errors"
	"fmt"

	"golang.org/x/sys/windows"
	"golang.org/x/sys/windows/registry"
)

const (
	autostartKey  = `Software\Microsoft\Windows\CurrentVersion\Run`
	autostartName = "GXU-Net-AutoLogin"
)

// applyAutostart 把"开机自启动"落到 HKCU\...\Run：
// 打开时写入 exe 绝对路径（每次保存/启动都重写一遍，程序挪了位置也能自愈），
// 关闭时删掉该键值。
func applyAutostart(enabled bool, exePath string) error {
	k, err := registry.OpenKey(registry.CURRENT_USER, autostartKey, registry.SET_VALUE|registry.QUERY_VALUE)
	if err != nil {
		return fmt.Errorf("打开注册表启动项失败：%v", err)
	}
	defer k.Close()

	if !enabled {
		if err := k.DeleteValue(autostartName); err != nil && !errors.Is(err, windows.ERROR_FILE_NOT_FOUND) {
			return fmt.Errorf("删除注册表启动项失败：%v", err)
		}
		return nil
	}

	if err := k.SetStringValue(autostartName, `"`+exePath+`"`); err != nil {
		return fmt.Errorf("写入注册表启动项失败：%v", err)
	}
	return nil
}
