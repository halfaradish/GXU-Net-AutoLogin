//go:build windows

// 主界面：基础配置 + 可展开的高级选项 + 运行状态 + 内置日志。
//
// 所有控件操作都发生在 UI 线程上：托盘菜单的回调由 walk 的消息循环派发（同一个
// 线程），定时刷新则通过 mw.Synchronize 排队到该线程执行。
package main

import (
	"fmt"
	"path/filepath"
	"strings"
	"time"

	"github.com/halfaradish/GXU-Net-AutoLogin/internal/config"
	"github.com/halfaradish/GXU-Net-AutoLogin/internal/daemon"
	"github.com/halfaradish/GXU-Net-AutoLogin/internal/logging"
	"github.com/halfaradish/GXU-Net-AutoLogin/internal/netauth"
	"github.com/lxn/walk"
	"github.com/lxn/win"
)

const appTitle = "广西大学校园网自动登录（托盘版）"

// 界面上一次最多渲染多少行日志（与内存里的环形缓冲一致）
const maxLogLines = logging.DefaultRingLines

// 运营商下拉项：显示名与配置取值一一对应
var netTypeOptions = []struct {
	Label string
	Value string
}{
	{"校园网（默认）", ""},
	{"电信 telecom", "telecom"},
	{"联通 unicom", "unicom"},
	{"移动 cmcc", "cmcc"},
}

const (
	macAutoOption   = "自动（用认证 IP 所在网卡）"
	macManualOption = "手动输入"
)

var logLevelOptions = []string{"全部", "WARN 及以上", "ERROR"}

type ui struct {
	app *app
	mw  *walk.MainWindow

	// 内容区（除了底部按钮行都在里面）。窗口的高度由它兜底：内容放不下就出
	// 滚动条，而不是把窗口顶大 —— 见 newUI 里的说明。
	scroll *walk.ScrollView

	// 基础配置
	userEdit *walk.LineEdit
	passEdit *walk.LineEdit
	netCombo *walk.ComboBox

	// 高级配置
	advanced      *walk.Composite
	advBtn        *walk.PushButton
	autostartChk  *walk.CheckBox
	minToTrayChk  *walk.CheckBox
	startHideChk  *walk.CheckBox
	logDirEdit    *walk.LineEdit
	routerIPEdit  *walk.LineEdit
	routerMACEdit *walk.LineEdit
	macCombo      *walk.ComboBox
	macEdit       *walk.LineEdit
	adapters      []netauth.Adapter

	// 运行状态
	stateVal  *walk.Label
	detailVal *walk.Label
	ipVal     *walk.Label
	macVal    *walk.Label
	routerVal *walk.Label
	loginVal  *walk.Label
	statsVal  *walk.Label

	// 日志
	levelCombo  *walk.ComboBox
	filterEdit  *walk.LineEdit
	pauseChk    *walk.CheckBox
	logView     *walk.TextEdit
	logSeen     uint64 // logger 累计计数里已渲染到界面的位置
	logRendered int    // 界面上现有的行数

	lastTip string // 上一次写进托盘提示的文字，避免反复调 Shell_NotifyIcon

	onQuit func()
}

// ── 小工具 ────────────────────────────────────────────────

func hboxRow(parent walk.Container) (*walk.Composite, error) {
	c, err := walk.NewComposite(parent)
	if err != nil {
		return nil, err
	}
	if err := c.SetLayout(walk.NewHBoxLayout()); err != nil {
		return nil, err
	}
	// 只去掉左右外边距：这样行里的标签与分组里直接摆放的控件（比如高级选项的
	// 复选框）从同一条竖线开始；上下留着，行的疏密才不至于挤在一起。
	if err := c.Layout().SetMargins(walk.Margins{HNear: 0, VNear: 9, HFar: 0, VFar: 9}); err != nil {
		return nil, err
	}
	return c, nil
}

// rowLabelWidth 是"标签 + 值"一行里标签列的固定宽度（1/96 英寸单位）
const rowLabelWidth = 68

// labeledRow 建一行"定宽标签 + 后续控件"，返回值那行容器，调用方往里加控件
func labeledRow(parent walk.Container, text string, width int) (*walk.Composite, error) {
	r, err := hboxRow(parent)
	if err != nil {
		return nil, err
	}
	lbl, err := walk.NewTextLabel(r)
	if err != nil {
		return nil, err
	}
	lbl.SetText(text)
	// 最大宽度也设成同一个值。walk 只把"非左对齐"的静态文本当作可伸展，
	// 但只要一行里还剩下宽度，它就会把标签一起撑开，把值挤到行中间去，
	// 各行的值也就对不齐了。
	lbl.SetMinMaxSize(walk.Size{Width: width, Height: 20}, walk.Size{Width: width})
	return r, nil
}

