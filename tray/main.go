//go:build windows

// 广西大学校园网自动登陆程序（Windows 托盘版）。
//
// 与命令行版共用 internal/ 下的核心逻辑和同一份 .env：
//   - 常驻托盘，双击/左键点图标打开主界面
//   - 账号密码、运营商在界面里填一次即可
//   - 改完"保存并应用"立刻生效并发起认证，不用重启程序
//   - 高级选项默认收起：开机自启、关闭窗口最小化到托盘、启动不弹窗、
//     日志目录、路由器模式、自定义 MAC
//
// 线程模型：walk 的窗口与消息循环独占主线程（runtime.LockOSThread），
// 托盘图标挂在同一个窗口上，守护进程跑在自己的 goroutine 里。
package main

import (
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"sync"
	"time"

	"github.com/halfaradish/GXU-Net-AutoLogin/internal/config"
	"github.com/halfaradish/GXU-Net-AutoLogin/internal/daemon"
	"github.com/halfaradish/GXU-Net-AutoLogin/internal/logging"
	"github.com/lxn/walk"
	"github.com/lxn/win"
)

// version 由打包脚本通过 -ldflags "-X main.version=…" 注入
var version = "dev"

const appName = "GXU-Net-AutoLogin"

type app struct {
	exePath string
	dir     string // 程序目录：.env 与 logs/ 都相对它
	cfg     *config.Config
	log     *logging.Logger
	daemon  *daemon.Daemon
	ui      *ui
	ni      *walk.NotifyIcon
	hidden  *hiddenWindow
	icon    *walk.Icon
	logPath string
	loadErr error

	quittingMu sync.Mutex
	quitting   bool
}

func main() {
	// walk 的窗口与消息循环必须在同一条 OS 线程上
	runtime.LockOSThread()

	show := flag.Bool("show", false, "启动后直接显示主界面（忽略“启动后不弹窗”）")
	quitOther := flag.Bool("quit", false, "让正在运行的实例退出")
	flag.Parse()

	// 高分屏不糊；必须在建任何窗口之前
	setDPIAware()

	if *quitOther {
		// 没有正在运行的实例就什么也不用做，直接退出
		requestQuitExisting()
		return
	}

	// 单实例：已经在跑了就把那个实例的主界面唤出来
	if !acquireSingleInstance() {
		wakeExistingInstance()
		return
	}

	a, err := newApp()
	if err != nil {
		messageBox(appTitle, err.Error(), win.MB_OK|win.MB_ICONERROR)
		return
	}
	a.run(*show)
}

// newApp 准备好配置、日志、守护进程、托盘图标与主界面
func newApp() (*app, error) {
	exe, err := os.Executable()
	if err != nil {
		return nil, fmt.Errorf("无法确定程序路径：%v", err)
	}
	exeDir := filepath.Dir(exe)

	// 配置目录：启动时的工作目录里如果有 .env 就用它，否则用 exe 所在目录。
	// 这样"在程序目录里双击"或"从命令行带路径启动"都能直接用现成的 .env，
	// 而开机自启（工作目录是 System32，那里不会有 .env）自然落到 exe 目录。
	// .env 与 logs/ 都是相对路径，所以还要把工作目录切过去。
	dir := exeDir
	if cwd, err := os.Getwd(); err == nil {
		if _, statErr := os.Stat(config.Path(cwd)); statErr == nil {
			dir = cwd
		}
	}
	if err := os.Chdir(dir); err != nil {
		return nil, fmt.Errorf("无法切换到配置目录 %s：%v", dir, err)
	}

	a := &app{exePath: exe, dir: dir, icon: appIcon(exe)}

	cfg, err := config.Read(dir)
	if err != nil {
		// 还没配置（或读不动）：用默认值，界面上让用户填
		a.loadErr = err
		cfg = config.Defaults()
	}
	a.cfg = cfg

	// 托盘版没有控制台，日志一律落文件；LOG_FILE 决定放哪儿
	a.log = logging.New(logging.Options{})
	if abs, err := a.log.RetargetFile(cfg.LogPath); err != nil {
		a.log.Warn("%v", err)
	} else {
		a.logPath = abs
	}

	a.log.Info("广西大学校园网自动登陆程序（托盘版 %s）", version)
	if a.loadErr != nil {
		a.log.Warn("还没读到配置（%v），请在界面里填写账号密码", a.loadErr)
	} else {
		a.log.Info("配置文件加载成功！")
	}
	a.log.Info("用户: %s", cfg.User)
	a.log.Info("密码: ******（%d 字符）", len([]rune(cfg.Password)))
	a.log.Info("运营商: %s", cfg.NetType)
	if a.logPath != "" {
		a.log.Info("日志文件: %s（单文件上限 %d MiB，保留 %d 个备份）", a.logPath, a.log.MaxSizeMiB(), a.log.Backups())
	}

	// 开机自启动：每次启动按配置重写一遍，程序挪了位置也能自愈
	if cfg.User != "" {
		if err := applyAutostart(cfg.Autostart, exe); err != nil {
			a.log.Warn("%v", err)
		}
	}

	// 隐藏消息窗口：建在 UI 线程上，用来接收二次启动的唤醒与退出请求
	hw, err := newHiddenWindow(a.onHiddenMessage)
	if err != nil {
		return nil, err
	}
	a.hidden = hw

	// 先建守护进程（此时不启动），界面上"立即重连"等按钮直接引用它
	a.daemon = daemon.New(a.log, cfg, daemon.WithEventHandler(a.onDaemonEvent))

	// 主界面（同时承载消息循环与托盘图标）
	ui, err := newUI(a)
	if err != nil {
		return nil, err
	}
	a.ui = ui

	if err := a.setupTray(); err != nil {
		a.log.Warn("托盘图标创建失败：%v（主界面仍可正常使用）", err)
	}

	// 账号齐了才启动守护；没配就先躺着，界面里会提示
	if cfg.User != "" && cfg.Password != "" {
		if err := a.daemon.Start(); err != nil {
			a.log.Error("%v", err)
		}
	} else {
		a.log.Info("尚未配置账号密码，等待在界面中填写")
	}

	return a, nil
}

