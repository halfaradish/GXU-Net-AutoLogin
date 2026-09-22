// Package daemon 是认证守护的核心：可取消的探测循环、断网重连、长时断网静默，
// 以及对外的状态快照。逻辑与命令行版的 main() 循环逐行对应，只是把 time.Sleep
// 换成了可被"停止/立即重连"打断的等待，并把状态暴露给界面。
package daemon

import (
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/halfaradish/GXU-Net-AutoLogin/internal/config"
	"github.com/halfaradish/GXU-Net-AutoLogin/internal/logging"
	"github.com/halfaradish/GXU-Net-AutoLogin/internal/netauth"
)

// Timing 是守护循环的节奏参数；零值表示使用默认值，测试里可以调快
type Timing struct {
	FailThreshold      int
	IntervalOnline     time.Duration
	IntervalFast       time.Duration
	LoginCooldown      time.Duration
	SessionRecheck     time.Duration
	QuietAfter         time.Duration
	QuietProbeInterval time.Duration
	QuietLoginInterval time.Duration
}

// DefaultTiming 返回与命令行版完全一致的节奏
func DefaultTiming() Timing {
	return Timing{
		FailThreshold:      2, // 连续失败达到该次数才判定断网（原先单次失败即判定）
		IntervalOnline:     5 * time.Second,
		IntervalFast:       1 * time.Second, // 出现失败后加快探测，便于尽快发现恢复
		LoginCooldown:      5 * time.Minute, // 认证成功后：探测恢复之前不再重复登录
		SessionRecheck:     1 * time.Minute, // 服务器报告"已有会话"后：隔多久再尝试一次
		QuietAfter:         20 * time.Minute,
		QuietProbeInterval: 60 * time.Second,
		QuietLoginInterval: 5 * time.Minute,
	}
}

func (t Timing) withDefaults() Timing {
	d := DefaultTiming()
	if t.FailThreshold <= 0 {
		t.FailThreshold = d.FailThreshold
	}
	if t.IntervalOnline <= 0 {
		t.IntervalOnline = d.IntervalOnline
	}
	if t.IntervalFast <= 0 {
		t.IntervalFast = d.IntervalFast
	}
	if t.LoginCooldown <= 0 {
		t.LoginCooldown = d.LoginCooldown
	}
	if t.SessionRecheck <= 0 {
		t.SessionRecheck = d.SessionRecheck
	}
	if t.QuietAfter <= 0 {
		t.QuietAfter = d.QuietAfter
	}
	if t.QuietProbeInterval <= 0 {
		t.QuietProbeInterval = d.QuietProbeInterval
	}
	if t.QuietLoginInterval <= 0 {
		t.QuietLoginInterval = d.QuietLoginInterval
	}
	return t
}

// State 是守护进程对外暴露的状态
type State int

const (
	// StateStopped 已停止
	StateStopped State = iota
	// StateStarting 正在解析认证信息 / 启动
	StateStarting
	// StateOnline 探测正常
	StateOnline
	// StateDown 探测失败（未达静默时长）
	StateDown
	// StateQuiet 已连续断网超过静默阈值
	StateQuiet
	// StateAuthing 正在发起登录
	StateAuthing
	// StateFailed 启动失败。现在没有路径会置成它了——拿不到本机 IP/MAC 之类的
	// 问题都改成在循环里重试（见 refreshIdentity），不再把守护判死；
	// 保留它是为了界面上仍有对应的显示分支
	StateFailed
)

// Text 返回界面上显示的中文状态
func (s State) Text() string {
	switch s {
	case StateOnline:
		return "在线"
	case StateDown:
		return "断网"
	case StateQuiet:
		return "静默"
	case StateAuthing:
		return "认证中"
	case StateStarting:
		return "启动中"
	case StateFailed:
		return "启动失败"
	default:
		return "已停止"
	}
}