// valueLabel 建一个显示状态值的标签：靠左对齐，过长时截成省略号（悬停看全文）
func valueLabel(parent walk.Container) (*walk.Label, error) {
	lbl, err := walk.NewLabel(parent)
	if err != nil {
		return nil, err
	}
	// 带省略号模式的 Label 才会收缩，窗口窄了不会把整行的最小宽度顶大
	if err := lbl.SetEllipsisMode(walk.EllipsisEnd); err != nil {
		return nil, err
	}
	return lbl, nil
}

// statusRow 建一行"标签 + 值"。值后面补一个占位控件吃掉剩余宽度，
// 值才会紧贴标签左侧，而不是被 walk 按剩余宽度推到行中间。
func statusRow(parent walk.Container, label string, width int) (*walk.Label, error) {
	r, err := labeledRow(parent, label, width)
	if err != nil {
		return nil, err
	}
	val, err := valueLabel(r)
	if err != nil {
		return nil, err
	}
	if _, err := walk.NewHSpacer(r); err != nil {
		return nil, err
	}
	return val, nil
}

func newGroup(parent walk.Container, title string) (*walk.GroupBox, error) {
	gb, err := walk.NewGroupBox(parent)
	if err != nil {
		return nil, err
	}
	if err := gb.SetTitle(title); err != nil {
		return nil, err
	}
	if err := gb.SetLayout(walk.NewVBoxLayout()); err != nil {
		return nil, err
	}
	return gb, nil
}

func dash(s string) string {
	if s == "" {
		return "—"
	}
	return s
}

// shortDuration 把时长写成人话
func shortDuration(d time.Duration) string {
	if d < 0 {
		d = 0
	}
	switch {
	case d < time.Minute:
		return fmt.Sprintf("%d 秒", int(d.Seconds()))
	case d < time.Hour:
		return fmt.Sprintf("%d 分 %d 秒", int(d.Minutes()), int(d.Seconds())%60)
	default:
		return fmt.Sprintf("%d 小时 %d 分", int(d.Hours()), int(d.Minutes())%60)
	}
}

func netTypeIndex(value string) int {
	for i, o := range netTypeOptions {
		if o.Value == value {
			return i
		}
	}
	return 0
}

// logDirOf 把配置里的 LOG_FILE 折算成界面上的"日志目录"
func logDirOf(baseDir, logFile string) string {
	if strings.TrimSpace(logFile) == "" {
		return filepath.Join(baseDir, logging.DefaultDir)
	}
	return filepath.Dir(logFile)
}

// ── 构建界面 ──────────────────────────────────────────────

// 窗口尺寸（1/96 英寸单位）。高度这里写得比内容需要的大一点，实际会被显示器
// 工作区夹紧：屏幕够高就一次看全（高级选项展开也放得下），不够高就出滚动条。
const (
	minWindowWidth  = 620 // 再窄内容就挤不下了（日志那行的过滤控件决定的）
	minWindowHeight = 420 // 故意留小：小屏也得能拖到装得下
	windowMargin    = 12  // 离屏幕边缘留一点，别贴着任务栏

	defaultWindowWidth  = 760
	defaultWindowHeight = 1100 // 收起 ~900、展开高级选项 ~1080（100% 缩放下实测），取够放下两者
)

// applyStartupBounds 把窗口摆进当前显示器的工作区并居中。
//
// 不能只靠 SetSize：屏幕逻辑高度可能比内容还矮（本机 2560x1600 @175%，可用高度
// 只有 ~866 逻辑px），那样子窗口底部会被顶出屏幕，按钮点不到。
func (u *ui) applyStartupBounds() {
	wa, err := workArea(u.mw.Handle())
	if err != nil {
		u.app.log.Warn("%v", err)
		return
	}

	margin := u.mw.IntFrom96DPI(windowMargin)
	availW := int(wa.Right-wa.Left) - 2*margin
	availH := int(wa.Bottom-wa.Top) - 2*margin

	size := u.mw.SizeFrom96DPI(walk.Size{Width: defaultWindowWidth, Height: defaultWindowHeight})
	w, h := size.Width, size.Height
	if w > availW {
		w = availW
	}
	if h > availH {
		h = availH
	}

	bounds := walk.Rectangle{
		X:      int(wa.Left) + (int(wa.Right-wa.Left)-w)/2,
		Y:      int(wa.Top) + (int(wa.Bottom-wa.Top)-h)/2,
		Width:  w,
		Height: h,
	}
	if err := u.mw.SetBoundsPixels(bounds); err != nil {
		u.app.log.Warn("设置窗口位置失败：%v", err)
	}
}

