//go:build windows

package main

import (
	"os"
	"path/filepath"
	"testing"

	"golang.org/x/sys/windows/registry"
)

// 这个用例会真的写/删注册表启动项，因此默认跳过；
// 需要验证"开机自启动"时用 GXU_TEST_REGISTRY=1 跑：
//
//	GXU_TEST_REGISTRY=1 go test -run TestApplyAutostart ./...
func TestApplyAutostart(t *testing.T) {
	if os.Getenv("GXU_TEST_REGISTRY") == "" {
		t.Skip("设置 GXU_TEST_REGISTRY=1 才会读写注册表")
	}

	exe := filepath.Join(`C:\Program Files\GXU-Net-AutoLogin`, "GXU_Net_AutoLogin_Tray.exe")

	// 打开：写入带引号的绝对路径（路径里有空格也不会被拆开）
	if err := applyAutostart(true, exe); err != nil {
		t.Fatalf("写入启动项失败：%v", err)
	}
	k, err := registry.OpenKey(registry.CURRENT_USER, autostartKey, registry.QUERY_VALUE)
	if err != nil {
		t.Fatal(err)
	}
	got, _, err := k.GetStringValue(autostartName)
	k.Close()
	if err != nil {
		t.Fatalf("读不到刚写入的启动项：%v", err)
	}
	if want := `"` + exe + `"`; got != want {
		t.Fatalf("启动项内容不对：\n got=%s\nwant=%s", got, want)
	}

	// 关闭：键值必须被删掉
	if err := applyAutostart(false, exe); err != nil {
		t.Fatalf("删除启动项失败：%v", err)
	}
	k2, err := registry.OpenKey(registry.CURRENT_USER, autostartKey, registry.QUERY_VALUE)
	if err != nil {
		t.Fatal(err)
	}
	defer k2.Close()
	if _, _, err := k2.GetStringValue(autostartName); err == nil {
		t.Fatal("关闭开机自启动后注册表里还留着启动项")
	}

	// 重复删除不应报错（第一次保存时本来就没有这个键）
	if err := applyAutostart(false, exe); err != nil {
		t.Fatalf("重复删除应当无害，实际：%v", err)
	}
}

// shellOpen 的返回值判定：打不开的路径要报错，存在的路径交给系统打开。
// 这里只验证"返回值处理"这段逻辑，不真的等外部程序起来。
func TestShellOpenBadPath(t *testing.T) {
	if err := shellOpen(filepath.Join(os.TempDir(), "gxu-not-exist-", "nope.txt")); err == nil {
		t.Fatal("不存在的路径应当返回错误")
	}
}
