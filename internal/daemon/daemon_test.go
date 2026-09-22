package daemon

import (
	"errors"
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

// fakeResolver 的身份解析结果由测试随时切换：默认按配置给出固定身份，
// 需要模拟"开机时网络还没就绪"就先让它失败
type fakeResolver struct {
	mu    sync.Mutex
	calls int
	err   error
	ip    string
	mac   string
}

// fail 让之后的解析都失败
func (r *fakeResolver) fail(err error) {
	r.mu.Lock()
	r.err = err
	r.mu.Unlock()
}

// ok 让之后的解析返回指定身份
func (r *fakeResolver) ok(ip, mac string) {
	r.mu.Lock()
	r.err, r.ip, r.mac = nil, ip, mac
	r.mu.Unlock()
}

func (r *fakeResolver) count() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.calls
}

func (r *fakeResolver) resolve(cfg *config.Config) (netauth.Identity, error) {
	r.mu.Lock()
	defer r.mu.Unlock()

	r.calls++
	if r.err != nil {
		return netauth.Identity{}, r.err
	}
	if cfg.RouterIP != "" && cfg.RouterMAC != "" {
		return netauth.Identity{IP: cfg.RouterIP, MAC: cfg.RouterMAC, Source: netauth.SourceRouter}, nil
	}
	ip, mac := r.ip, r.mac
	if ip == "" {
		ip, mac = "10.0.0.2", "aa:bb:cc:dd:ee:ff"
	}
	return netauth.Identity{IP: ip, MAC: mac, Source: netauth.SourceAuto}, nil
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
	d        *Daemon
	prober   *toggleProber
	auth     *fakeAuth
	resolver *fakeResolver
	log      *logging.Logger
}

