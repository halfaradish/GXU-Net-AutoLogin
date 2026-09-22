package daemon

import (
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/halfaradish/GXU-Net-AutoLogin/internal/config"
	"github.com/halfaradish/GXU-Net-AutoLogin/internal/logging"
	"github.com/halfaradish/GXU-Net-AutoLogin/internal/netauth"
)

// ── 测试替身 ──────────────────────────────────────────────

// toggleProber 的探测结果由测试随时切换
type toggleProber struct {
	mu   sync.Mutex
	ok   bool
	why  string
	kind netauth.FailKind
}

func (p *toggleProber) set(ok bool, detail string) {
	p.mu.Lock()
	p.ok, p.why = ok, detail
	p.mu.Unlock()
}

func (p *toggleProber) probe() (bool, netauth.ProbeFailure) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.ok {
		return true, netauth.ProbeFailure{}
	}
	kind := p.kind
	if kind == netauth.FailNone {
		kind = netauth.FailTimeout
	}
	return false, netauth.ProbeFailure{Kind: kind, Detail: p.why}
}

// call 是一次登录调用的留痕
type call struct {
	user, password, netType, ip, mac string
}

type fakeAuth struct {
	mu    sync.Mutex
	calls []call
	next  func(n int) netauth.Outcome
}

func (a *fakeAuth) login(user, password, netType, ip, mac string) netauth.Outcome {
	a.mu.Lock()
	a.calls = append(a.calls, call{user, password, netType, ip, mac})
	n := len(a.calls)
	fn := a.next
	a.mu.Unlock()

	if fn != nil {
		return fn(n)
	}
	return netauth.Outcome{Status: 200, Success: true, Msg: "Portal协议认证成功！"}
}

func (a *fakeAuth) snapshot() []call {
	a.mu.Lock()
	defer a.mu.Unlock()
	out := make([]call, len(a.calls))
	copy(out, a.calls)
	return out
}

func (a *fakeAuth) count() int {
	a.mu.Lock()
	defer a.mu.Unlock()
	return len(a.calls)
}

// fastTiming 把节奏压到毫秒级，让状态机在几十毫秒内走完
func fastTiming() Timing {
	return Timing{
		FailThreshold:      2,
		IntervalOnline:     5 * time.Millisecond,
		IntervalFast:       time.Millisecond,
		LoginCooldown:      40 * time.Millisecond,
		SessionRecheck:     5 * time.Millisecond,
		QuietAfter:         25 * time.Millisecond,
		QuietProbeInterval: 5 * time.Millisecond,
		QuietLoginInterval: 15 * time.Millisecond,
	}
}

// fixture 是搭好的一套 daemon + 测试替身
type fixture struct {
	d      *Daemon
	prober *toggleProber
	auth   *fakeAuth
	log    *logging.Logger
}

func newFixture(t *testing.T, cfg *config.Config, mutate func(*fakeAuth)) *fixture {
	t.Helper()

	f := &fixture{
		prober: &toggleProber{},
		auth:   &fakeAuth{},
		log:    logging.New(logging.Options{RingLines: 500}),
	}
	if mutate != nil {
		mutate(f.auth)
	}

	f.d = New(f.log, cfg,
		WithTiming(fastTiming()),
		WithProber(f.prober.probe),
		WithAuthenticator(f.auth.login),
		// 身份解析固定住，测试不依赖真实网卡
		WithIdentityResolver(func(cfg *config.Config) (netauth.Identity, error) {
			if cfg.RouterIP != "" && cfg.RouterMAC != "" {
				return netauth.Identity{IP: cfg.RouterIP, MAC: cfg.RouterMAC, Source: netauth.SourceRouter}, nil
			}
			return netauth.Identity{IP: "10.0.0.2", MAC: "aa:bb:cc:dd:ee:ff", Source: netauth.SourceAuto}, nil
		}),
	)
	t.Cleanup(f.d.Stop)
	return f
}