func newUI(a *app) (*ui, error) {
	mw, err := walk.NewMainWindow()
	if err != nil {
		return nil, err
	}
	u := &ui{app: a, mw: mw}
	u.onQuit = a.quit

	if err := mw.SetTitle(appTitle); err != nil {
		return nil, err
	}
	// 与托盘共用同一个从 exe 抠出来的图标：SetIcon 发的是 WM_SETICON，
	// 它压过 exe 自带的图标资源，任务栏、Alt+Tab 与标题栏都看这个
	if err := mw.SetIcon(a.icon); err != nil {
		a.log.Warn("设置窗口图标失败：%v", err)
	}
	if err := mw.SetLayout(walk.NewVBoxLayout()); err != nil {
		return nil, err
	}
	mw.SetMinMaxSize(walk.Size{Width: minWindowWidth, Height: minWindowHeight}, walk.Size{})

	// 内容区放进可滚动的 ScrollView，只有底部按钮行留在窗口上。
	//
	// 这一步是为了窗口的下限：walk 用 max(内容布局的最小尺寸, 上面声明的最小尺寸)
	// 当窗口的最小可拖拽尺寸，而内容累加起来能到 800 逻辑px 以上（高级选项展开时
	// 1100+）。在高缩放的小屏上（本机 2560x1600 @175%，逻辑可用高度只有 ~866px）
	// 就会：窗口拖不小、底部按钮被顶到屏幕外、最大化时标题栏被顶到屏幕上方之外。
	// ScrollView 的最小高度是 0（walk/scrollview.go），窗口的下限从此只由声明值
	// 决定；内容装得下时滚动条不出现，看上去和以前一样。
	if u.scroll, err = walk.NewScrollView(mw); err != nil {
		return nil, err
	}
	u.scroll.SetScrollbars(false, true) // 横向仍按内容最小宽度，不加横向滚动条
	if err := u.scroll.SetLayout(walk.NewVBoxLayout()); err != nil {
		return nil, err
	}

	if err := u.buildBasic(); err != nil {
		return nil, err
	}
	if err := u.buildStatus(); err != nil {
		return nil, err
	}
	logGroup, err := u.buildLog()
	if err != nil {
		return nil, err
	}
	if err := u.buildAdvanced(); err != nil {
		return nil, err
	}
	if err := u.buildButtons(); err != nil {
		return nil, err
	}

	// 日志区吸收多余高度
	if bl, ok := u.scroll.Layout().(*walk.BoxLayout); ok {
		bl.SetStretchFactor(logGroup, 1)
	}

	u.applyStartupBounds()

	// 关闭窗口：按界面上的勾选决定"最小化到托盘"还是"退出"
	mw.Closing().Attach(func(canceled *bool, reason walk.CloseReason) {
		if a.isQuitting() {
			return
		}
		if u.minToTrayChk.Checked() {
			*canceled = true
			mw.Hide()
			// 进托盘也要提醒一次：窗口刚消失，用户需要知道程序还在跑
			a.balloon(appTitle, "程序仍在后台运行，左键点托盘图标可以重新打开。")
			return
		}
		a.setQuitting(true)
	})

	u.loadFromConfig(a.cfg)

	// 高级选项沿用上次的开合状态；首次运行（没有记录）保持收起
	if expanded, err := loadAdvancedShown(); err != nil {
		a.log.Warn("读取界面状态失败：%v", err)
	} else if expanded {
		u.setAdvanced(true)
	}

	return u, nil
}