// Status 是一次状态快照，界面按秒刷新它即可
type Status struct {
	State  State
	Detail string // 最近一次判定/认证的一句说明

	Identity   netauth.Identity // 本次认证使用的 IP / MAC 及来源
	RouterMode bool             // 是否路由器模式
	RouterIP   string
	RouterMAC  string

	ConsecutiveFails int       // 连续探测失败次数
	LastProbeAt      time.Time // 最近一次探测时间
	LastFailDetail   string    // 最近一次探测失败原因
	OutageSince      time.Time // 本次断网起点，零值表示当前在线
	OutageCount      int       // 累计断网次数
	OnlineSince      time.Time // 本次在线起点

	LastLoginAt   time.Time // 最近一次登录尝试时间
	LastLoginDesc string    // 最近一次登录结果
	NextLoginAt   time.Time // 下一次允许登录的时间

	Fast  bool // 当前是否处于快速探测（1 秒）
	Quiet bool // 当前是否处于静默期
}

// EventKind 是状态切换事件的类别。托盘版现在不订阅事件（提醒只留启动与进托盘两处），
// 这个扩展点保留给需要感知状态切换的上层，见 docs/tray.md 的"通知策略"。
type EventKind int

const (
	// EventDown 判定断网
	EventDown EventKind = iota
	// EventRecovered 网络恢复
	EventRecovered
	// EventQuiet 进入静默
	EventQuiet
	// EventLogin 一次登录尝试有了结果
	EventLogin
)

// Event 是一条状态切换通知
type Event struct {
	Kind EventKind
	Text string
}

// EventHandler 接收状态切换通知；实现方不能阻塞
type EventHandler func(Event)

// Prober 与 Authenticator 是可替换的探测/登录实现，测试里注入假数据用
type Prober func() (bool, netauth.ProbeFailure)

// Authenticator 是可替换的登录实现
type Authenticator func(user, password, netType, ip, mac string) netauth.Outcome

// IdentityResolver 解析这次认证要用的 IP/MAC（测试里注入固定值，避免依赖真实网卡）
type IdentityResolver func(cfg *config.Config) (netauth.Identity, error)

// Daemon 是守护进程
type Daemon struct {
	log     *logging.Logger
	cfg     *config.Config
	timing  Timing
	probe   Prober
	login   Authenticator
	resolve IdentityResolver

	handler EventHandler

	mu      sync.Mutex
	status  Status
	cancel  context.CancelFunc
	done    chan struct{}
	forced  bool
	trigger chan struct{}
}

// Option 用于替换可注入的依赖
type Option func(*Daemon)

// WithEventHandler 设置状态切换通知的接收者
func WithEventHandler(h EventHandler) Option {
	return func(d *Daemon) { d.handler = h }
}

// WithTiming 覆盖节奏参数（测试用）
func WithTiming(t Timing) Option {
	return func(d *Daemon) { d.timing = t }
}

// WithProber 覆盖探测实现（测试用）
func WithProber(p Prober) Option {
	return func(d *Daemon) { d.probe = p }
}

// WithAuthenticator 覆盖登录实现（测试用）
func WithAuthenticator(a Authenticator) Option {
	return func(d *Daemon) { d.login = a }
}

// WithIdentityResolver 覆盖认证身份的解析方式（测试用）
func WithIdentityResolver(r IdentityResolver) Option {
	return func(d *Daemon) { d.resolve = r }
}

// New 创建守护进程；此时还没启动，Status().State 为 StateStopped
func New(log *logging.Logger, cfg *config.Config, opts ...Option) *Daemon {
	d := &Daemon{
		log:     log,
		cfg:     cfg,
		timing:  DefaultTiming(),
		probe:   netauth.Probe,
		login:   netauth.Login,
		trigger: make(chan struct{}, 1),
		status:  Status{State: StateStopped},
	}
	for _, o := range opts {
		o(d)
	}
	if d.resolve == nil {
		d.resolve = func(cfg *config.Config) (netauth.Identity, error) {
			return netauth.ResolveIdentity(cfg.RouterIP, cfg.RouterMAC, cfg.MacAddress, log.Warn)
		}
	}
	return d
}