// setupTray 建托盘图标与右键菜单
func (a *app) setupTray() error {
	ni, err := walk.NewNotifyIcon(a.ui.mw)
	if err != nil {
		return err
	}
	a.ni = ni

	if err := ni.SetIcon(a.icon); err != nil {
		return err
	}
	if err := ni.SetToolTip(appTitle); err != nil {
		return err
	}

	// 左键点图标 = 打开主界面（比要求双击更好按）
	ni.MouseUp().Attach(func(x, y int, button walk.MouseButton) {
		if button == walk.LeftButton {
			a.ui.showWindow()
		}
	})

	addAction := func(text string, handler func()) error {
		act := walk.NewAction()
		if err := act.SetText(text); err != nil {
			return err
		}
		act.Triggered().Attach(handler)
		return ni.ContextMenu().Actions().Add(act)
	}

	if err := addAction("显示主界面", a.ui.showWindow); err != nil {
		return err
	}
	if err := addAction("立即重连", a.triggerReconnect); err != nil {
		return err
	}
	if err := ni.ContextMenu().Actions().Add(walk.NewSeparatorAction()); err != nil {
		return err
	}
	if err := addAction("打开日志文件", a.openLogFile); err != nil {
		return err
	}
	if err := addAction("打开日志目录", a.openLogDir); err != nil {
		return err
	}
	if err := ni.ContextMenu().Actions().Add(walk.NewSeparatorAction()); err != nil {
		return err
	}
	if err := addAction("退出", a.quit); err != nil {
		return err
	}

	// 托盘图标默认不可见，要显式打开
	if err := ni.SetVisible(true); err != nil {
		return err
	}
	a.log.Info("托盘图标已就绪")
	return nil
}

func (a *app) run(show bool) {
	defer a.shutdown()

	// 定时刷新：窗口就绪后开始，每秒把状态与日志刷到界面上。
	// mw.Synchronize 是异步投递，函数由 walk 的消息循环在 UI 线程上执行。
	a.ui.mw.Starting().Attach(func() {
		go a.refreshLoop()
	})

	if show || a.firstRun() || !a.cfg.StartMinimized {
		a.ui.showWindow()
	}

	a.ui.mw.Run() // 阻塞到窗口被真正关闭（最小化到托盘不会走到这里）
}

// refreshLoop 每秒刷新一次界面与托盘提示
func (a *app) refreshLoop() {
	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()

	for range ticker.C {
		if a.isQuitting() {
			return
		}
		a.ui.mw.Synchronize(a.ui.refresh)
	}
}

// firstRun 判断是不是"还没有可用配置"的首次运行
func (a *app) firstRun() bool {
	return a.loadErr != nil || a.cfg.User == "" || a.cfg.Password == ""
}