func (u *ui) buildBasic() error {
	gb, err := newGroup(u.scroll, "校园网账号")
	if err != nil {
		return err
	}

	if r, err := labeledRow(gb, "账号", rowLabelWidth); err != nil {
		return err
	} else if u.userEdit, err = walk.NewLineEdit(r); err != nil {
		return err
	}
	u.userEdit.SetCueBanner("学号 / 工号")

	if r, err := labeledRow(gb, "密码", rowLabelWidth); err != nil {
		return err
	} else if u.passEdit, err = walk.NewLineEdit(r); err != nil {
		return err
	}
	u.passEdit.SetPasswordMode(true)

	if r, err := labeledRow(gb, "运营商", rowLabelWidth); err != nil {
		return err
	} else if u.netCombo, err = walk.NewComboBox(r); err != nil {
		return err
	}
	labels := make([]string, len(netTypeOptions))
	for i, o := range netTypeOptions {
		labels[i] = o.Label
	}
	u.netCombo.SetModel(labels)
	u.netCombo.SetMinMaxSize(walk.Size{Width: 200}, walk.Size{})
	u.netCombo.SetToolTipText("留空使用校园网；用运营商账号时选对应的一项")

	return nil
}

func (u *ui) buildStatus() error {
	gb, err := newGroup(u.scroll, "运行状态")
	if err != nil {
		return err
	}

	if u.stateVal, err = statusRow(gb, "连接状态", rowLabelWidth); err != nil {
		return err
	}
	if r, err := labeledRow(gb, "详细", rowLabelWidth); err != nil {
		return err
	} else {
		if u.detailVal, err = valueLabel(r); err != nil {
			return err
		}
		if _, err := walk.NewHSpacer(r); err != nil {
			return err
		}
	}

	if u.ipVal, err = statusRow(gb, "认证地址", rowLabelWidth); err != nil {
		return err
	}
	if u.macVal, err = statusRow(gb, "认证 MAC", rowLabelWidth); err != nil {
		return err
	}
	if u.routerVal, err = statusRow(gb, "路由器", rowLabelWidth); err != nil {
		return err
	}
	if u.loginVal, err = statusRow(gb, "最近认证", rowLabelWidth); err != nil {
		return err
	}
	if u.statsVal, err = statusRow(gb, "运行统计", rowLabelWidth); err != nil {
		return err
	}

	return nil
}

func (u *ui) buildLog() (*walk.GroupBox, error) {
	gb, err := newGroup(u.scroll, "日志")
	if err != nil {
		return nil, err
	}

	bar, err := hboxRow(gb)
	if err != nil {
		return nil, err
	}
	if lbl, err := walk.NewTextLabel(bar); err != nil {
		return nil, err
	} else {
		lbl.SetText("级别")
	}
	if u.levelCombo, err = walk.NewComboBox(bar); err != nil {
		return nil, err
	}
	u.levelCombo.SetModel(logLevelOptions)
	u.levelCombo.SetCurrentIndex(0)
	u.levelCombo.SetMinMaxSize(walk.Size{Width: 110}, walk.Size{})

	if lbl, err := walk.NewTextLabel(bar); err != nil {
		return nil, err
	} else {
		lbl.SetText("关键字")
	}
	if u.filterEdit, err = walk.NewLineEdit(bar); err != nil {
		return nil, err
	}
	u.filterEdit.SetMinMaxSize(walk.Size{Width: 120}, walk.Size{})
	u.filterEdit.SetCueBanner("按回车过滤")

	if u.pauseChk, err = walk.NewCheckBox(bar); err != nil {
		return nil, err
	}
	u.pauseChk.SetText("暂停滚动")

	if _, err := walk.NewHSpacer(bar); err != nil {
		return nil, err
	}

	openFile, err := walk.NewPushButton(bar)
	if err != nil {
		return nil, err
	}
	openFile.SetText("打开日志文件")
	openFile.Clicked().Attach(u.app.openLogFile)

	openDir, err := walk.NewPushButton(bar)
	if err != nil {
		return nil, err
	}
	openDir.SetText("打开目录")
	openDir.Clicked().Attach(u.app.openLogDir)

	clear, err := walk.NewPushButton(bar)
	if err != nil {
		return nil, err
	}
	clear.SetText("清空日志")
	clear.Clicked().Attach(u.onClearLog)

	if u.logView, err = walk.NewTextEditWithStyle(gb, win.WS_VSCROLL|win.ES_AUTOVSCROLL); err != nil {
		return nil, err
	}
	u.logView.SetReadOnly(true)
	// 最小高度压到 100：日志区是内容里最"软"的一块，它小一点，整份内容在高缩放
	// 的小屏上（本机 175% 缩放，可用高度只有 ~866 逻辑px）才不用滚动就能看全
	u.logView.SetMinMaxSize(walk.Size{Width: 400, Height: 100}, walk.Size{})

	u.levelCombo.CurrentIndexChanged().Attach(u.renderAllLog)
	u.filterEdit.EditingFinished().Attach(u.renderAllLog)

	return gb, nil
}