func testConfig() *config.Config {
	return &config.Config{User: "u1", Password: "p1"}
}

// waitFor 轮询状态直到条件成立，超时即失败
func (f *fixture) waitFor(t *testing.T, what string, cond func(Status) bool) Status {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for {
		st := f.d.Status()
		if cond(st) {
			return st
		}
		if time.Now().After(deadline) {
			t.Fatalf("等待 %q 超时，最后的状态：%+v", what, st)
		}
		time.Sleep(time.Millisecond)
	}
}

// waitCalls 等登录调用次数达到 n
func (f *fixture) waitCalls(t *testing.T, n int) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for f.auth.count() < n {
		if time.Now().After(deadline) {
			t.Fatalf("等待第 %d 次登录超时，实际只有 %d 次", n, f.auth.count())
		}
		time.Sleep(time.Millisecond)
	}
}

// logContains 报告日志里是否出现过某段文字
func (f *fixture) logContains(sub string) bool {
	lines, _ := f.log.Snapshot()
	for _, l := range lines {
		if strings.Contains(l.Message, sub) {
			return true
		}
	}
	return false
}

// ── 用例 ──────────────────────────────────────────────────

// 在线时不发起登录，状态稳定在"在线"
func TestOnlineDoesNotLogin(t *testing.T) {
	f := newFixture(t, testConfig(), nil)
	f.prober.set(true, "")

	if err := f.d.Start(); err != nil {
		t.Fatalf("Start 失败：%v", err)
	}
	f.waitFor(t, "在线", func(s Status) bool { return s.State == StateOnline })

	time.Sleep(30 * time.Millisecond) // 跑过好几个探测周期
	if n := f.auth.count(); n != 0 {
		t.Fatalf("在线状态下不应发起登录，实际 %d 次", n)
	}
}

// 连续失败达到阈值才判定断网并登录；恢复后统计断网次数
func TestOutageTriggersLoginThenRecovers(t *testing.T) {
	f := newFixture(t, testConfig(), nil)
	f.prober.set(false, "超时：2s 内无响应")

	if err := f.d.Start(); err != nil {
		t.Fatalf("Start 失败：%v", err)
	}

	st := f.waitFor(t, "判定断网", func(s Status) bool { return s.State == StateDown })
	if st.ConsecutiveFails < fastTiming().FailThreshold {
		t.Fatalf("判定断网时连续失败次数应达到阈值，实际 %+v", st)
	}
	f.waitCalls(t, 1)

	if c := f.auth.snapshot()[0]; c.user != "u1" || c.password != "p1" || c.ip != "10.0.0.2" {
		t.Fatalf("登录参数不对：%+v", c)
	}
	if !f.logContains("连续 2 次探测失败，判定断网，开始重连") {
		t.Fatal("日志里缺少判定断网的记录")
	}

	// 网络恢复：状态回到在线，并记下一次断网
	f.prober.set(true, "")
	st = f.waitFor(t, "恢复在线", func(s Status) bool { return s.State == StateOnline && s.OutageCount == 1 })
	if st.OutageSince != (time.Time{}) {
		t.Fatalf("恢复后断网起点应清零：%+v", st.OutageSince)
	}
	if !f.logContains("网络已恢复（断网持续") {
		t.Fatal("日志里缺少恢复记录")
	}
}

// 认证成功后进入冷却，冷却期内不再重复登录
func TestLoginCooldownSuppressesRepeats(t *testing.T) {
	f := newFixture(t, testConfig(), nil)
	f.prober.set(false, "连接失败：connection refused")

	if err := f.d.Start(); err != nil {
		t.Fatalf("Start 失败：%v", err)
	}
	f.waitCalls(t, 1)

	// 冷却期内（40ms）应保持安静：允许探测继续失败，但不能反复登录
	time.Sleep(20 * time.Millisecond)
	if n := f.auth.count(); n != 1 {
		t.Fatalf("冷却期内不应重复登录，实际 %d 次", n)
	}
}

