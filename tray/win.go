//go:build windows

// 托盘版直接调用 Win32 的那一层：单实例、隐藏消息窗口、DPI、打开文件/目录、
// 对话框、图标。
//
// 托盘图标本身不用自己写——walk 自带 NotifyIcon（图标、提示、气泡、右键菜单、
// 资源管理器重启后自动补图标）。这里只补 walk 没提供的东西。
//
// 隐藏消息窗口建在 walk 主窗口那条线程上：Windows 的消息循环是按线程的，这些
// 消息由 walk 的消息循环一并派发，因此回调天然跑在 UI 线程上。
package main

import (
	"fmt"
	"syscall"
	"unsafe"

	"github.com/lxn/walk"
	"github.com/lxn/win"
	"golang.org/x/sys/windows"
)

// 自定义消息：二次启动唤醒、-quit 请求退出
const (
	wmApp         = 0x8000 // WM_APP
	wmShowWindow  = wmApp + 1
	wmQuitRequest = wmApp + 2
)

// 隐藏消息窗口的类名：二次启动靠它找到已有实例
const trayWindowClass = "GXU_Net_AutoLogin_Tray"

var (
	user32   = windows.NewLazySystemDLL("user32.dll")
	shell32  = windows.NewLazySystemDLL("shell32.dll")
	kernel32 = windows.NewLazySystemDLL("kernel32.dll")

	procShellExecuteW      = shell32.NewProc("ShellExecuteW")
	procSetProcessDPIAware = user32.NewProc("SetProcessDPIAware")
	procCreateMutexW       = kernel32.NewProc("CreateMutexW")
)

func utf16(s string) *uint16 {
	p, err := windows.UTF16PtrFromString(s)
	if err != nil {
		return nil
	}
	return p
}

// setDPIAware 让界面在高分屏上不糊，必须在建窗之前调用
func setDPIAware() {
	procSetProcessDPIAware.Call()
}

// ── 隐藏消息窗口 ──────────────────────────────────────────

// hiddenWindow 是一个不可见的顶层窗口，只用来接收"唤醒/退出"消息
type hiddenWindow struct {
	hwnd  win.HWND
	onMsg func(msg uint32)
}

// WndProc 是全局回调，只能通过包级变量把消息转交回实例
var hidden *hiddenWindow

func hiddenWndProc(hwnd win.HWND, msg uint32, wparam, lparam uintptr) uintptr {
	if hidden != nil && (msg == wmShowWindow || msg == wmQuitRequest) {
		hidden.onMsg(msg)
		return 0
	}
	return win.DefWindowProc(hwnd, msg, wparam, lparam)
}

func newHiddenWindow(onMsg func(msg uint32)) (*hiddenWindow, error) {
	hInst := win.GetModuleHandle(nil)
	className := utf16(trayWindowClass)

	wc := win.WNDCLASSEX{
		CbSize:        uint32(unsafe.Sizeof(win.WNDCLASSEX{})),
		LpfnWndProc:   syscall.NewCallback(hiddenWndProc),
		HInstance:     hInst,
		LpszClassName: className,
	}
	if win.RegisterClassEx(&wc) == 0 {
		return nil, fmt.Errorf("注册窗口类失败：%v", windows.GetLastError())
	}

	hwnd := win.CreateWindowEx(0, className, utf16("GXU-Net-AutoLogin"),
		0, 0, 0, 0, 0, 0, 0, hInst, nil)
	if hwnd == 0 {
		return nil, fmt.Errorf("创建消息窗口失败：%v", windows.GetLastError())
	}

	hidden = &hiddenWindow{hwnd: hwnd, onMsg: onMsg}
	return hidden, nil
}

func (h *hiddenWindow) destroy() {
	if h.hwnd != 0 {
		win.DestroyWindow(h.hwnd)
		h.hwnd = 0
	}
	hidden = nil
}

// findExistingWindow 找到已在运行实例的消息窗口
func findExistingWindow() win.HWND {
	return win.FindWindow(utf16(trayWindowClass), nil)
}

// ── 单实例 ────────────────────────────────────────────────

const mutexName = `Local\GXU-Net-AutoLogin-Tray`

var instanceMutex windows.Handle

// acquireSingleInstance 取命名互斥体；返回 false 表示已经有实例在跑。
// 这里直接调 CreateMutexW 是为了拿到 ERROR_ALREADY_EXISTS——Go 的包装函数只在
// 失败时返回错误，成功时会把"已存在"这个信息丢掉。
func acquireSingleInstance() bool {
	r1, _, err := procCreateMutexW.Call(0, 0, uintptr(unsafe.Pointer(utf16(mutexName))))
	h := windows.Handle(r1)
	if h == 0 {
		return true // 互斥体建不出来也让程序继续跑，不因为这点小事拒绝启动
	}
	instanceMutex = h
	return err != windows.ERROR_ALREADY_EXISTS
}

// wakeExistingInstance 让已经在跑的实例把主界面显示出来
func wakeExistingInstance() bool {
	hwnd := findExistingWindow()
	if hwnd == 0 {
		return false
	}
	win.PostMessage(hwnd, wmShowWindow, 0, 0)
	return true
}

// requestQuitExisting 让已经在跑的实例退出（-quit 参数）
func requestQuitExisting() bool {
	hwnd := findExistingWindow()
	if hwnd == 0 {
		return false
	}
	win.PostMessage(hwnd, wmQuitRequest, 0, 0)
	return true
}

// ── 杂项 ──────────────────────────────────────────────────

// shellOpen 用系统默认程序打开文件或目录
func shellOpen(path string) error {
	const swShowNormal = 1
	ret, _, _ := procShellExecuteW.Call(0,
		uintptr(unsafe.Pointer(utf16("open"))),
		uintptr(unsafe.Pointer(utf16(path))),
		0, 0, swShowNormal)
	if ret <= 32 {
		return fmt.Errorf("无法打开 %s", path)
	}
	return nil
}

// messageBox 是给用户看的提示/确认对话框
func messageBox(title, text string, flags uint32) int {
	return int(win.MessageBox(0, utf16(text), utf16(title), flags))
}

// workArea 是窗口当前所在显示器的工作区（物理像素，已扣掉任务栏）。
// 窗口尺寸要按它夹紧：高分屏（比如 2560x1600 @175%）上逻辑可用高度比想象中
// 小得多，不夹紧的话窗口会顶出屏幕，底部按钮点不到、最大化时标题栏还会被顶到
// 屏幕上方之外。
func workArea(hwnd win.HWND) (win.RECT, error) {
	var mi win.MONITORINFO
	mi.CbSize = uint32(unsafe.Sizeof(mi))
	if !win.GetMonitorInfo(win.MonitorFromWindow(hwnd, win.MONITOR_DEFAULTTONEAREST), &mi) {
		return win.RECT{}, fmt.Errorf("取显示器工作区失败：%v", windows.GetLastError())
	}
	return mi.RcWork, nil
}

// appIcon 取程序图标：打包时 rsrc 会把 .ico 嵌进 exe，这里把它抠出来；
// 没嵌图标就退回系统默认图标，保证界面上一定有东西。
func appIcon(exePath string) *walk.Icon {
	if icon, err := walk.NewIconExtractedFromFileWithSize(exePath, 0, 32); err == nil {
		return icon
	}
	return walk.IconApplication()
}