func (u *ui) buildAdvanced() error {
	// 高级选项整块放在一个 Composite 里，默认收起（首次运行就是收起的；
	// 之后的开合状态由 setAdvanced 记在注册表里）。walk 的布局会跳过不可见控件，
	// 所以展开/收起只影响布局，不用改窗口大小。
	box, err := walk.NewComposite(u.scroll)
	if err != nil {
		return err
	}
	if err := box.SetLayout(walk.NewVBoxLayout()); err != nil {
		return err
	}
	box.SetVisible(false)
	u.advanced = box

	gb, err := newGroup(box, "高级选项")
	if err != nil {
		return err
	}

	if u.autostartChk, err = walk.NewCheckBox(gb); err != nil {
		return err
	}
	u.autostartChk.SetText("开机自启动（写入当前用户的启动项）")
	u.leftAlign(u.autostartChk)

	if u.minToTrayChk, err = walk.NewCheckBox(gb); err != nil {
		return err
	}
	u.minToTrayChk.SetText("关闭窗口时最小化至托盘")
	u.leftAlign(u.minToTrayChk)

	if u.startHideChk, err = walk.NewCheckBox(gb); err != nil {
		return err
	}
	u.startHideChk.SetText("启动后不显示主界面（驻留托盘）")
	u.leftAlign(u.startHideChk)

	if r, err := labeledRow(gb, "日志目录", rowLabelWidth); err != nil {
		return err
	} else {
		if u.logDirEdit, err = walk.NewLineEdit(r); err != nil {
			return err
		}
		u.logDirEdit.SetMinMaxSize(walk.Size{Width: 300}, walk.Size{})
		browse, err := walk.NewPushButton(r)
		if err != nil {
			return err
		}
		browse.SetText("浏览…")
		browse.Clicked().Attach(u.onBrowseLogDir)
	}

	if r, err := labeledRow(gb, "路由器 IP", rowLabelWidth); err != nil {
		return err
	} else if u.routerIPEdit, err = walk.NewLineEdit(r); err != nil {
		return err
	}
	u.routerIPEdit.SetCueBanner("留空 = 使用本机 IP")

	if r, err := labeledRow(gb, "路由器 MAC", rowLabelWidth); err != nil {
		return err
	} else if u.routerMACEdit, err = walk.NewLineEdit(r); err != nil {
		return err
	}
	u.routerMACEdit.SetCueBanner("与路由器 IP 同时填写才生效")

	if r, err := labeledRow(gb, "MAC 地址", rowLabelWidth); err != nil {
		return err
	} else {
		if u.macCombo, err = walk.NewComboBox(r); err != nil {
			return err
		}
		u.macCombo.SetMinMaxSize(walk.Size{Width: 240}, walk.Size{})
		if u.macEdit, err = walk.NewLineEdit(r); err != nil {
			return err
		}
		u.macEdit.SetMinMaxSize(walk.Size{Width: 170}, walk.Size{})
		u.macCombo.CurrentIndexChanged().Attach(u.onMACOptionChanged)
	}

	return nil
}

func (u *ui) buildButtons() error {
	row, err := hboxRow(u.mw)
	if err != nil {
		return err
	}

	save, err := walk.NewPushButton(row)
	if err != nil {
		return err
	}
	save.SetText("保存并应用")
	save.Clicked().Attach(u.onSave)

	reconnect, err := walk.NewPushButton(row)
	if err != nil {
		return err
	}
	reconnect.SetText("立即重连")
	reconnect.Clicked().Attach(u.app.triggerReconnect)

	if u.advBtn, err = walk.NewPushButton(row); err != nil {
		return err
	}
	u.advBtn.SetText("高级选项 ▾")
	u.advBtn.Clicked().Attach(u.toggleAdvanced)

	if _, err := walk.NewHSpacer(row); err != nil {
		return err
	}

	reset, err := walk.NewPushButton(row)
	if err != nil {
		return err
	}
	reset.SetText("恢复默认配置")
	reset.Clicked().Attach(u.onReset)

	quit, err := walk.NewPushButton(row)
	if err != nil {
		return err
	}
	quit.SetText("退出")
	quit.Clicked().Attach(func() { u.onQuit() })

	return nil
}

// ── 界面与配置之间的同步 ──────────────────────────────────