// 长时断网进入静默，并按静默的低频节奏重试
func TestQuietModeAfterLongOutage(t *testing.T) {
	f := newFixture(t, testConfig(), func(a *fakeAuth) {
		// 一直认证失败，模拟"网络真的出不去"
		a.next = func(int) netauth.Outcome {
			return netauth.Outcome{Status: 200, Msg: "认证未成功"}
		}
	})
	f.prober.set(false, "超时：2s 内无响应")

	if err := f.d.Start(); err != nil {
		t.Fatalf("Start 失败：%v", err)
	}

	f.waitFor(t, "进入静默", func(s Status) bool { return s.State == StateQuiet })
	if !f.logContains("进入静默（探测 60 秒") && !f.logContains("进入静默（探测") {
		t.Fatal("日志里缺少进入静默的记录")
	}

	// 静默期仍会按 quietLoginInterval 重试
	before := f.auth.count()
	f.waitCalls(t, before+1)

	// 静默期不因失败而退出静默
	if st := f.d.Status(); st.State != StateQuiet {
		t.Fatalf("静默期状态不应变化，实际 %+v", st)
	}
}

// "立即重连"即使当前在线也会认证一次
func TestTriggerForcesLoginWhileOnline(t *testing.T) {
	f := newFixture(t, testConfig(), nil)
	f.prober.set(true, "")

	if err := f.d.Start(); err != nil {
		t.Fatalf("Start 失败：%v", err)
	}
	f.waitFor(t, "在线", func(s Status) bool { return s.State == StateOnline })

	f.d.Trigger()
	f.waitCalls(t, 1)

	if !f.logContains("手动触发：立即认证一次") {
		t.Fatal("日志里缺少手动触发的记录")
	}
}

// 保存并应用：不重启进程即换用新配置，并立即按新配置认证一次
func TestApplySwitchesConfigAndLogsIn(t *testing.T) {
	f := newFixture(t, testConfig(), nil)
	f.prober.set(true, "")

	if err := f.d.Start(); err != nil {
		t.Fatalf("Start 失败：%v", err)
	}
	f.waitFor(t, "在线", func(s Status) bool { return s.State == StateOnline })

	// 换成路由器模式 + 新密码
	next := &config.Config{User: "u2", Password: "p2", NetType: "telecom", RouterIP: "172.16.6.6", RouterMAC: "36:88:8A:99:A4:CC"}
	if err := f.d.Apply(next); err != nil {
		t.Fatalf("Apply 失败：%v", err)
	}
	f.waitCalls(t, 1)

	c := f.auth.snapshot()[0]
	if c.user != "u2" || c.password != "p2" || c.netType != "telecom" {
		t.Fatalf("应按新配置登录，实际 %+v", c)
	}
	if c.ip != "172.16.6.6" || c.mac != "36:88:8A:99:A4:CC" {
		t.Fatalf("应按路由器身份登录，实际 %+v", c)
	}

	st := f.d.Status()
	if !st.RouterMode || st.RouterIP != "172.16.6.6" || st.RouterMAC != "36:88:8A:99:A4:CC" {
		t.Fatalf("状态里的路由器信息不对：%+v", st)
	}

	// 新的探测循环接着跑
	f.waitFor(t, "恢复在线", func(s Status) bool { return s.State == StateOnline })
}

// 停止是幂等的，状态回到"已停止"
func TestStopIsIdempotent(t *testing.T) {
	f := newFixture(t, testConfig(), nil)
	f.prober.set(true, "")

	if err := f.d.Start(); err != nil {
		t.Fatalf("Start 失败：%v", err)
	}
	f.waitFor(t, "在线", func(s Status) bool { return s.State == StateOnline })

	f.d.Stop()
	f.d.Stop() // 再来一次不应 panic / 死锁

	if st := f.d.Status(); st.State != StateStopped {
		t.Fatalf("停止后状态应为已停止，实际 %+v", st)
	}
}
