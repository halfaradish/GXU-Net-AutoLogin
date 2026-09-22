//go:build windows

package main

import (
	"errors"
	"os"
	"path/filepath"
	"testing"

	"golang.org/x/sys/windows"
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

// 界面状态（高级选项开合）的注册表读写。同样默认跳过，需要时用
// GXU_TEST_REGISTRY=1 跑。
//
// 这个键下的值可能是用户正在用的状态（同机上还有别的实现会写同一个键），
// 所以用例先把原值存下来，跑完再放回去，绝不留下"被测试清掉"的后果。
func TestAdvancedShownState(t *testing.T) {
	if os.Getenv("GXU_TEST_REGISTRY") == "" {
		t.Skip("设置 GXU_TEST_REGISTRY=1 才会读写注册表")
	}

	// 记下原值（可能来自另一个实现，格式也可能是 DWORD）
	prev, hadPrev, prevErr := readRawState(t)

	cleanup := func() {
		k, _, err := registry.CreateKey(registry.CURRENT_USER, uiStateKey, registry.SET_VALUE)
		if err != nil {
			t.Fatalf("恢复现场失败：%v", err)
		}
		defer k.Close()
		if hadPrev {
			if prevErr == nil && prev.isString {
				if err := k.SetStringValue(advancedShownName, prev.str); err != nil {
					t.Fatalf("恢复现场失败：%v", err)
				}
			} else {
				if err := k.SetDWordValue(advancedShownName, uint32(prev.dword)); err != nil {
					t.Fatalf("恢复现场失败：%v", err)
				}
			}
			return
		}
		if err := k.DeleteValue(advancedShownName); err != nil && !errors.Is(err, windows.ERROR_FILE_NOT_FOUND) {
			t.Fatalf("清理现场失败：%v", err)
		}
	}
	defer cleanup()

	// 删掉键值，模拟首次运行
	k, err := registry.OpenKey(registry.CURRENT_USER, uiStateKey, registry.SET_VALUE)
	if err == nil {
		k.DeleteValue(advancedShownName)
		k.Close()
	}

	// 首次运行：没有记录 → 收起，且不算错误
	got, err := loadAdvancedShown()
	if err != nil {
		t.Fatalf("没有记录时不该报错：%v", err)
	}
	if got {
		t.Fatal("首次运行默认应当是收起（false）")
	}

	// 展开后要能读回来
	if err := saveAdvancedShown(true); err != nil {
		t.Fatalf("写入界面状态失败：%v", err)
	}
	if got, err := loadAdvancedShown(); err != nil || !got {
		t.Fatalf("读回的展开状态不对：%v %v", got, err)
	}

	// 收起同样要能读回来（不能只是"有值就 true"）
	if err := saveAdvancedShown(false); err != nil {
		t.Fatalf("写入界面状态失败：%v", err)
	}
	if got, err := loadAdvancedShown(); err != nil || got {
		t.Fatalf("读回的收起状态不对：%v %v", got, err)
	}

	// 写法要与同机上已有的约定一致：字符串 "0"/"1"
	if s, _, err := readStringState(t); err != nil || s != "0" {
		t.Fatalf("应当写成字符串 \"0\"，实际 %q err=%v", s, err)
	}

	// 容忍 DWORD 形式：别的实现若写成 DWORD，也要能读出来
	k2, _, err := registry.CreateKey(registry.CURRENT_USER, uiStateKey, registry.SET_VALUE)
	if err != nil {
		t.Fatal(err)
	}
	if err := k2.SetDWordValue(advancedShownName, 1); err != nil {
		k2.Close()
		t.Fatalf("写入 DWORD 失败：%v", err)
	}
	k2.Close()
	if got, err := loadAdvancedShown(); err != nil || !got {
		t.Fatalf("DWORD 形式的展开状态没读出来：%v %v", got, err)
	}
}

type rawState struct {
	isString bool
	str      string
	dword    uint64
}

// readRawState 读出原值的原始形式（用于跑完还原）
func readRawState(t *testing.T) (rawState, bool, error) {
	t.Helper()

	k, err := registry.OpenKey(registry.CURRENT_USER, uiStateKey, registry.QUERY_VALUE)
	if err != nil {
		if errors.Is(err, windows.ERROR_FILE_NOT_FOUND) {
			return rawState{}, false, nil
		}
		return rawState{}, false, err
	}
	defer k.Close()

	if s, _, err := k.GetStringValue(advancedShownName); err == nil {
		return rawState{isString: true, str: s}, true, nil
	}
	if v, _, err := k.GetIntegerValue(advancedShownName); err == nil {
		return rawState{dword: v}, true, nil
	}
	return rawState{}, false, nil
}

func readStringState(t *testing.T) (string, bool, error) {
	t.Helper()

	k, err := registry.OpenKey(registry.CURRENT_USER, uiStateKey, registry.QUERY_VALUE)
	if err != nil {
		return "", false, err
	}
	defer k.Close()

	s, _, err := k.GetStringValue(advancedShownName)
	return s, true, err
}
