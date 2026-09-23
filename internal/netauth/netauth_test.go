package netauth

import (
	"strings"
	"testing"
	"time"
)

// 门户返回的是 JSONP，解析错了就等于认证永远失败
func TestParseResponse(t *testing.T) {
	cases := []struct {
		body   string
		result int
		msg    string
		wantOK bool
	}{
		{`dr1003({"result":1,"msg":"Portal协议认证成功！"});`, 1, "Portal协议认证成功！", true},
		{`dr1003({"result":0,"msg":"512","ret_code":2});`, 0, "512", true},
		{`dr1003({"result":0,"msg":"用户不存在","ret_code":"1"});`, 0, "用户不存在", true},
		{`<html><body>登录页</body></html>`, 0, "", false},
		{``, 0, "", false},
	}

	for _, c := range cases {
		result, msg, ok := ParseResponse(c.body)
		if ok != c.wantOK || result != c.result || msg != c.msg {
			t.Fatalf("ParseResponse(%q) = (%d, %q, %v)，期望 (%d, %q, %v)",
				c.body, result, msg, ok, c.result, c.msg, c.wantOK)
		}
	}
}

func TestOutcomeDesc(t *testing.T) {
	if got := (Outcome{Success: true, Msg: "ok"}).Desc(); !strings.Contains(got, "认证成功") {
		t.Fatalf("成功文案不对：%s", got)
	}
	if got := (Outcome{AlreadyUp: true, Msg: "512"}).Desc(); !strings.Contains(got, "已有会话") {
		t.Fatalf("512 文案不对：%s", got)
	}
}

func TestNextBackoffIndexClamps(t *testing.T) {
	if got := NextBackoffIndex(Backoff, -1); got != 0 {
		t.Fatalf("首次退避应取第 0 档，实际 %d", got)
	}
	last := len(Backoff) - 1
	if got := NextBackoffIndex(Backoff, last); got != last {
		t.Fatalf("应停在最后一档 %d，实际 %d", last, got)
	}

	// 调用方可以换一张更短的调度表（测试里把退避压到毫秒级就是这么做的）
	short := []time.Duration{time.Millisecond, 2 * time.Millisecond}
	if got := NextBackoffIndex(short, 0); got != 1 {
		t.Fatalf("短表应前进到第 1 档，实际 %d", got)
	}
	if got := NextBackoffIndex(short, 1); got != 1 {
		t.Fatalf("短表应停在末档 1，实际 %d", got)
	}
}

// 退避爬升必须够缓：原先 6 档、48 秒就顶到 60 秒，判定断网一分钟后所有重试都被锁在
// 最大档上。这里把"缓"钉住——档位够多、单调不减、第一档仍然快、到顶要花分钟级。
func TestBackoffRampIsGradual(t *testing.T) {
	if len(Backoff) < 8 {
		t.Fatalf("退避只有 %d 档，爬升太陡", len(Backoff))
	}
	if Backoff[0] > time.Second {
		t.Fatalf("第一档 %s 太慢，判定断网后应该很快重试", Backoff[0])
	}
	if last := Backoff[len(Backoff)-1]; last != 60*time.Second {
		t.Fatalf("末档应为 60 秒，实际 %s", last)
	}

	var ramp time.Duration
	for i, d := range Backoff {
		if i > 0 && d <= Backoff[i-1] {
			t.Fatalf("第 %d 档 %s 没有比上一档 %s 更大", i, d, Backoff[i-1])
		}
		if i < len(Backoff)-1 {
			ramp += d
		}
	}
	if ramp < 90*time.Second {
		t.Fatalf("从密集退到稀疏只用了 %s，太快了", ramp)
	}
}

func TestJitterStaysWithin20Percent(t *testing.T) {
	d := 10 * time.Second
	lo, hi := time.Duration(float64(d)*0.79), time.Duration(float64(d)*1.21)
	for i := 0; i < 200; i++ {
		if got := Jitter(d); got < lo || got > hi {
			t.Fatalf("抖动越界：%s", got)
		}
	}
}

func TestOutageStatSummary(t *testing.T) {
	var s OutageStat
	if s.Summary() != "" {
		t.Fatalf("没有失败时不该有统计：%q", s.Summary())
	}

	s.Add(ProbeFailure{Kind: FailTimeout})
	s.Add(ProbeFailure{Kind: FailTimeout})
	s.Add(ProbeFailure{Kind: FailDNS})
	got := s.Summary()
	for _, want := range []string{"3 次", "超时 2", "DNS 解析失败 1"} {
		if !strings.Contains(got, want) {
			t.Fatalf("统计里缺少 %q：%s", want, got)
		}
	}
	// 没有出现的类别不该硬凑进来
	if strings.Contains(got, "连接失败") || strings.Contains(got, "非 204") {
		t.Fatalf("统计里出现了不该有的类别：%s", got)
	}

	s.Reset()
	if s.Summary() != "" {
		t.Fatal("Reset 后统计应为空")
	}
}

func TestNormalizeMAC(t *testing.T) {
	for in, want := range map[string]string{
		"AA:BB:CC:DD:EE:FF":     "aa:bb:cc:dd:ee:ff",
		"aa-bb-cc-dd-ee-ff":     "aa:bb:cc:dd:ee:ff",
		"  00:11:22:33:44:55  ": "00:11:22:33:44:55",
	} {
		got, err := NormalizeMAC(in)
		if err != nil || got != want {
			t.Fatalf("NormalizeMAC(%q) = %q, %v；期望 %q", in, got, err, want)
		}
	}
	for _, bad := range []string{"", "not-a-mac", "aa:bb:cc:dd:ee", "aa:bb:cc:dd:ee:ff:00"} {
		if _, err := NormalizeMAC(bad); err == nil {
			t.Fatalf("%q 应被判定为非法 MAC", bad)
		}
	}
}

// 本机至少应该能列出一块网卡（CI/容器里可能没有，跳过而不是误报失败）
func TestListAdapters(t *testing.T) {
	adapters := ListAdapters()
	if len(adapters) == 0 {
		t.Skip("当前环境没有可枚举的网卡")
	}
	for _, ad := range adapters {
		if ad.MAC == "" || ad.IP == "" {
			t.Fatalf("网卡信息不完整：%+v", ad)
		}
	}
}
