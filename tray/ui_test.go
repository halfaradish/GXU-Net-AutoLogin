//go:build windows

package main

import (
	"bytes"
	"os"
	"runtime"
	"testing"
	"unsafe"

	"github.com/halfaradish/GXU-Net-AutoLogin/internal/config"
	"github.com/halfaradish/GXU-Net-AutoLogin/internal/daemon"
	"github.com/halfaradish/GXU-Net-AutoLogin/internal/logging"
	"github.com/lxn/walk"
	"github.com/lxn/win"
)

// WM_GETICON 的 wParam：0 = ICON_SMALL（标题栏），1 = ICON_BIG（任务栏 / Alt+Tab）
const (
	wparamIconSmall = 0
	wparamIconBig   = 1
)

// 主界面窗口图标必须是 exe 里嵌的那个（与托盘图标同一个），不能退回系统自带的
// 通用图标：任务栏、Alt+Tab、标题栏看的都是它，写成 walk.IconApplication()
// 任务栏就显示成"默认图标"了。
//
// 比的是像素而不是句柄：walk 给系统图标走 LoadIconWithScaleDown，同一个图标
// 每次拿到的句柄都不一样，比句柄区分不出来。
func TestMainWindowIconIsAppIcon(t *testing.T) {
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()

	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	// 测试二进制同样链接了 rsrc.syso，所以自己也带图标；万一没有就跳过，
	// 免得把"这个二进制没嵌图标"误判成"窗口图标设错了"。
	if _, err := walk.NewIconExtractedFromFileWithSize(exe, 0, 32); err != nil {
		t.Skipf("二进制里没有图标资源，跳过：%v", err)
	}

	a := &app{
		exePath: exe,
		dir:     t.TempDir(),
		cfg:     config.Defaults(),
		log:     logging.New(logging.Options{}),
		icon:    appIcon(exe),
	}
	a.daemon = daemon.New(a.log, a.cfg)

	u, err := newUI(a)
	if err != nil {
		t.Fatalf("建界面失败：%v", err)
	}
	defer u.mw.Dispose()

	hwnd := u.mw.Handle()
	for _, tc := range []struct {
		name   string
		wparam uintptr
	}{
		{"ICON_SMALL", wparamIconSmall},
		{"ICON_BIG", wparamIconBig},
	} {
		got := win.HICON(win.SendMessage(hwnd, win.WM_GETICON, tc.wparam, 0))
		if got == 0 {
			t.Fatalf("%s 没设上", tc.name)
		}
		gotPix, w, h := iconPixels(t, got)

		// 按同样的尺寸取一张系统自带的通用图标作对照
		var stock win.HICON
		if hr := win.LoadIconWithScaleDown(0, win.MAKEINTRESOURCE(win.IDI_APPLICATION), w, h, &stock); win.FAILED(hr) || stock == 0 {
			t.Fatalf("取系统图标失败：%#x", hr)
		}
		stockPix, sw, sh := iconPixels(t, stock)
		if w != sw || h != sh {
			t.Fatalf("尺寸对不上：%dx%d vs %dx%d", w, h, sw, sh)
		}

		if bytes.Equal(gotPix, stockPix) {
			t.Fatalf("%s 就是系统自带的通用图标（%dx%d），任务栏会显示成默认图标", tc.name, w, h)
		}
		t.Logf("%s: %dx%d，与系统图标不同 ✓", tc.name, w, h)
	}
}

// iconPixels 读出 HICON 彩色位图的像素（32bpp BGRA，自上而下）
func iconPixels(t *testing.T, h win.HICON) ([]byte, int32, int32) {
	t.Helper()

	var ii win.ICONINFO
	if !win.GetIconInfo(h, &ii) {
		t.Fatal("GetIconInfo 失败")
	}
	defer win.DeleteObject(win.HGDIOBJ(ii.HbmMask))
	if ii.HbmColor == 0 {
		t.Fatal("图标没有彩色位图")
	}
	defer win.DeleteObject(win.HGDIOBJ(ii.HbmColor))

	var bm win.BITMAP
	if win.GetObject(win.HGDIOBJ(ii.HbmColor), unsafe.Sizeof(bm), unsafe.Pointer(&bm)) == 0 {
		t.Fatal("GetObject 失败")
	}

	bmi := win.BITMAPINFO{
		BmiHeader: win.BITMAPINFOHEADER{
			BiSize:        uint32(unsafe.Sizeof(win.BITMAPINFOHEADER{})),
			BiWidth:       bm.BmWidth,
			BiHeight:      -bm.BmHeight, // 负高度：自上而下，行序才确定
			BiPlanes:      1,
			BiBitCount:    32,
			BiCompression: win.BI_RGB,
		},
	}
	buf := make([]byte, int(bm.BmWidth)*int(bm.BmHeight)*4)

	hdc := win.GetDC(0)
	defer win.ReleaseDC(0, hdc)
	if win.GetDIBits(hdc, ii.HbmColor, 0, uint32(bm.BmHeight), &buf[0], &bmi, win.DIB_RGB_COLORS) == 0 {
		t.Fatal("GetDIBits 失败")
	}

	return buf, bm.BmWidth, bm.BmHeight
}