func (a *app) shutdown() {
	if a.ni != nil {
		a.ni.SetVisible(false)
		a.ni.Dispose()
	}
	a.daemon.Stop()
	a.log.Info("已退出")
	a.hidden.destroy()
	a.log.Close()
}

// applyConfig 把界面上的新配置落到磁盘、注册表与守护进程（不重启程序）
func (a *app) applyConfig(cfg *config.Config) {
	a.log.Info("应用新配置：用户=%s 运营商=%s 路由器=%s MAC=%s",
		cfg.User, dash(cfg.NetType), routerDesc(cfg), dash(cfg.MacAddress))

	if err := applyAutostart(cfg.Autostart, a.exePath); err != nil {
		a.log.Warn("%v", err)
	}

	// 日志目录可能变了
	if abs, err := a.log.RetargetFile(cfg.LogPath); err != nil {
		a.log.Warn("%v（日志仍写在原处）", err)
	} else {
		if abs != a.logPath {
			a.log.Info("日志文件: %s", abs)
		}
		a.logPath = abs
	}

	a.cfg = cfg
	if err := a.daemon.Apply(cfg); err != nil {
		a.log.Error("%v", err)
		messageBox(appTitle, err.Error(), win.MB_OK|win.MB_ICONERROR)
	}
}

func routerDesc(cfg *config.Config) string {
	if cfg.RouterIP == "" && cfg.RouterMAC == "" {
		return "未启用"
	}
	return fmt.Sprintf("IP=%s MAC=%s", cfg.RouterIP, cfg.RouterMAC)
}

// onHiddenMessage 处理二次启动的唤醒与 -quit 请求（运行在 UI 线程）
func (a *app) onHiddenMessage(msg uint32) {
	switch msg {
	case wmShowWindow:
		a.ui.showWindow()
	case wmQuitRequest:
		a.quit()
	}
}

// onDaemonEvent 把状态切换弹成气泡提示。这个回调来自守护进程的 goroutine，
// 所以走 Synchronize 排到 UI 线程，避免和界面同时操作同一个 NotifyIcon。
func (a *app) onDaemonEvent(ev daemon.Event) {
	if a.isQuitting() {
		return
	}
	a.ui.mw.Synchronize(func() {
		if a.ni == nil {
			return
		}
		switch ev.Kind {
		case daemon.EventDown:
			a.ni.ShowWarning("校园网已断线", ev.Text+"，正在自动重连…")
		case daemon.EventRecovered:
			a.ni.ShowInfo("校园网已恢复", ev.Text)
		case daemon.EventQuiet:
			a.ni.ShowInfo("进入静默期", ev.Text+"，之后每 5 分钟尝试一次登录")
		}
	})
}

// quit 真正退出程序（关闭窗口不算，除非用户取消勾选了"最小化至托盘"）
func (a *app) quit() {
	a.setQuitting(true)
	a.ui.mw.Close()
}

// triggerReconnect 立即认证一次；守护进程还没建好时忽略
func (a *app) triggerReconnect() {
	if a.daemon != nil {
		a.daemon.Trigger()
	}
}

// setToolTip 更新托盘的鼠标悬停提示
func (a *app) setToolTip(tip string) {
	if a.ni != nil {
		a.ni.SetToolTip(tip)
	}
}

// balloon 弹一个信息气泡（要在 UI 线程上调用）
func (a *app) balloon(title, text string) {
	if a.ni != nil {
		a.ni.ShowInfo(title, text)
	}
}

func (a *app) isQuitting() bool {
	a.quittingMu.Lock()
	defer a.quittingMu.Unlock()
	return a.quitting
}

func (a *app) setQuitting(v bool) {
	a.quittingMu.Lock()
	a.quitting = v
	a.quittingMu.Unlock()
}

// openLogFile 用系统默认程序打开日志文件
func (a *app) openLogFile() {
	if a.logPath == "" {
		messageBox(appTitle, "当前没有启用日志文件。", win.MB_OK|win.MB_ICONINFORMATION)
		return
	}
	if err := shellOpen(a.logPath); err != nil {
		messageBox(appTitle, err.Error(), win.MB_OK|win.MB_ICONERROR)
	}
}

// openLogDir 在资源管理器里定位日志目录
func (a *app) openLogDir() {
	dir := filepath.Dir(a.logPath)
	if a.logPath == "" {
		dir = filepath.Join(a.dir, logging.DefaultDir)
	}
	if err := shellOpen(dir); err != nil {
		messageBox(appTitle, err.Error(), win.MB_OK|win.MB_ICONERROR)
	}
}
