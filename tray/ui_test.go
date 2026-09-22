//go:build windows

package main

import (
	"bytes"
	"os"
	"runtime"
	"testing"
	"time"
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

// newTestUI 把一个和线上一样的主界面搭出来（真窗口），测试用完自动销毁。
// 只填 newUI 会用到的那几个字段：配置、日志、守护进程（不启动）、图标。
func newTestUI(t *testing.T) *ui {
	t.Helper()

	runtime.LockOSThread()
	t.Cleanup(runtime.UnlockOSThread)

	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
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
	t.Cleanup(func() {
		u.mw.Dispose()
	})

	return u
}

// monitor 是窗口所在显示器的度量，断言都按它算，这样在任何分辨率/缩放下都成立。
type monitor struct {
	work    win.RECT
	caption int32 // 标题栏高度
	dpi     uint32
}

func monitorOf(t *testing.T, hwnd win.HWND) monitor {
	t.Helper()

	work, err := workArea(hwnd)
	if err != nil {
		t.Fatal(err)
	}
	dpi := win.GetDpiForWindow(hwnd)

	return monitor{
		work:    work,
		caption: win.GetSystemMetricsForDpi(win.SM_CYCAPTION, dpi),
		dpi:     dpi,
	}
}

// 窗口的最小尺寸不能超过屏幕工作区，否则用户没法把窗口拖到装得下 —— 在小屏幕
// 或高缩放（本机 2560x1600 @175%，逻辑可用高度只有 ~866px）上就是"高度调不动、
// 底部按钮点不到"，最大化时标题栏还会被顶到屏幕上方之外。
//
// 这个最小值由 walk 取 max(内容布局最小尺寸, 应用声明的最小尺寸) 得出来
// （walk/form.go 的 WM_GETMINMAXINFO），所以内容一旦累加得过高就会顶穿。
func TestWindowMinTrackSizeFitsScreen(t *testing.T) {
	u := newTestUI(t)
	hwnd := u.mw.Handle()
	m := monitorOf(t, hwnd)

	// walk 只在窗口已经有"目标尺寸"时才认真算最小尺寸，先摆一下
	if err := u.mw.SetBoundsPixels(walk.Rectangle{X: 100, Y: 100, Width: 760, Height: 660}); err != nil {
		t.Fatal(err)
	}

	workW := m.work.Right - m.work.Left
	workH := m.work.Bottom - m.work.Top

	for _, tc := range []struct {
		name   string
		expand bool
	}{
		{"收起高级选项", false},
		{"展开高级选项", true},
	} {
		u.advanced.SetVisible(tc.expand)

		// 系统会先填好 MINMAXINFO 再发这条消息，这里手工发，清零就够读了：
		// walk 只写 PtMinTrackSize 一个字段
		var mmi win.MINMAXINFO
		win.SendMessage(hwnd, win.WM_GETMINMAXINFO, 0, uintptr(unsafe.Pointer(&mmi)))

		gotW, gotH := mmi.PtMinTrackSize.X, mmi.PtMinTrackSize.Y
		t.Logf("%s：最小尺寸 %dx%d 物理px，工作区 %dx%d 物理px（dpi %d）",
			tc.name, gotW, gotH, workW, workH, m.dpi)

		if gotW > workW || gotH > workH {
			t.Errorf("%s：窗口最小尺寸 %dx%d 超过工作区 %dx%d，用户拖不小、内容会被顶出屏幕",
				tc.name, gotW, gotH, workW, workH)
		}
	}
}

// 窗口摆出来之后必须整个装进屏幕：底部按钮不能被顶到屏幕下边缘之外。
// 走到这一步只用了 walk 自己的布局（startLayout 会把窗口撑到内容的最小高度），
// 没有任何夹紧逻辑。
func TestShownWindowFitsScreen(t *testing.T) {
	u := newTestUI(t)
	hwnd := u.mw.Handle()
	m := monitorOf(t, hwnd)
	tol := nonClientTolerance(t, hwnd)

	for _, tc := range []struct {
		name   string
		expand bool
	}{
		{"收起高级选项", false},
		{"展开高级选项", true},
	} {
		u.advanced.SetVisible(tc.expand)
		win.ShowWindow(hwnd, win.SW_SHOW)
		runMessageLoop(t, u, 250*time.Millisecond)

		frame := windowRect(t, hwnd)
		t.Logf("%s：窗口 %d,%d-%d,%d（%dx%d），工作区 %d,%d-%d,%d",
			tc.name, frame.Left, frame.Top, frame.Right, frame.Bottom,
			frame.Right-frame.Left, frame.Bottom-frame.Top,
			m.work.Left, m.work.Top, m.work.Right, m.work.Bottom)

		if frame.Left < m.work.Left-tol || frame.Top < m.work.Top-tol ||
			frame.Right > m.work.Right+tol || frame.Bottom > m.work.Bottom+tol {
			t.Errorf("%s：窗口 %d,%d-%d,%d 超出工作区 %d,%d-%d,%d，底部按钮会被顶到屏幕外",
				tc.name, frame.Left, frame.Top, frame.Right, frame.Bottom,
				m.work.Left, m.work.Top, m.work.Right, m.work.Bottom)
		}

		content, view := scrollState(t, u.scroll)
		if view == 0 {
			t.Fatalf("%s：内容区可视高度为 0，布局没跑起来（消息循环时间不够？）", tc.name)
		}
		// 窗口里除了内容区之外的部分：标题栏 + 边框 + 底部按钮行 + 边距
		chrome := (frame.Bottom - frame.Top) - view
		t.Logf("%s：内容高 %d 物理px，可视 %d，窗口其余部分 %d", tc.name, content, view, chrome)

		// 屏幕装得下的内容就不该要滚动 —— 打开就能看全是这台小屏笔记本的诉求。
		// 装不下（比如高级选项展开后）才允许出滚动条。
		if content > view && content+chrome <= m.work.Bottom-m.work.Top {
			t.Errorf("%s：屏幕装得下（内容+其余=%d ≤ 工作区 %d），但默认窗口只有 %d 高，还得滚动 %d px",
				tc.name, content+chrome, m.work.Bottom-m.work.Top,
				frame.Bottom-frame.Top, content-view)
		}

		win.ShowWindow(hwnd, win.SW_RESTORE)
		win.ShowWindow(hwnd, win.SW_HIDE)
	}
}

// 最大化之后窗口必须真的装进屏幕：客户区不能比工作区还大，标题栏也不能被顶到
// 屏幕上方之外（用户报的就是"最大化后看不到最小化/最大化/关闭三个按钮"）。
func TestMaximizedWindowFitsScreen(t *testing.T) {
	u := newTestUI(t)
	hwnd := u.mw.Handle()
	m := monitorOf(t, hwnd)
	tol := nonClientTolerance(t, hwnd)

	workW := int(m.work.Right - m.work.Left)
	workH := int(m.work.Bottom - m.work.Top)

	for _, tc := range []struct {
		name   string
		expand bool
	}{
		{"收起高级选项", false},
		{"展开高级选项", true},
	} {
		u.advanced.SetVisible(tc.expand)

		win.ShowWindow(hwnd, win.SW_RESTORE)
		win.ShowWindow(hwnd, win.SW_MAXIMIZE)

		frame := windowRect(t, hwnd)
		client := clientSize(t, hwnd)

		t.Logf("%s：最大化后窗口 %d,%d-%d,%d（%dx%d），客户区 %dx%d，工作区 %d,%d-%d,%d，标题栏高 %d",
			tc.name, frame.Left, frame.Top, frame.Right, frame.Bottom,
			frame.Right-frame.Left, frame.Bottom-frame.Top, client.Width, client.Height,
			m.work.Left, m.work.Top, m.work.Right, m.work.Bottom, m.caption)

		// 客户区比工作区大 → 多出来的部分被推到屏幕外：正常最大化时客户区高度
		// 是工作区高度再减掉标题栏，所以这里用工作区高度做上限
		if client.Width > workW+1 || client.Height > workH+1 {
			t.Errorf("%s：最大化后客户区 %dx%d 超过工作区 %dx%d，内容会被推到屏幕外",
				tc.name, client.Width, client.Height, workW, workH)
		}
		// 标题栏整个跑到工作区上方 → 三个按钮都看不见了
		if frame.Top+m.caption <= m.work.Top {
			t.Errorf("%s：最大化后标题栏（顶边 %d，高 %d）完全在工作区上方（顶边 %d），按钮看不到",
				tc.name, frame.Top, m.caption, m.work.Top)
		}
		// 右上/下也不能顶出去（留一个边框的余量：最大化时窗口边框正好压在屏幕边上）
		if frame.Left < m.work.Left-tol || frame.Right > m.work.Right+tol ||
			frame.Bottom > m.work.Bottom+tol {
			t.Errorf("%s：最大化后窗口超出工作区：窗口 %d,%d-%d,%d，工作区 %d,%d-%d,%d",
				tc.name, frame.Left, frame.Top, frame.Right, frame.Bottom,
				m.work.Left, m.work.Top, m.work.Right, m.work.Bottom)
		}
	}

	win.ShowWindow(hwnd, win.SW_RESTORE)
	win.ShowWindow(hwnd, win.SW_HIDE)
}

func windowRect(t *testing.T, hwnd win.HWND) win.RECT {
	t.Helper()

	var r win.RECT
	if !win.GetWindowRect(hwnd, &r) {
		t.Fatal("GetWindowRect 失败")
	}
	return r
}

func clientSize(t *testing.T, hwnd win.HWND) walk.Size {
	t.Helper()

	var r win.RECT
	if !win.GetClientRect(hwnd, &r) {
		t.Fatal("GetClientRect 失败")
	}
	return walk.Size{Width: int(r.Right - r.Left), Height: int(r.Bottom - r.Top)}
}

// nonClientTolerance 是"单边边框厚度"：普通窗口的 frame 与 client 之差的一半。
// 最大化时窗口四边正好压在屏幕外这么多像素（SM_CXSIZEFRAME 还漏算了
// SM_CXPADDEDBORDER，所以这里直接量，别用 GetSystemMetrics 推）。
func nonClientTolerance(t *testing.T, hwnd win.HWND) int32 {
	t.Helper()

	frame := windowRect(t, hwnd)
	client := clientSize(t, hwnd)

	return (frame.Right - frame.Left - int32(client.Width)) / 2
}

// runMessageLoop 跑一小会儿 walk 的消息循环，让布局真正算完并应用到控件上。
//
// 布局是异步的：算好之后要等 UI 线程的消息循环（walk 的 mainLoop -> RunSynchronized）
// 才应用，测试里光抽消息不够，只能真跑一下循环。用投递的 WM_QUIT 让它退出。
func runMessageLoop(t *testing.T, u *ui, d time.Duration) {
	t.Helper()

	go func() {
		time.Sleep(d)
		if win.PostMessage(u.mw.Handle(), win.WM_QUIT, 0, 0) == 0 {
			t.Error("PostMessage(WM_QUIT) 失败")
		}
	}()

	if code := u.mw.Run(); code != 0 {
		t.Errorf("消息循环返回 %d", code)
	}
}

// scrollState 返回内容区里"内容高度"和"可视高度"（物理px）：前者比后者大就说明
// 有内容被挡住、要滚动才看得到。
func scrollState(t *testing.T, sv *walk.ScrollView) (content, view int32) {
	t.Helper()

	var si win.SCROLLINFO
	si.CbSize = uint32(unsafe.Sizeof(si))
	si.FMask = win.SIF_PAGE | win.SIF_RANGE
	if !win.GetScrollInfo(sv.Handle(), win.SB_VERT, &si) {
		t.Fatal("GetScrollInfo 失败")
	}

	return si.NMax + 1, int32(si.NPage)
}

// 按钮行必须留在主窗口上、不能进滚动区：内容再长，按钮也一直钉在底部。
// 反过来内容各组必须在滚动区里，否则它们的最小高度又会把整个窗口顶大 ——
// 那就退回原来"小屏拖不小、按钮点不到"的老毛病了。
func TestButtonsPinnedOutsideScrollArea(t *testing.T) {
	u := newTestUI(t)

	if isInside(u.advBtn.Handle(), u.scroll.Handle()) {
		t.Error("按钮行跑到滚动区里了：内容一长按钮就会被滚到看不见的地方")
	}
	for _, tc := range []struct {
		name string
		w    walk.Widget
	}{
		{"校园网账号", u.userEdit},
		{"运行状态", u.stateVal},
		{"日志区", u.logView},
		{"高级选项", u.advanced},
	} {
		if !isInside(tc.w.Handle(), u.scroll.Handle()) {
			t.Errorf("%s 没在滚动区里，它的最小高度会把窗口顶大", tc.name)
		}
	}
}

// isInside 判断窗口 hwnd 是否在容器 container 里面（沿父窗口链往上找）
func isInside(hwnd, container win.HWND) bool {
	for h := win.GetParent(hwnd); h != 0; h = win.GetParent(h) {
		if h == container {
			return true
		}
	}
	return false
}

// 主界面窗口图标必须是 exe 里嵌的那个（与托盘图标同一个），不能退回系统自带的
// 通用图标：任务栏、Alt+Tab、标题栏看的都是它，写成 walk.IconApplication()
// 任务栏就显示成"默认图标"了。
//
// 比的是像素而不是句柄：walk 给系统图标走 LoadIconWithScaleDown，同一个图标
// 每次拿到的句柄都不一样，比句柄区分不出来。
func TestMainWindowIconIsAppIcon(t *testing.T) {
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	// 测试二进制同样链接了 rsrc.syso，所以自己也带图标；万一没有就跳过，
	// 免得把"这个二进制没嵌图标"误判成"窗口图标设错了"。
	if _, err := walk.NewIconExtractedFromFileWithSize(exe, 0, 32); err != nil {
		t.Skipf("二进制里没有图标资源，跳过：%v", err)
	}

	u := newTestUI(t)
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