// Start 启动守护循环。按命令行版的语义，启动时只探测、不强制登录。
func (d *Daemon) Start() error {
	return d.restart(false)
}

// Apply 换用新配置并立即认证一次：托盘"保存并应用"走的就是这条路径，
// 不需要重启进程。
func (d *Daemon) Apply(cfg *config.Config) error {
	d.Stop()

	d.mu.Lock()
	d.cfg = cfg
	d.mu.Unlock()

	return d.restart(true)
}

// Trigger 请求立即认证一次（托盘"立即重连"）
func (d *Daemon) Trigger() {
	d.mu.Lock()
	d.forced = true
	d.mu.Unlock()

	select {
	case d.trigger <- struct{}{}:
	default: // 已经有一次待处理的请求，不重复塞
	}
}

// Stop 停止守护循环并等待它退出；未启动时是空操作
func (d *Daemon) Stop() {
	d.mu.Lock()
	cancel, done := d.cancel, d.done
	d.cancel, d.done = nil, nil
	d.mu.Unlock()

	if cancel == nil {
		return
	}
	cancel()
	if done != nil {
		<-done // 等在途的探测/登录结束，避免新旧两个循环同时改状态
	}

	d.mutate(func(s *Status) {
		s.State = StateStopped
		s.Fast, s.Quiet = false, false
	})
	d.log.Info("守护已停止")
}

// Status 返回当前状态快照
func (d *Daemon) Status() Status {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.status
}

func (d *Daemon) restart(forceLogin bool) error {
	d.mu.Lock()
	cfg := d.cfg
	d.mu.Unlock()

	d.mutate(func(s *Status) {
		s.State = StateStarting
		s.Detail = ""
		s.Fast, s.Quiet = false, false
		s.OutageSince = time.Time{}
		s.ConsecutiveFails = 0
		// 换配置后旧的认证身份不再作数（路由器模式、手动 MAC 都会改它），
		// 循环里会重新解析
		s.Identity = netauth.Identity{}
	})

	if cfg.RouterIP != "" && cfg.RouterMAC != "" {
		d.log.Info("使用路由器模式进行认证")
	} else {
		d.log.Info("使用本机模式进行认证")
	}

	// 认证身份不在这里解析：开机自启时网络可能还没就绪，解析失败一次就把守护
	// 判死（原来就是这样，进程从此再也不重试）。解析挪进循环里跟着重试，
	// 见 refreshIdentity / identityFor。
	d.mutate(func(s *Status) {
		s.RouterMode = cfg.RouterIP != "" && cfg.RouterMAC != ""
		s.RouterIP, s.RouterMAC = cfg.RouterIP, cfg.RouterMAC
	})

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})

	d.mu.Lock()
	d.cancel, d.done = cancel, done
	d.mu.Unlock()

	go d.run(ctx, done, cfg, forceLogin)
	return nil
}