func newFixture(t *testing.T, cfg *config.Config, mutate func(*fakeAuth)) *fixture {
	t.Helper()

	f := &fixture{
		prober:   &toggleProber{},
		auth:     &fakeAuth{},
		resolver: &fakeResolver{},
		log:      logging.New(logging.Options{RingLines: 500}),
	}
	if mutate != nil {
		mutate(f.auth)
	}

	f.d = New(f.log, cfg,
		WithTiming(fastTiming()),
		WithProber(f.prober.probe),
		WithAuthenticator(f.auth.login),
		// 身份解析固定住，测试不依赖真实网卡
		WithIdentityResolver(f.resolver.resolve),
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

// logCount 数一段文字在日志里出现了几次
func (f *fixture) logCount(sub string) int {
	lines, _ := f.log.Snapshot()
	n := 0
	for _, l := range lines {
		if strings.Contains(l.Message, sub) {
			n++
		}
	}
	return n
}

// logContains 报告日志里是否出现过某段文字
func (f *fixture) logContains(sub string) bool {
	return f.logCount(sub) > 0
}

// ── 用例 ──────────────────────────────────────────────────

// 开机自启时网络还没就绪，拿不到本机 IP（unreachable）不能把守护判死：
// 应当一直重试，网络恢复后自动接上。用户报的就是这条——原来解析只做一次，
// 失败后连探测循环都不启动，程序从此永久失效。
func TestIdentityFailureRetriesUntilReady(t *testing.T) {
	f := newFixture(t, testConfig(), nil)
	unreachable := errors.New("dial udp 8.8.8.8:80: connect: A socket operation was attempted to an unreachable host")
	f.resolver.fail(unreachable)
	f.prober.set(false, "网络不可达")

	// 拿不到 IP 不该算启动失败：循环照常跑，否则没人能再触发解析
	if err := f.d.Start(); err != nil {
		t.Fatalf("拿不到本机 IP 不该算启动失败：%v", err)
	}
	f.waitFor(t, "判定断网", func(s Status) bool { return s.State == StateDown })

	// 要一直在重试，而不是试一次就算了
	before := f.resolver.count()
	time.Sleep(30 * time.Millisecond)
	if after := f.resolver.count(); after <= before {
		t.Fatalf("身份解析没有重试：调用次数停在 %d", before)
	}

	// 失败原因只警告一次，别一秒一条刷屏
	if n := f.logCount("守护继续探测"); n != 1 {
		t.Errorf("解析失败的警告应当只出现一次，实际 %d 次", n)
	}

	// 网络恢复：探测正常、也能拿到 IP 了
	f.prober.set(true, "")
	f.resolver.ok("10.0.0.9", "aa:bb:cc:dd:ee:09")
	st := f.waitFor(t, "在线且身份已解析", func(s Status) bool {
		return s.State == StateOnline && s.Identity.IP == "10.0.0.9"
	})
	if st.Identity.MAC != "aa:bb:cc:dd:ee:09" {
		t.Errorf("界面上的认证 MAC 没跟着更新：%+v", st.Identity)
	}
	if !f.logContains("认证身份") {
		t.Error("解析成功后应当记录一行认证身份")
	}
}

// 身份还没解析出来时不能拿空 IP 去认证：跳过这次登录、说清原因，
// 等身份拿到了再补上
func TestLoginSkippedWhileIdentityUnknown(t *testing.T) {
	f := newFixture(t, testConfig(), nil)
	f.resolver.fail(errors.New("dial udp 8.8.8.8:80: connect: A socket operation was attempted to an unreachable host"))
	f.prober.set(false, "网络不可达")

	if err := f.d.Start(); err != nil {
		t.Fatalf("拿不到本机 IP 不该算启动失败：%v", err)
	}
	f.waitFor(t, "判定断网", func(s Status) bool {
		return s.State == StateDown && s.ConsecutiveFails >= fastTiming().FailThreshold
	})
	time.Sleep(30 * time.Millisecond) // 跑过好几个"该登录了"的时刻

	if n := f.auth.count(); n != 0 {
		t.Fatalf("身份没解析出来还发了 %d 次登录（会拿空 IP 去认证）：%+v", n, f.auth.snapshot())
	}
	if !f.logContains("本次登录跳过") {
		t.Error("跳过登录时应当在日志里说明原因")
	}

	// 身份能拿到之后，这次登录要补上，并且用解析出来的 IP
	f.resolver.ok("10.0.0.7", "aa:bb:cc:dd:ee:07")
	f.waitCalls(t, 1)
	if got := f.auth.snapshot()[0].ip; got != "10.0.0.7" {
		t.Fatalf("登录用的 IP 不对：%q", got)
	}
}

// 登录前要重新解析身份：换网 / DHCP 续租之后旧 IP 可能已经不对了
func TestLoginRefreshesIdentity(t *testing.T) {
	f := newFixture(t, testConfig(), nil)
	f.prober.set(true, "")

	if err := f.d.Start(); err != nil {
		t.Fatalf("Start 失败：%v", err)
	}
	f.waitFor(t, "在线且身份已解析", func(s Status) bool {
		return s.State == StateOnline && s.Identity.IP == "10.0.0.2"
	})

	// 网络变了：本机 IP 换了一个，接着断网触发登录
	f.resolver.ok("10.0.0.9", "aa:bb:cc:dd:ee:09")
	f.prober.set(false, "网络不可达")
	f.waitCalls(t, 1)

	if got := f.auth.snapshot()[0].ip; got != "10.0.0.9" {
		t.Fatalf("登录用的是旧 IP %q，登录前应当重新解析", got)
	}
	if st := f.d.Status(); st.Identity.IP != "10.0.0.9" {
		t.Errorf("界面上的认证地址没跟着更新：%+v", st.Identity)
	}
}

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

	f.waitFor(t, "判定断网", func(s Status) bool { return s.State == StateDown })
	f.waitCalls(t, 1)

	if c := f.auth.snapshot()[0]; c.user != "u1" || c.password != "p1" || c.ip != "10.0.0.2" {
		t.Fatalf("登录参数不对：%+v", c)
	}
	// "先达到阈值、再登录"由这行日志作证：它就在阈值判定处、紧挨着登录发出。
	// 别去采 Status().ConsecutiveFails——登录成功会把它清零，采样点落在哪一侧
	// 全看调度，原来那个断言就是因此随机失败的
	if !f.logContains("连续 2 次探测失败，判定断网，开始重连") {
		t.Fatal("日志里缺少判定断网的记录")
	}

	// 网络恢复：状态回到在线，并记下一次断网
	f.prober.set(true, "")
	st := f.waitFor(t, "恢复在线", func(s Status) bool { return s.State == StateOnline && s.OutageCount == 1 })
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
