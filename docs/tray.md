# 托盘版实现说明

面向维护者，记录托盘版（`tray/`）的几个关键决定与坑。用户文档见 [README](../README.md#-windows-托盘版)。

## 为什么是独立 module

`tray/go.mod` 是**独立 module**，通过 `replace` 引用仓库根 module：

```
module github.com/halfaradish/GXU-Net-AutoLogin/tray
require github.com/halfaradish/GXU-Net-AutoLogin v0.0.0
replace github.com/halfaradish/GXU-Net-AutoLogin => ../
```

这样根 module 保持**零第三方依赖**：命令行版依旧 `go build main.go` 就能编，Linux/AUR 那条路径不受任何影响；只有托盘版需要拉 `github.com/lxn/walk`（+ 传递依赖 `lxn/win`、`golang.org/x/sys`、`gopkg.in/Knetic/govaluate.v3`）。

`internal/` 包虽然属于根 module，但托盘 module 通过 `replace` 指到本地目录后可以正常 import —— Go 的 `internal` 规则按导入路径判断，`.../GXU-Net-AutoLogin/tray` 在 `.../GXU-Net-AutoLogin/` 这棵树下，因此合法。

## 为什么不用 cgo

本机（以及多数 Windows 开发机）`CGO_ENABLED=0`。`getlantern/systray`、`fyne.io/systray` 都需要 cgo，因此托盘图标用 walk 自带的 `walk.NotifyIcon`，配合 walk 本身就够用：图标、悬停提示、气泡通知、右键菜单（walk 的 Action）、资源管理器重启后自动补图标，全都有。

## 线程模型：一套消息循环

Windows 的消息循环是**按线程**的。所以：

- 主 goroutine `runtime.LockOSThread()`，walk 的窗口与消息循环都在这里；
- 托盘图标挂在 walk 的主窗口上（`walk.NewNotifyIcon(mw)`）；
- 单实例唤醒用的隐藏窗口也用 `CreateWindowEx` 建在**同一条线程**上，它的消息由 walk 的循环派发到自己的 `WndProc`。

于是托盘菜单回调天然运行在 UI 线程，可以直接操作控件；只有两类跨线程来源需要注意：

- 守护进程的状态事件（断线/恢复/静默）→ 用 `walk.Form.Synchronize`（异步投递）排到 UI 线程再弹气泡；
- 每秒一次的界面刷新 → 同样走 `Synchronize`，一次一个 ticker，不会堆积。

## 必须打包资源文件（rsrc.syso）

walk 依赖 Common Controls v6。**没有清单文件时，`walk.NewMainWindow()` 会直接失败**（报 `TTM_ADDTOOL failed`，因为 tooltip 控件建不出来），窗口都出不来——不是"外观变旧"这么轻。

所以 `tray/rsrc.syso` 必须入库，它由 rsrc 生成：

```bash
go install github.com/akavel/rsrc@latest
cd tray
rsrc -arch amd64 -manifest tray.exe.manifest -ico assets/icon.ico -o rsrc.syso
```

`.syso` 同时嵌入：Common Controls v6 清单、`asInvoker` 权限声明、`dpiAware`，以及图标。改图标或清单后必须重新生成。

## 图标

`tools/genicon` 用纯标准库手写 ICO：16/32/48 是经典 BMP(DIB) 条目，256 是 PNG 条目，每张图 4×4 超采样。改图标只需改 `tools/genicon/main.go` 里的绘制函数，然后：

```bash
go run ./tools/genicon
cd tray && rsrc -arch amd64 -manifest tray.exe.manifest -ico assets/icon.ico -o rsrc.syso
```

托盘图标与主界面窗口图标是**同一个** `walk.Icon`（`appIcon(exe)` 从 exe 抠出来的，见 `tray/win.go`）：托盘走 `NotifyIcon.SetIcon`，窗口走 `ui.go` 里的 `mw.SetIcon(a.icon)`。窗口图标别写成 `walk.IconApplication()`——那是系统自带的通用图标，`WM_SETICON` 会压过 exe 的图标资源，任务栏和 Alt+Tab 就变回默认样子了。尺寸由 walk 的 `FormBase.SetIcon` 按 DPI 从源图标派生（16px 给标题栏、32px 给任务栏），所以 `appIcon` 按 32 取即可。

## 核心逻辑的可测性

`internal/daemon` 把探测、登录、认证身份解析都做成了可注入的函数（`WithProber`/`WithAuthenticator`/`WithIdentityResolver`/`WithTiming`），因此状态机可以在毫秒级跑完整场景：断网判定、退避重试、静默进入与退出、恢复统计、保存并应用后立即认证。见 `internal/daemon/daemon_test.go`。

一个刻意的设计：**登录的成败不改写连接状态**。连接状态只由探测决定（门户说"认证成功"而探测仍失败的情况确实存在，重试期间用 `StateAuthing` 表示正在认证）。2026-09 的单元测试就是因为最初的实现把状态判错了才补上的。

## 窗口尺寸：下限不能被内容决定

小屏幕上（尤其高缩放，本机 2560x1600 @175% 的逻辑可用高度只有 ~866px）出过三个
毛病：窗口高度拖不小、底部按钮被顶出屏幕、最大化后标题栏跑到屏幕上方之外。根因
是同一个 —— **walk 把 `max(内容布局的最小尺寸, mw.SetMinMaxSize 声明的尺寸)` 当窗口
的最小可拖拽尺寸**（walk/form.go 的 `WM_GETMINMAXINFO`），而内容累加起来有 800
逻辑px 以上（高级选项展开时 1100+），比屏幕还高；窗口被撑到超过工作区后，最大化时
系统把客户区贴到工作区左上角，多出来的高度就全推到了标题栏之上。

所以有两条硬约定：

- **内容都放进 `u.scroll`（ScrollView），只有底部按钮行留在主窗口上。** ScrollView 的
  最小高度是 0，窗口的下限从此只由 `minWindowWidth/minWindowHeight` 决定；按钮行在
  滚动区外面，内容再长也钉在底部可点。往界面上加东西时**别把控件挂回 `u.mw`**
  （`tray/ui_test.go` 的 `TestButtonsPinnedOutsideScrollArea` 会拦）。
- **启动尺寸由 `applyStartupBounds` 按显示器工作区夹紧**（`workArea()` + 居中），不是
  写死的 760x660：屏幕够高就一次看全，不够高就铺满可用高度、内容区自己出滚动条。

`tray/ui_test.go` 里有四个用例盯着这几点（最小尺寸不超过工作区、窗口/最大化后装得下
屏幕、标题栏没被顶出去、按钮行不在滚动区里），都是拿当前显示器的工作区比，换分辨率
和缩放也成立。

顺带记下两个 walk 的坑：表单的 `SetMinMaxSize` 里 **max 参数是空操作**（walk 只写
`PtMinTrackSize`，从不写 `PtMaxTrackSize`），别指望它限制放大；`SetSize` 走的是
`WindowBase` 的方法、会绕过 `FormBase.SetBoundsPixels` 的布局下限夹紧，要摆窗口用
`SetBoundsPixels`。

## 界面状态存哪
高级选项的开合状态记在注册表 `HKCU\Software\GXU-Net-AutoLogin` 的 `AdvancedShown`（字符串 `"0"`/`"1"`），切换时立即写入。首次运行没有这个值 → 默认收起。

- **不写进 `.env`**：`.env` 是与命令行版共用的配置文件，「恢复默认配置」也不该把窗口开合一起重置；放注册表也与开机自启项的风格一致。
- 值名与字符串形式沿用同机上已有的写法（注册表里还看到 `CloseToTray`、`PlainSymbols`、`AutoScroll`、`StartMinimized`，来自另一份实现），读取时**同时兼容 DWORD**，免得被写成数字类型就读不出来。
- 注册表相关的用例（`tray/win_test.go`）默认跳过，用 `GXU_TEST_REGISTRY=1` 才跑；它们会先存下原值、跑完还原，不会动用户正在用的设置。

## 已知限制

- 托盘版只在 Windows 编译；`tray/main_other.go` 是给其它平台的桩，让 `go build ./...` 不至于报错。
- 界面为单窗口平铺布局，没有分页；日志区最多渲染 2000 行（与内存环形缓冲一致），更早的日志需要看日志文件（界面里有"打开日志文件"）。
- 密码仍按原样明文存 `.env`（与命令行版共用），没有做 DPAPI/凭据管理器加密。