// run 是守护循环本体。结构与命令行版保持一致：
// 连续失败才判定断网、探测间隔自适应、登录失败按退避重试、长时间断网进入静默。
func (d *Daemon) run(ctx context.Context, done chan<- struct{}, cfg *config.Config, forceLogin bool) {
	defer close(done)

	t := d.timing.withDefaults()
	st := &loopState{backoffIdx: -1}

	// "保存并应用"要求改完立刻认证一次，不等探测判定
	if forceLogin {
		d.log.Info("配置已应用，立即认证一次")
		d.attemptLogin(cfg, st, t, false)
	}

	for {
		// 还没有认证身份（开机自启时网络没就绪就是这种情况）就再试一次。
		// 失败不影响探测：网络一恢复，这里就能拿到 IP 了。
		d.refreshIdentity(cfg, st)

		// "立即重连"：不管当前判定如何，直接认证一次
		if d.takeForced() {
			d.log.Info("手动触发：立即认证一次")
			d.attemptLogin(cfg, st, t, false)
		}

		ok, fail := d.probe()
		if ok {
			d.noteOnline(st, t)
			if !d.sleep(ctx, t.IntervalOnline) {
				return
			}
			continue
		}

		st.fails++
		if st.outageSince.IsZero() {
			// 本次断网的第一笔失败：记下起点、清空统计，并把失败原因写出来。
			// 原先只报"判定断网"，看不出是超时、DNS 还是探测端点返回了异常状态码
			st.outageSince = time.Now()
			st.stat.Reset()
			d.mutate(func(s *Status) { s.OutageCount++ })
			d.log.Warn("探测失败（%s），连续 %d 次失败即判定断网", fail.Detail, t.FailThreshold)
		}
		st.stat.Add(fail)
		d.noteFail(st, fail)

		// 长时断网静默：探测放慢、登录尝试固定低频，直到网络真的能出去为止。
		// 不按日历判断，因此假期里网络正常时不会进入静默，假期里真断网也能按常规节奏快速恢复。
		if time.Since(st.outageSince) >= t.QuietAfter {
			if !st.quietLogged {
				st.quietLogged = true
				d.log.Warn("已连续断网 %.0f 分钟，进入静默（探测 %.0f 秒、每 %.0f 分钟尝试一次登录）",
					t.QuietAfter.Minutes(), t.QuietProbeInterval.Seconds(), t.QuietLoginInterval.Minutes())
				d.emit(Event{Kind: EventQuiet, Text: fmt.Sprintf("已连续断网 %.0f 分钟，进入静默期", t.QuietAfter.Minutes())})
			}
			d.mutate(func(s *Status) {
				s.State = StateQuiet
				s.Quiet, s.Fast = true, false
				s.Detail = "连续断网 " + time.Since(st.outageSince).Round(time.Second).String() + "，静默中"
			})

			if time.Now().After(st.nextLogin) {
				d.attemptLogin(cfg, st, t, true)
			}

			// 睡到"下次探测"与"下次登录尝试"中较早的一个，避免尝试时刻被探测周期推后
			wait := t.QuietProbeInterval
			if left := time.Until(st.nextLogin); left < wait {
				wait = left
			}
			if !d.sleep(ctx, wait) {
				return
			}
			continue
		}

		// 会话已确认时探测失败更可能来自探测端点本身，保持在线档间隔，避免高频空探
		if !st.sessionAssumed {
			st.fast = true
		}
		d.mutate(func(s *Status) { s.Fast = st.fast })

		// 只有"连续失败达到阈值"且"到了本次允许尝试的时间点"才重连：
		// 前者挡掉单次抖动，后者挡掉冷却期内的重复请求。
		if st.fails >= t.FailThreshold && time.Now().After(st.nextLogin) {
			if !st.downLogged {
				st.downLogged = true
				d.log.Warn("连续 %d 次探测失败，判定断网，开始重连", st.fails)
				d.emit(Event{Kind: EventDown, Text: fmt.Sprintf("连续 %d 次探测失败，判定断网", st.fails)})
			}
			d.attemptLogin(cfg, st, t, false)
		}

		interval := t.IntervalOnline
		if st.fast {
			interval = t.IntervalFast
		}
		if !d.sleep(ctx, interval) {
			return
		}
	}
}

// loopState 是守护循环的局部状态，与命令行版里的那几个变量一一对应
type loopState struct {
	fails          int
	fast           bool
	backoffIdx     int
	downLogged     bool
	quietLogged    bool
	sessionAssumed bool
	nextLogin      time.Time
	outageSince    time.Time
	stat           netauth.OutageStat

	// 认证身份（本机 IP/MAC）。零值表示还没解析出来——开机自启时网络可能还没就绪，
	// 解析不出来就每轮重试，网络恢复后自然接上；解析出来后登录前还会再刷新一次，
	// 因为换网 / DHCP 续租之后旧 IP 可能已经不对了。
	id netauth.Identity

	// 两类警告各只报一次，避免一秒一条刷屏（解析成功后重新武装）
	resolveWarned bool
	skipWarned    bool
}

