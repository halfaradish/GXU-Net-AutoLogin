// Package netauth 封装校园网认证的两件事——探测出口是否可达、向门户发起登录，
// 外加认证身份（认证 IP 与 MAC）的解析。全部逻辑与原先的 main.go 一致。
package netauth

import (
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"
)

const (
	// ProbeURL 是探测出口是否可达的端点：返回 204 即视为联网正常
	ProbeURL = "http://connect.rom.miui.com/generate_204"

	// ProbeTimeout 留足余量：实测校园网有线 p99=72ms，WiFi 出现过 619ms 的延迟尖峰，
	// 原先 1s 的预算会把一次尖峰误判成断网。
	ProbeTimeout = 2 * time.Second

	// LoginTimeout 是登录请求的超时；原先 http.Get 没有超时，是唯一会永久卡死的地方
	LoginTimeout = 5 * time.Second
)

// FailKind 是探测失败的类别，用于日志与恢复时的统计
type FailKind int

const (
	// FailNone 表示没有失败
	FailNone FailKind = iota
	// FailTimeout 表示请求超时
	FailTimeout
	// FailDNS 表示域名解析失败
	FailDNS
	// FailConn 表示连接失败
	FailConn
	// FailStatus 表示探测端点返回了非 204 状态码
	FailStatus
)

// String 返回类别名
func (k FailKind) String() string {
	switch k {
	case FailTimeout:
		return "超时"
	case FailDNS:
		return "DNS 解析失败"
	case FailConn:
		return "连接失败"
	case FailStatus:
		return "非 204 响应"
	default:
		return "未知原因"
	}
}

// ProbeFailure 是一次探测失败的详情（成功时为零值）
type ProbeFailure struct {
	Kind   FailKind
	Detail string // 面向日志：类别 + 具体错误
}

// Probe 探测出口是否可达；失败时把原因一并带出来，便于排障与统计
func Probe() (bool, ProbeFailure) {
	client := &http.Client{Timeout: ProbeTimeout}

	resp, err := client.Get(ProbeURL)
	if err != nil {
		kind, detail := errKind(err, ProbeTimeout) // 网络不通 / DNS 故障 / 超时
		return false, ProbeFailure{Kind: kind, Detail: detail}
	}
	defer resp.Body.Close()

	if resp.StatusCode != 204 {
		return false, ProbeFailure{Kind: FailStatus, Detail: fmt.Sprintf("非 204 响应：HTTP %d", resp.StatusCode)}
	}
	return true, ProbeFailure{}
}

// errKind 归类网络错误，并给出可写进日志的简短描述。timeout 是本次请求的超时预算，
// 用于把超时的原始报错（context deadline exceeded…）换成人话。
//
// 描述里必须去掉 *url.Error 的 URL：它会把整个请求 URL 抄一遍，而登录 URL 带着
// user_password 参数——直接把 err 打进日志等于把密码写进日志（旧版就是这样）。
func errKind(err error, timeout time.Duration) (FailKind, string) {
	kind := FailConn

	// *url.Error 自己实现了 net.Error，因此超时判定要放在解包之前
	var dnsErr *net.DNSError
	var netErr net.Error
	switch {
	case errors.As(err, &dnsErr):
		kind = FailDNS
	case errors.As(err, &netErr) && netErr.Timeout():
		kind = FailTimeout
	}

	if kind == FailTimeout {
		return kind, fmt.Sprintf("%s：%s 内无响应", kind, timeout)
	}

	var urlErr *url.Error
	if errors.As(err, &urlErr) {
		err = urlErr.Err
	}

	s := err.Error()
	if r := []rune(s); len(r) > 120 {
		s = string(r[:120]) + "…"
	}
	return kind, fmt.Sprintf("%s：%s", kind, s)
}

// OutageStat 是一次断网期间按原因累计的失败次数
type OutageStat struct {
	Probes  int
	Timeout int
	DNS     int
	Conn    int
	Status  int
}

// Reset 清空统计
func (s *OutageStat) Reset() { *s = OutageStat{} }

// Add 记下一次失败
func (s *OutageStat) Add(f ProbeFailure) {
	s.Probes++
	switch f.Kind {
	case FailTimeout:
		s.Timeout++
	case FailDNS:
		s.DNS++
	case FailConn:
		s.Conn++
	case FailStatus:
		s.Status++
	}
}

// Summary 拼出"期间失败 N 次：超时 a、连接失败 b"这样的统计；没有任何失败时返回空串
func (s OutageStat) Summary() string {
	if s.Probes == 0 {
		return ""
	}

	parts := make([]string, 0, 4)
	for _, p := range []struct {
		name string
		n    int
	}{
		{FailTimeout.String(), s.Timeout},
		{FailConn.String(), s.Conn},
		{FailDNS.String(), s.DNS},
		{FailStatus.String(), s.Status},
	} {
		if p.n > 0 {
			parts = append(parts, fmt.Sprintf("%s %d", p.name, p.n))
		}
	}

	detail := fmt.Sprintf("%d 次", s.Probes)
	if len(parts) > 0 {
		// 用冒号而不是括号：恢复那行整体已经在括号里，避免套两层括号
		detail += "：" + strings.Join(parts, "、")
	}
	return "；期间探测失败 " + detail
}
