package netauth

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math/rand"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// Backoff 是登录失败后的重试退避序列（附加 ±20% 抖动）。
//
// 爬升刻意放缓：比值约 1.5、10 档、约 2 分钟到顶。原先的 1/2/5/10/30/60 只要 48 秒
// 就顶到最大档，于是判定断网一分钟后所有重试都被锁在 60 秒上——网络在 90 秒时恢复
// 也得再等满一档（含抖动约 72 秒）。
//
// 档位不是"失败了几次"的记账：守护进程在网络一有变化时（探测失败类别变了、认证
// IP/MAC 变了、手动触发）就把档位清零，见 daemon.onNetworkChange。所以真正决定
// 间隔的是"距上次网络变化过了多久"。
var Backoff = []time.Duration{
	1 * time.Second,
	2 * time.Second,
	3 * time.Second,
	5 * time.Second,
	8 * time.Second,
	12 * time.Second,
	18 * time.Second,
	27 * time.Second,
	40 * time.Second,
	60 * time.Second,
}

// Outcome 描述一次登录尝试的结果
type Outcome struct {
	Status    int    // HTTP 状态码，0 表示请求未能发出
	Success   bool   // result == 1
	AlreadyUp bool   // msg == "512"：该 IP 已有会话
	Msg       string // 服务器返回的 msg；响应无法解析时为原始响应体片段
	Err       error
}

// Desc 把结果写成一句可直接进日志的话
func (o Outcome) Desc() string {
	switch {
	case o.Err != nil:
		return fmt.Sprintf("登录请求失败: %v", o.Err)
	case o.Success:
		return fmt.Sprintf("认证成功（msg=%s）", o.Msg)
	case o.AlreadyUp:
		return fmt.Sprintf("服务器报告该 IP 已有会话（msg=%s）", o.Msg)
	default:
		return fmt.Sprintf("认证未成功（HTTP %d, msg=%s）", o.Status, o.Msg)
	}
}

type loginResp struct {
	Result  int             `json:"result"`
	Msg     string          `json:"msg"`
	RetCode json.RawMessage `json:"ret_code"` // 可能是数字也可能是字符串，按原样保留
}

// ParseResponse 解析形如 dr1003({"result":1,"msg":"…"}); 的 JSONP 响应
func ParseResponse(body string) (result int, msg string, ok bool) {
	start := strings.Index(body, "{")
	end := strings.LastIndex(body, "}")
	if start < 0 || end <= start {
		return 0, "", false
	}

	var r loginResp
	if err := json.Unmarshal([]byte(body[start:end+1]), &r); err != nil {
		return 0, "", false
	}
	return r.Result, r.Msg, true
}

// Login 向门户发起一次认证。netType 为空表示校园网，否则拼成 账号@运营商。
func Login(user, password, netType, ip, mac string) Outcome {
	// 格式化 MAC：去掉冒号，转小写（适配最早的 bash 脚本行为）
	cleanMAC := strings.ReplaceAll(strings.ToLower(mac), ":", "")

	account := user
	if netType != "" {
		account = user + "@" + netType
	}

	params := url.Values{
		"callback":       {"dr1003"},
		"login_method":   {"1"},
		"user_account":   {account},
		"user_password":  {password},
		"wlan_user_ip":   {ip},
		"wlan_user_mac":  {cleanMAC},
		"wlan_user_ipv6": {""},
		"wlan_ac_ip":     {""},
		"wlan_ac_name":   {""},
		"jsVersion":      {"4.2.1"},
		"terminal_type":  {"1"},
		"lang":           {"zh-cn"},
		"v":              {"5574"},
	}

	loginURL := "http://172.17.0.2:801/eportal/portal/login?" + params.Encode()

	client := &http.Client{Timeout: LoginTimeout}
	resp, err := client.Get(loginURL)
	if err != nil {
		// 只保留归类后的描述：*url.Error 原文会把含 user_password 的完整 URL 抄进消息
		_, detail := errKind(err, LoginTimeout)
		return Outcome{Err: errors.New(detail)}
	}
	defer resp.Body.Close()

	// 限长读取：门户异常时可能返回大页面
	body, err := io.ReadAll(io.LimitReader(resp.Body, 4096))
	if err != nil {
		return Outcome{Status: resp.StatusCode, Err: fmt.Errorf("读取响应体失败: %w", err)}
	}
	bodyStr := strings.TrimSpace(string(body))

	result, msg, ok := ParseResponse(bodyStr)
	if !ok {
		// 例如门户返回 HTML 登录页：保留片段便于排障，按未成功处理
		if r := []rune(bodyStr); len(r) > 120 {
			bodyStr = string(r[:120]) + "…"
		}
		return Outcome{Status: resp.StatusCode, Msg: bodyStr}
	}

	return Outcome{
		Status:    resp.StatusCode,
		Success:   result == 1,
		AlreadyUp: msg == "512",
		Msg:       msg,
	}
}

// NextBackoffIndex 返回退避序列的下一档下标（cur = -1 表示尚未退避过），
// 到末档就停在末档。sched 由调用方给：守护进程用 Timing.Backoff（可注入），
// 默认就是上面这张表。
func NextBackoffIndex(sched []time.Duration, cur int) int {
	if cur+1 >= len(sched) {
		return len(sched) - 1
	}
	return cur + 1
}

// Jitter 给退避时长加 ±20% 抖动，避免多台设备同时重试
func Jitter(d time.Duration) time.Duration {
	delta := float64(d) * 0.2
	return d + time.Duration((rand.Float64()*2-1)*delta)
}