// loadFromConfig 把配置灌进控件（启动时调用一次）
func (u *ui) loadFromConfig(cfg *config.Config) {
	u.userEdit.SetText(cfg.User)
	u.passEdit.SetText(cfg.Password)
	u.netCombo.SetCurrentIndex(netTypeIndex(cfg.NetType))
	u.autostartChk.SetChecked(cfg.Autostart)
	u.minToTrayChk.SetChecked(cfg.MinimizeToTray)
	u.startHideChk.SetChecked(cfg.StartMinimized)
	u.logDirEdit.SetText(logDirOf(u.app.dir, cfg.LogPath))
	u.routerIPEdit.SetText(cfg.RouterIP)
	u.routerMACEdit.SetText(cfg.RouterMAC)
	u.loadAdapters(cfg.MacAddress)
}

// loadAdapters 列出本机网卡，供"MAC 地址"下拉选择
func (u *ui) loadAdapters(current string) {
	u.adapters = netauth.ListAdapters()

	items := make([]string, 0, len(u.adapters)+2)
	items = append(items, macAutoOption)
	for _, ad := range u.adapters {
		items = append(items, fmt.Sprintf("%s  %s  %s", ad.Name, ad.IP, ad.MAC))
	}
	items = append(items, macManualOption)
	u.macCombo.SetModel(items)

	switch cur := strings.ToLower(strings.TrimSpace(current)); {
	case cur == "":
		u.macCombo.SetCurrentIndex(0)
	default:
		idx := len(u.adapters) + 1 // 手动输入
		for i, ad := range u.adapters {
			if strings.EqualFold(ad.MAC, cur) {
				idx = i + 1
				break
			}
		}
		u.macCombo.SetCurrentIndex(idx)
		u.macEdit.SetText(current)
	}
	u.onMACOptionChanged()
}

// onMACOptionChanged 选了网卡就把 MAC 填进去，选"自动"就清空
func (u *ui) onMACOptionChanged() {
	idx := u.macCombo.CurrentIndex()
	switch {
	case idx <= 0:
		u.macEdit.SetText("")
		u.macEdit.SetEnabled(false)
	case idx <= len(u.adapters):
		u.macEdit.SetText(u.adapters[idx-1].MAC)
		u.macEdit.SetEnabled(true)
	default:
		u.macEdit.SetEnabled(true)
	}
}

// collect 把控件里的内容收成一份配置，并做校验
func (u *ui) collect() (*config.Config, error) {
	cfg := *u.app.cfg // 保留配置文件里其它字段

	cfg.User = strings.TrimSpace(u.userEdit.Text())
	cfg.Password = u.passEdit.Text()
	if cfg.User == "" || cfg.Password == "" {
		return nil, fmt.Errorf("请填写校园网账号和密码")
	}
	cfg.NetType = netTypeOptions[u.netCombo.CurrentIndex()].Value

	cfg.Autostart = u.autostartChk.Checked()
	cfg.MinimizeToTray = u.minToTrayChk.Checked()
	cfg.StartMinimized = u.startHideChk.Checked()

	cfg.RouterIP = strings.TrimSpace(u.routerIPEdit.Text())
	cfg.RouterMAC = strings.TrimSpace(u.routerMACEdit.Text())
	if (cfg.RouterIP == "") != (cfg.RouterMAC == "") {
		return nil, fmt.Errorf("路由器 IP 与 MAC 必须同时填写，只填一个不会生效")
	}
	if cfg.RouterMAC != "" {
		mac, err := netauth.NormalizeMAC(cfg.RouterMAC)
		if err != nil {
			return nil, err
		}
		cfg.RouterMAC = mac
	}

	switch idx := u.macCombo.CurrentIndex(); {
	case idx <= 0:
		cfg.MacAddress = ""
	case idx <= len(u.adapters):
		cfg.MacAddress = u.adapters[idx-1].MAC
	default:
		v := strings.TrimSpace(u.macEdit.Text())
		if v == "" {
			return nil, fmt.Errorf("选择“%s”时请填写 MAC 地址", macManualOption)
		}
		mac, err := netauth.NormalizeMAC(v)
		if err != nil {
			return nil, err
		}
		cfg.MacAddress = mac
	}

	dir := strings.TrimSpace(u.logDirEdit.Text())
	if dir == "" {
		dir = filepath.Join(u.app.dir, logging.DefaultDir)
	}
	cfg.LogPath = filepath.Join(dir, logging.DefaultFileName)
	// 托盘版没有控制台，日志一律写文件
	cfg.LogToFile = true
	return &cfg, nil
}

