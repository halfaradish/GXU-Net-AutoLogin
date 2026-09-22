//go:build windows

// 界面偏好（不是配置）存这里。
//
// 为什么不写进 .env：.env 是给命令行版共用的配置文件，「恢复默认配置」也不该
// 把窗口的开合状态一起重置；放注册表与已有的开机自启项风格一致。
//
// 值名沿用注册表里已有的 AdvancedShown、按字符串 "0"/"1" 存（同机上另一份
// 实现就是这么写的）。读取时同时也认 DWORD，免得被写成另一种类型就读不出来。
package main

import (
	"errors"
	"strings"

	"golang.org/x/sys/windows"
	"golang.org/x/sys/windows/registry"
)

const (
	uiStateKey        = `Software\GXU-Net-AutoLogin`
	advancedShownName = "AdvancedShown"
)

// loadAdvancedShown 读"高级选项是否展开"。没有记录（首次运行）时返回 false，
// 也就是默认收起；只有真正的读取错误才返回 error。
func loadAdvancedShown() (bool, error) {
	k, err := registry.OpenKey(registry.CURRENT_USER, uiStateKey, registry.QUERY_VALUE)
	if err != nil {
		if errors.Is(err, windows.ERROR_FILE_NOT_FOUND) {
			return false, nil
		}
		return false, err
	}
	defer k.Close()

	// 字符串形式（本程序写的就是这个）。值类型不对时 GetStringValue 会报错，
	// 这里不当作失败，继续往下试 DWORD。
	if s, _, err := k.GetStringValue(advancedShownName); err == nil {
		switch strings.ToLower(strings.TrimSpace(s)) {
		case "1", "true", "yes", "on":
			return true, nil
		default:
			return false, nil
		}
	}

	// 兼容别的实现写成 DWORD 的情况；两种形式都没有就是首次运行 → 收起
	v, _, err := k.GetIntegerValue(advancedShownName)
	if err != nil {
		if errors.Is(err, windows.ERROR_FILE_NOT_FOUND) {
			return false, nil
		}
		return false, err
	}
	return v != 0, nil
}

// saveAdvancedShown 记住"高级选项是否展开"。切换时立刻写，被强杀也不丢。
func saveAdvancedShown(shown bool) error {
	k, _, err := registry.CreateKey(registry.CURRENT_USER, uiStateKey, registry.SET_VALUE)
	if err != nil {
		return err
	}
	defer k.Close()

	v := "0"
	if shown {
		v = "1"
	}
	return k.SetStringValue(advancedShownName, v)
}