func (st *loopState) reset() {
	st.fails, st.fast, st.backoffIdx = 0, false, -1
	st.downLogged, st.quietLogged, st.sessionAssumed = false, false, false
	st.nextLogin, st.outageSince = time.Time{}, time.Time{}
	st.stat.Reset()
}

// resolveIdentity 解析一次认证身份并记下来
func (d *Daemon) resolveIdentity(cfg *config.Config, st *loopState) (netauth.Identity, error) {
	id, err := d.resolve(cfg)
	if err != nil {
		return netauth.Identity{}, err
	}

	changed := id != st.id
	st.id = id
	st.resolveWarned, st.skipWarned = false, false

	d.mutate(func(s *Status) { s.Identity = id })
	if changed {
		d.log.Info("认证身份：IP=%s | MAC=%s（%s）", id.IP, id.MAC, id.Source)
	}
	return id, nil
}

// identityFor 取这次认证要用的身份。refresh=true（登录前）时总是重新解析一次；
// 平时只有手上还没有结果时才解析。解析失败时如果还有旧结果就沿用旧的——一次抖动
// 不该把登录也跳掉；一个结果都没有才报错，由调用方决定怎么办。
func (d *Daemon) identityFor(cfg *config.Config, st *loopState, refresh bool) (netauth.Identity, error) {
	if !refresh && st.id != (netauth.Identity{}) {
		return st.id, nil
	}

	id, err := d.resolveIdentity(cfg, st)
	if err == nil {
		return id, nil
	}
	if st.id != (netauth.Identity{}) {
		d.log.Warn("重新解析认证身份失败（%v），沿用上次的 IP=%s MAC=%s", err, st.id.IP, st.id.MAC)
		return st.id, nil
	}
	return netauth.Identity{}, err
}

// refreshIdentity 是循环每轮开头的"尽力解析"：还没有身份就再试一次，失败只警告
// 一次、不打断探测——开机时网络没就绪就是这种情况，等网络起来自然就好了。
func (d *Daemon) refreshIdentity(cfg *config.Config, st *loopState) {
	if st.id != (netauth.Identity{}) {
		return
	}
	if _, err := d.identityFor(cfg, st, false); err != nil && !st.resolveWarned {
		st.resolveWarned = true
		d.log.Warn("拿不到本机 IP/MAC（%v），守护继续探测，稍后自动重试", err)
	}
}

// noteOnline 处理"这次探测成功"：只在状态切换时打印，避免刷屏
func (d *Daemon) noteOnline(st *loopState, t Timing) {
	recovered := st.downLogged || st.quietLogged
	duration := time.Since(st.outageSince).Round(time.Second)
	summary := st.stat.Summary()

	if recovered {
		d.log.Info("网络已恢复（断网持续 %s%s）", duration, summary)
		d.emit(Event{Kind: EventRecovered, Text: "网络已恢复（断网持续 " + duration.String() + "）"})
	}

	st.reset()

	d.mutate(func(s *Status) {
		s.State = StateOnline
		s.Quiet, s.Fast = false, false
		s.LastProbeAt = time.Now()
		s.LastFailDetail = ""
		s.ConsecutiveFails = 0
		s.Detail = "探测正常"
		if recovered || s.OnlineSince.IsZero() {
			s.OnlineSince = time.Now()
		}
		s.OutageSince = time.Time{}
	})
}

// noteFail 处理"这次探测失败"：更新状态面板要用的失败原因与计数
func (d *Daemon) noteFail(st *loopState, fail netauth.ProbeFailure) {
	d.mutate(func(s *Status) {
		s.State = StateDown
		s.Detail = "探测失败：" + fail.Detail
		s.LastProbeAt = time.Now()
		s.LastFailDetail = fail.Detail
		s.ConsecutiveFails = st.fails
		s.OutageSince = st.outageSince
		s.OnlineSince = time.Time{}
	})
}