// ── 事件处理 ──────────────────────────────────────────────

func (u *ui) toggleAdvanced() {
	u.setAdvanced(!u.advanced.Visible())
}

// setAdvanced 展开/收起高级选项，并把状态记下来（下次启动沿用）
func (u *ui) setAdvanced(show bool) {
	u.advanced.SetVisible(show)
	if show {
		u.advBtn.SetText("高级选项 ▴")
	} else {
		u.advBtn.SetText("高级选项 ▾")
	}

	if err := saveAdvancedShown(show); err != nil {
		u.app.log.Warn("记住界面状态失败：%v", err)
	}
}

// leftAlign 把控件靠左对齐。walk 的纵向布局对"默认对齐"的控件会在水平方向居中，
// 而按钮类型（复选框就是）的 init 并不设置对齐，于是复选框会跑到中间去。
func (u *ui) leftAlign(w walk.Widget) {
	if err := w.SetAlignment(walk.AlignHNearVCenter); err != nil {
		u.app.log.Warn("设置控件对齐失败：%v", err)
	}
}

func (u *ui) onBrowseLogDir() {
	dlg := new(walk.FileDialog)
	dlg.Title = "选择日志存放目录"
	if _, err := dlg.ShowBrowseFolder(u.mw); err != nil {
		messageBox(appTitle, "打开目录选择框失败："+err.Error(), win.MB_OK|win.MB_ICONERROR)
		return
	}
	if dlg.FilePath != "" {
		u.logDirEdit.SetText(dlg.FilePath)
	}
}

func (u *ui) onSave() {
	cfg, err := u.collect()
	if err != nil {
		messageBox(appTitle, err.Error(), win.MB_OK|win.MB_ICONERROR)
		return
	}
	if err := config.Save(u.app.dir, cfg); err != nil {
		messageBox(appTitle, "保存配置失败："+err.Error(), win.MB_OK|win.MB_ICONERROR)
		return
	}
	u.app.applyConfig(cfg)
	u.renderAllLog()
	messageBox(appTitle, "配置已保存并应用，已按新配置立即发起一次认证。\n（无需重启程序）", win.MB_OK|win.MB_ICONINFORMATION)
}

func (u *ui) onReset() {
	if messageBox(appTitle, "把高级选项恢复为默认值？\n\n账号、密码、运营商不会被改动。",
		win.MB_YESNO|win.MB_ICONQUESTION) != win.IDYES {
		return
	}

	d := config.Defaults()
	u.autostartChk.SetChecked(d.Autostart)
	u.minToTrayChk.SetChecked(d.MinimizeToTray)
	u.startHideChk.SetChecked(d.StartMinimized)
	u.logDirEdit.SetText(filepath.Join(u.app.dir, logging.DefaultDir))
	u.routerIPEdit.SetText("")
	u.routerMACEdit.SetText("")
	u.macCombo.SetCurrentIndex(0)
	u.loadAdapters("")

	u.onSave() // 恢复默认值也要立刻生效
}

func (u *ui) onClearLog() {
	if messageBox(appTitle, "清空当前日志文件与轮转备份？", win.MB_YESNO|win.MB_ICONQUESTION) != win.IDYES {
		return
	}
	if err := u.app.log.ClearFile(); err != nil {
		messageBox(appTitle, "清空日志失败："+err.Error(), win.MB_OK|win.MB_ICONERROR)
		return
	}
	u.logView.SetText("")
	u.logSeen, u.logRendered = 0, 0
}

// ── 定时刷新（都由 mw.Synchronize 排到 UI 线程执行）──────────

func (u *ui) refresh() {
	u.refreshStatus()
	u.refreshLog()
}

func (u *ui) refreshStatus() {
	st := u.app.daemon.Status()

	u.stateVal.SetText(st.State.Text())
	switch st.State {
	case daemon.StateOnline:
		u.stateVal.SetTextColor(walk.RGB(0, 128, 0))
	case daemon.StateDown, daemon.StateFailed:
		u.stateVal.SetTextColor(walk.RGB(192, 0, 0))
	case daemon.StateQuiet:
		u.stateVal.SetTextColor(walk.RGB(160, 120, 0))
	default:
		u.stateVal.SetTextColor(walk.RGB(0, 0, 0))
	}

	detail := st.Detail
	if detail == "" {
		if u.app.cfg.User == "" {
			detail = "尚未配置账号"
		} else {
			detail = "—"
		}
	}
	if st.State == daemon.StateAuthing {
		detail = "正在向门户发起认证…"
	}
	u.detailVal.SetText(detail)

	u.ipVal.SetText(dash(st.Identity.IP))
	mac := dash(st.Identity.MAC)
	if st.Identity.Source != "" {
		mac += "（" + st.Identity.Source + "）"
	}
	u.macVal.SetText(mac)

	if st.RouterMode {
		u.routerVal.SetText(fmt.Sprintf("已启用：IP=%s  MAC=%s", st.RouterIP, st.RouterMAC))
	} else {
		u.routerVal.SetText("未启用（使用本机 IP 与 MAC）")
	}

	if st.LastLoginAt.IsZero() {
		u.loginVal.SetText("尚无记录")
	} else {
		u.loginVal.SetText(st.LastLoginAt.Format("2006-01-02 15:04:05") + "  " + st.LastLoginDesc)
	}

	var parts []string
	if !st.OnlineSince.IsZero() {
		parts = append(parts, "本次在线 "+shortDuration(time.Since(st.OnlineSince)))
	}
	if !st.OutageSince.IsZero() {
		parts = append(parts, "本次断网 "+shortDuration(time.Since(st.OutageSince)))
	}
	if st.ConsecutiveFails > 0 {
		parts = append(parts, fmt.Sprintf("连续失败 %d 次", st.ConsecutiveFails))
	}
	parts = append(parts, fmt.Sprintf("累计断网 %d 次", st.OutageCount))
	interval := "5 秒"
	if st.Quiet {
		interval = "60 秒"
	} else if st.Fast {
		interval = "1 秒"
	}
	parts = append(parts, "探测间隔 "+interval)
	if !st.NextLoginAt.IsZero() && st.NextLoginAt.After(time.Now()) {
		parts = append(parts, "下次登录 "+shortDuration(time.Until(st.NextLoginAt))+"后")
	}
	u.statsVal.SetText(strings.Join(parts, "　·　"))

	// 托盘提示跟着状态走
	if tip := st.State.Text() + "　" + dash(st.Detail); tip != u.lastTip {
		u.lastTip = tip
		u.app.setToolTip(appTitle + "\n" + tip)
	}
}

func (u *ui) refreshLog() {
	lines, total := u.app.log.Snapshot()

	// 日志被清空过：界面也从头来
	if total < u.logSeen {
		u.logView.SetText("")
		u.logSeen, u.logRendered = 0, 0
	}
	if total == u.logSeen {
		return
	}

	if total-u.logSeen > uint64(len(lines)) {
		// 环形缓冲已经翻篇，之前的内容追溯不到，整体重画
		u.renderAllLog()
		return
	}

	for _, ln := range lines[len(lines)-int(total-u.logSeen):] {
		u.appendLogLine(ln)
	}
	u.logSeen = total

	if u.logRendered > maxLogLines {
		u.renderAllLog()
		return
	}
	if !u.pauseChk.Checked() {
		u.scrollToEnd()
	}
}

// renderAllLog 按当前过滤条件重画整个日志区
func (u *ui) renderAllLog() {
	lines, total := u.app.log.Snapshot()

	u.logView.SetText("")
	u.logRendered = 0
	for _, ln := range lines {
		u.appendLogLine(ln)
	}
	u.logSeen = total
	u.scrollToEnd()
}

func (u *ui) appendLogLine(ln logging.Line) {
	if !u.matchFilter(ln) {
		return
	}
	u.logView.AppendText(ln.String() + "\r\n")
	u.logRendered++
}

func (u *ui) matchFilter(ln logging.Line) bool {
	switch u.levelCombo.CurrentIndex() {
	case 1:
		if ln.Level < logging.Warn {
			return false
		}
	case 2:
		if ln.Level < logging.Error {
			return false
		}
	}

	kw := strings.TrimSpace(u.filterEdit.Text())
	if kw == "" {
		return true
	}
	return strings.Contains(strings.ToLower(ln.Message), strings.ToLower(kw))
}

func (u *ui) scrollToEnd() {
	n := u.logView.TextLength()
	u.logView.SetTextSelection(n, n)
	u.logView.ScrollToCaret()
}

// showWindow 把主界面显示出来并置前（托盘菜单、双击图标、二次启动都会用到）
func (u *ui) showWindow() {
	u.mw.Show()
	u.mw.Activate()
}