// attemptLogin 发起一次登录并按结果更新状态与节奏。quiet=true 时（静默期）由
// 调用方固定低频重试，因此这里只记录结果、不做退避。
func (d *Daemon) attemptLogin(cfg *config.Config, st *loopState, t Timing, quiet bool) netauth.Outcome {
	// 登录前重新解析一次身份：换网 / DHCP 续租之后旧 IP 可能已经不对，拿旧 IP
	// 去认证门户会拒。解析不出来就别发这次登录——发了也是白搭，等下一轮再试。
	id, err := d.identityFor(cfg, st, true)
	if err != nil {
		if !st.skipWarned {
			st.skipWarned = true
			d.log.Warn("拿不到本机 IP/MAC（%v），本次登录跳过，稍后重试", err)
		}
		d.mutate(func(s *Status) { s.Detail = "拿不到本机 IP/MAC，登录已跳过：" + err.Error() })
		return netauth.Outcome{Err: err}
	}

	// 认证期间状态标成"认证中"，结束后还原——连接状态由探测决定，
	// 一次登录的成败不该改写它（门户说成功、而探测仍失败的情况确实存在）
	prev := d.Status().State
	d.mutate(func(s *Status) { s.State = StateAuthing })

	o := d.login(cfg.User, cfg.Password, cfg.NetType, id.IP, id.MAC)

	d.mutate(func(s *Status) {
		s.LastLoginAt = time.Now()
		s.LastLoginDesc = o.Desc()
		if s.State == StateAuthing {
			s.State = prev
		}
	})

	if quiet {
		d.log.Info("· 静默期登录尝试：%s", o.Desc())
		st.nextLogin = time.Now().Add(t.QuietLoginInterval)
		d.mutate(func(s *Status) { s.NextLoginAt = st.nextLogin })
		d.emit(Event{Kind: EventLogin, Text: "静默期登录尝试：" + o.Desc()})
		return o
	}

	switch {
	case o.Success:
		d.log.Info("%s", o.Desc())
		st.fails, st.backoffIdx = 0, -1
		st.sessionAssumed, st.fast = true, false
		st.nextLogin = time.Now().Add(t.LoginCooldown)
	case o.AlreadyUp:
		// 该 IP 已有会话：探测失败更可能来自探测端点本身，放慢节奏
		d.log.Info("%s，%s 后才会再次尝试登录，期间继续探测", o.Desc(), t.SessionRecheck)
		st.sessionAssumed, st.fast = true, false
		st.nextLogin = time.Now().Add(t.SessionRecheck)
	default:
		st.backoffIdx = netauth.NextBackoffIndex(st.backoffIdx)
		delay := netauth.Jitter(netauth.Backoff[st.backoffIdx])
		st.nextLogin = time.Now().Add(delay)
		d.log.Warn("%s，%s 后重试", o.Desc(), delay.Round(time.Second))
	}

	d.mutate(func(s *Status) {
		s.ConsecutiveFails = st.fails
		s.Fast = st.fast
		s.NextLoginAt = st.nextLogin
	})
	d.emit(Event{Kind: EventLogin, Text: o.Desc()})
	return o
}

// sleep 等待一段时间，可被"停止"与"立即重连"打断；返回 false 表示该退出循环
func (d *Daemon) sleep(ctx context.Context, dur time.Duration) bool {
	if dur <= 0 {
		dur = time.Millisecond
	}
	timer := time.NewTimer(dur)
	defer timer.Stop()

	select {
	case <-ctx.Done():
		return false
	case <-d.trigger:
		return true
	case <-timer.C:
		return true
	}
}

func (d *Daemon) takeForced() bool {
	d.mu.Lock()
	defer d.mu.Unlock()
	forced := d.forced
	d.forced = false
	return forced
}

func (d *Daemon) mutate(f func(*Status)) {
	d.mu.Lock()
	f(&d.status)
	d.mu.Unlock()
}

func (d *Daemon) emit(ev Event) {
	if d.handler != nil {
		d.handler(ev)
	}
}
