package main

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"math/rand"
	"os"
	"path/filepath"
	"strings"
	"net/http"
	"net"
	"net/url"
	"time"
	"io"
	"flag"
)

const (
	configFileName = ".env"

	// 老版本用 config.txt；现在找不到 .env 时如果发现它还在，就提示改名而不是重开一个新配置
	legacyConfigFileName = "config.txt"

	// ── 探测 ──────────────────────────────────────────────
	// 超时留足余量：实测校园网有线 p99=72ms，WiFi 出现过 619ms 的延迟尖峰，
	// 原先 1s 的预算会把一次尖峰误判成断网。
	probeURL      = "http://connect.rom.miui.com/generate_204"
	probeTimeout  = 2 * time.Second
	failThreshold = 2 // 连续失败达到该次数才判定断网（原先单次失败即判定）

	// ── 探测节奏 ──────────────────────────────────────────
	intervalOnline = 5 * time.Second // 稳定在线时的探测间隔
	intervalFast   = 1 * time.Second // 出现失败后加快探测，便于尽快发现恢复

	// ── 登录 ──────────────────────────────────────────────
	loginTimeout   = 5 * time.Second // 原先 http.Get 无超时，是唯一会永久卡死的地方
	loginCooldown  = 5 * time.Minute // 认证成功后：探测恢复之前不再重复登录
	sessionRecheck = 1 * time.Minute // 服务器报告"已有会话"后：隔多久再尝试一次登录

	// ── 长时断网静默（替代原"学生模式"）────────────────────
	// 校园网会在 00:00-06:00 强制断网，但该策略随学期 / 假期 / 在校人数变化
	// （假期与开学期间并不一致），因此不按日历判断：连续探测失败超过 quietAfter
	// 即进入静默，避免整夜高频空转；探测一旦成功就立刻回到常规节奏。
	quietAfter         = 20 * time.Minute
	quietProbeInterval = 60 * time.Second
	quietLoginInterval = 5 * time.Minute

	// ── 日志 ──────────────────────────────────────────────
	// LOG_TO_FILE 打开时，每行日志同时写控制台与该文件；写满后轮转，
	// 只保留最近的 logBackups 份，避免长期挂机把磁盘写满。
	defaultLogDir      = "logs"                  // LOG_FILE 留空时，日志放在程序当前目录的 logs/ 下（不存在会自动创建）
	defaultLogFileName = "GXU_Net_AutoLogin.log" // 默认文件名
	logMaxSize         = 5 << 20                 // 单个日志文件上限
	logBackups         = 2                       // 保留的份数：.1、.2（更老的删除）
)

// ═══════════════════════════ 日志 ═══════════════════════════
//
// 所有输出都走 logInfo/logWarn/logError，控制台与日志文件拿到同一份文本：
// 文案保留 emoji 便于肉眼扫，前缀的时间戳与级别便于按时间对齐和 grep。

type logLevel int

const (
	lvlInfo logLevel = iota
	lvlWarn
	lvlError
)

func (l logLevel) String() string {
	switch l {
	case lvlWarn:
		return "WARN"
	case lvlError:
		return "ERROR"
	default:
		return "INFO"
	}
}

// logFileSink 非 nil 时日志同时写入文件（由 setupLogFile 设置）
var logFileSink *rotatingFile

func logf(lv logLevel, format string, args ...any) {
	line := time.Now().Format("2006-01-02 15:04:05") +
		" [" + lv.String() + "] " + fmt.Sprintf(format, args...) + "\n"

	os.Stdout.WriteString(line)

	if logFileSink != nil {
		if _, err := logFileSink.Write([]byte(line)); err != nil {
			// 日志文件不可写不能拖垮守护进程：报一次，之后只打印到控制台
			fmt.Fprintf(os.Stdout, "%s [WARN] ⚠️ 日志文件写入失败，后续仅打印到控制台: %v\n",
				time.Now().Format("2006-01-02 15:04:05"), err)
			logFileSink.Close()
			logFileSink = nil
		}
	}
}

func logInfo(format string, args ...any)  { logf(lvlInfo, format, args...) }
func logWarn(format string, args ...any)  { logf(lvlWarn, format, args...) }
func logError(format string, args ...any) { logf(lvlError, format, args...) }

// rotatingFile 按大小轮转的追加写入器：写满后把当前文件改名为 .1，
// 原来的 .1 变 .2，更老的丢弃，然后重开一个空文件继续写。
type rotatingFile struct {
	path    string
	maxSize int64
	backups int
	f       *os.File
	size    int64
}

func openLogFile(path string) (*rotatingFile, error) {
	f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0644)
	if err != nil {
		return nil, err
	}

	// 追加模式：重启后接着写同一个文件，大小以现有内容为起点
	size := int64(0)
	if st, err := f.Stat(); err == nil {
		size = st.Size()
	}

	return &rotatingFile{path: path, maxSize: logMaxSize, backups: logBackups, f: f, size: size}, nil
}

func (w *rotatingFile) Write(p []byte) (int, error) {
	if w.size+int64(len(p)) > w.maxSize {
		if err := w.rotate(); err != nil {
			return 0, err
		}
	}
	n, err := w.f.Write(p)
	w.size += int64(n)
	return n, err
}

func (w *rotatingFile) rotate() error {
	w.f.Close()

	// 改名是尽力而为：被别的进程占用（例如编辑器打开着 .1）时只是这一轮没轮转成功，
	// 后面重开的文件继续写，不影响日志本身
	os.Remove(fmt.Sprintf("%s.%d", w.path, w.backups))
	for i := w.backups - 1; i >= 1; i-- {
		os.Rename(fmt.Sprintf("%s.%d", w.path, i), fmt.Sprintf("%s.%d", w.path, i+1))
	}
	os.Rename(w.path, w.path+".1")

	f, err := os.OpenFile(w.path, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0644)
	if err != nil {
		return err
	}
	w.f, w.size = f, 0
	return nil
}

func (w *rotatingFile) Close() error {
	if w == nil || w.f == nil {
		return nil
	}
	return w.f.Close()
}

// setupLogFile 按配置打开日志文件；失败只警告，不影响程序继续运行。
// 返回实际使用的路径（失败时为空）。
func setupLogFile(enabled bool, path string) string {
	if !enabled {
		return ""
	}
	if path == "" {
		path = filepath.Join(defaultLogDir, defaultLogFileName)
	}

	// 目录不存在就建：默认的 logs/，或 LOG_FILE 里写的多级路径
	if dir := filepath.Dir(path); dir != "" && dir != "." {
		if err := os.MkdirAll(dir, 0755); err != nil {
			logWarn("⚠️ 无法创建日志目录 %s：%v（仅打印到控制台）", dir, err)
			return ""
		}
	}

	f, err := openLogFile(path)
	if err != nil {
		logWarn("⚠️ 无法写入日志文件 %s：%v（仅打印到控制台）", path, err)
		return ""
	}
	logFileSink = f

	abs, err := filepath.Abs(path)
	if err != nil {
		abs = path
	}
	return abs
}

// loginBackoff 登录失败后的重试退避序列（附加 ±20% 抖动）
var loginBackoff = []time.Duration{
	1 * time.Second,
	2 * time.Second,
	5 * time.Second,
	10 * time.Second,
	30 * time.Second,
	60 * time.Second,
}

// Config 结构体保存配置
type Config struct {
	User     string
	Password string
	NetType  string // 新增字段

	// 路由器模式（当两者都非空时启用）
	RouterIP  string
	RouterMAC string

	// 日志文件（仅在配置文件模式下使用；命令行模式见 -log / -logfile）
	LogToFile bool
	LogPath   string
}

// loadConfig 加载或创建 .env 配置（键名不区分大小写，值可以用引号包起来）
func loadConfig() (*Config, error) {
	// 检查文件是否存在
	if _, err := os.Stat(configFileName); os.IsNotExist(err) {
		// 老版本用的是 config.txt：提示改名，别让已有账号密码看起来"丢了"
		if _, legacy := os.Stat(legacyConfigFileName); legacy == nil {
			return nil, fmt.Errorf("配置已改用 %s，但检测到旧的 %s：请改成 %s 后重新运行（键名大小写不限）",
				configFileName, legacyConfigFileName, configFileName)
		}

		// 创建默认模板
		defaultContent := `# 校园网登陆脚本信息设置：（注意请不要改变格式）
# 用户名：（填写示例：USER=1807210721）
USER=
# 密码：（填写示例：PASSWORD=www.nekopara.uk）
PASSWORD=
# 运营商选择，留空选择校园网，如果需要选择运营商，电信填写telecom，联通填写unicom，移动填写cmcc
NET_TYPE=
# 开启路由器登陆模式：
# 如果填写以下两个参数（均非空），则使用指定的路由器IP和MAC进行认证。
# 否则使用本机IP和MAC。
# 示例：
# ROUTER_IP=172.16.6.6
# ROUTER_MAC=36:88:8A:99:A4:CC
ROUTER_IP=
ROUTER_MAC=
# 日志：（是否把日志同时写入文件，留空或 false 只打印到控制台）
LOG_TO_FILE=
# 日志文件路径：（留空则使用程序目录下的 logs/GXU_Net_AutoLogin.log）
LOG_FILE=
`

			err = os.WriteFile(configFileName, []byte(defaultContent), 0644)
			if err != nil {
				return nil, fmt.Errorf("无法创建配置文件: %v", err)
			}
			return nil, fmt.Errorf("未找到配置文件，配置文件 '%s' 已创建，请先填写上网信息后重新运行程序", configFileName)
	}

	// 读取并解析
	content, err := os.ReadFile(configFileName)
	if err != nil {
		return nil, fmt.Errorf("无法读取配置文件: %v", err)
	}

	cfg := &Config{}
	scanner := bufio.NewScanner(strings.NewReader(string(content)))
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())

		// 跳过空行和注释
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}

		// 按第一个 '=' 分割（避免密码含等号出错）
		parts := strings.SplitN(line, "=", 2)
		if len(parts) != 2 {
			continue // 格式错误，跳过
		}

		key := strings.TrimSpace(parts[0])
		value := strings.TrimSpace(parts[1])

		// .env 习惯把值用引号包起来，这里接受 "…" 与 '…' 两种写法
		if len(value) >= 2 {
			if (value[0] == '"' && value[len(value)-1] == '"') ||
				(value[0] == '\'' && value[len(value)-1] == '\'') {
				value = value[1 : len(value)-1]
			}
		}

		// 键名不区分大小写：.env 里习惯写全大写，旧 config.txt 里的写法也照旧能用
		switch strings.ToLower(key) {
			case "user":
				cfg.User = value
			case "password":
				cfg.Password = value
			case "net_type":
				cfg.NetType = value
			case "router_ip":
				cfg.RouterIP = value
			case "router_mac":
				cfg.RouterMAC = value
			case "log_to_file":
				// 留空 = 不写文件，与新增该键之前的行为一致
				switch strings.ToLower(value) {
				case "":
					cfg.LogToFile = false
				case "true", "1", "yes", "on":
					cfg.LogToFile = true
				case "false", "0", "no", "off":
					cfg.LogToFile = false
				default:
					return nil, fmt.Errorf("错误：LOG_TO_FILE 只能填 true 或 false（留空表示不写文件），当前值: %s", value)
				}
			case "log_file":
				cfg.LogPath = value
		}
	}

	if err := scanner.Err(); err != nil {
		return nil, err
	}

	// 基础校验
	if cfg.User == "" || cfg.Password == "" {
		return nil, fmt.Errorf("请在 '%s' 中填写用户名和密码", configFileName)
	}
	// 在 loadConfig 函数中，解析配置后添加：
	if cfg.NetType != "" {
		// 检查是否是合法的运营商
		valid := false
		switch strings.ToLower(cfg.NetType) {
			case "telecom", "unicom", "cmcc":
				valid = true
		}

		if !valid {
			return nil, fmt.Errorf("错误：运营商类型必须为空、telecom、unicom或cmcc（不区分大小写），当前值: %s", cfg.NetType)
		}
	}

	return cfg, nil
}

func getLocalIP() (string, error) {
	conn, err := net.Dial("udp", "8.8.8.8:80")
	if err != nil {
		return "", err
	}
	defer conn.Close()
	return conn.LocalAddr().(*net.UDPAddr).IP.String(), nil
}

func getMACForIP(ip string) (string, error) {
	interfaces, err := net.Interfaces()
	if err != nil {
		return "", err
	}

	for _, iface := range interfaces {
		if iface.Flags&net.FlagLoopback != 0 || iface.Flags&net.FlagUp == 0 {
			continue
		}

		mac := iface.HardwareAddr.String()
		if mac == "" {
			continue
		}

		addrs, _ := iface.Addrs()
		for _, addr := range addrs {
			ipnet, ok := addr.(*net.IPNet)
			if !ok || ipnet.IP.IsLoopback() || ipnet.IP.To4() == nil {
				continue
			}
			if ipnet.IP.String() == ip {
				return mac, nil
			}
		}
	}
	return "", fmt.Errorf("未找到持有 IP %s 的网卡", ip)
}

// getMACAddress 取第一块可用网卡的 MAC，作为 getMACForIP 匹配失败时的兜底。
// 注意：多网卡 / 虚拟机环境下它可能取到与认证 IP 无关的网卡。
func getMACAddress() (string, error) {
	interfaces, err := net.Interfaces()
	if err != nil {
		return "", err
	}

	for _, iface := range interfaces {
		if iface.Flags&net.FlagLoopback != 0 || iface.Flags&net.FlagUp == 0 {
			continue
		}

		mac := iface.HardwareAddr.String()
		if mac == "" {
			continue
		}

		addrs, _ := iface.Addrs()
		for _, addr := range addrs {
			if ipnet, ok := addr.(*net.IPNet); ok && !ipnet.IP.IsLoopback() && ipnet.IP.To4() != nil {
				return mac, nil
			}
		}
	}
	return "", fmt.Errorf("no active network interface with MAC found")
}

// failKind 探测失败的类别，用于日志与恢复时的统计
type failKind int

const (
	failNone failKind = iota
	failTimeout
	failDNS
	failConn
	failStatus
)

func (k failKind) String() string {
	switch k {
	case failTimeout:
		return "超时"
	case failDNS:
		return "DNS 解析失败"
	case failConn:
		return "连接失败"
	case failStatus:
		return "非 204 响应"
	default:
		return "未知原因"
	}
}

// probeFailure 一次探测失败的详情（成功时为零值）
type probeFailure struct {
	kind   failKind
	detail string // 面向日志：类别 + 具体错误
}

// isNetworkOK 探测出口是否可达；失败时把原因一并带出来，便于排障与统计
func isNetworkOK() (bool, probeFailure) {
	client := &http.Client{
		Timeout: probeTimeout,
	}
	resp, err := client.Get(probeURL)
	if err != nil {
		kind, detail := errKind(err, probeTimeout) // 网络不通 / DNS 故障 / 超时
		return false, probeFailure{kind: kind, detail: detail}
	}
	defer resp.Body.Close()

	if resp.StatusCode != 204 {
		return false, probeFailure{kind: failStatus, detail: fmt.Sprintf("非 204 响应：HTTP %d", resp.StatusCode)}
	}
	return true, probeFailure{}
}

// errKind 归类网络错误，并给出可写进日志的简短描述。timeout 是本次请求的超时预算，
// 用于把超时的原始报错（context deadline exceeded…）换成人话。
//
// 描述里必须去掉 *url.Error 的 URL：它会把整个请求 URL 抄一遍，而登录 URL 带着
// user_password 参数——直接把 err 打进日志等于把密码写进日志（旧版就是这样）。
func errKind(err error, timeout time.Duration) (failKind, string) {
	kind := failConn

	// *url.Error 自己实现了 net.Error，因此超时判定要放在解包之前
	var dnsErr *net.DNSError
	var netErr net.Error
	switch {
	case errors.As(err, &dnsErr):
		kind = failDNS
	case errors.As(err, &netErr) && netErr.Timeout():
		kind = failTimeout
	}

	if kind == failTimeout {
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

// outageStat 一次断网期间按原因累计的失败次数
type outageStat struct {
	probes  int
	timeout int
	dns     int
	conn    int
	status  int
}

func (s *outageStat) reset() { *s = outageStat{} }

func (s *outageStat) add(f probeFailure) {
	s.probes++
	switch f.kind {
	case failTimeout:
		s.timeout++
	case failDNS:
		s.dns++
	case failConn:
		s.conn++
	case failStatus:
		s.status++
	}
}

// summary 拼出"期间失败 N 次：超时 a、连接失败 b"这样的统计；没有任何失败时返回空串
func (s outageStat) summary() string {
	if s.probes == 0 {
		return ""
	}

	parts := make([]string, 0, 4)
	for _, p := range []struct {
		name string
		n    int
	}{
		{failTimeout.String(), s.timeout},
		{failConn.String(), s.conn},
		{failDNS.String(), s.dns},
		{failStatus.String(), s.status},
	} {
		if p.n > 0 {
			parts = append(parts, fmt.Sprintf("%s %d", p.name, p.n))
		}
	}

	detail := fmt.Sprintf("%d 次", s.probes)
	if len(parts) > 0 {
		// 用冒号而不是括号：恢复那行整体已经在括号里，避免套两层括号
		detail += "：" + strings.Join(parts, "、")
	}
	return "；期间探测失败 " + detail
}

// loginOutcome 描述一次登录尝试的结果
type loginOutcome struct {
	status    int    // HTTP 状态码，0 表示请求未能发出
	success   bool   // result == 1
	alreadyUp bool   // msg == "512"：该 IP 已有会话
	msg       string // 服务器返回的 msg；响应无法解析时为原始响应体片段
	err       error
}

func (o loginOutcome) desc() string {
	switch {
	case o.err != nil:
		return fmt.Sprintf("登录请求失败: %v", o.err)
	case o.success:
		return fmt.Sprintf("认证成功（msg=%s）", o.msg)
	case o.alreadyUp:
		return fmt.Sprintf("服务器报告该 IP 已有会话（msg=%s）", o.msg)
	default:
		return fmt.Sprintf("认证未成功（HTTP %d, msg=%s）", o.status, o.msg)
	}
}

type loginResp struct {
	Result  int             `json:"result"`
	Msg     string          `json:"msg"`
	RetCode json.RawMessage `json:"ret_code"` // 可能是数字也可能是字符串，按原样保留
}

// parseLoginResponse 解析形如 dr1003({"result":1,"msg":"…"}); 的 JSONP 响应
func parseLoginResponse(body string) (result int, msg string, ok bool) {
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

func login(cfg *Config, ip, mac string) loginOutcome {
	// 格式化 MAC：去掉冒号，转小写（适配你 bash 脚本的行为）
	cleanMAC := strings.ReplaceAll(strings.ToLower(mac), ":", "")

	userAccount := cfg.User
	if cfg.NetType != "" {
		userAccount = cfg.User + "@" + cfg.NetType
	}

	params := url.Values{
		"callback":       {"dr1003"},
		"login_method":   {"1"},
		"user_account":   {userAccount},
		"user_password":  {cfg.Password},
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

	client := &http.Client{Timeout: loginTimeout}
	resp, err := client.Get(loginURL)
	if err != nil {
		// 只保留归类后的描述：*url.Error 原文会把含 user_password 的完整 URL 抄进消息
		_, detail := errKind(err, loginTimeout)
		return loginOutcome{err: errors.New(detail)}
	}
	defer resp.Body.Close()

	// 限长读取：门户异常时可能返回大页面
	body, err := io.ReadAll(io.LimitReader(resp.Body, 4096))
	if err != nil {
		return loginOutcome{status: resp.StatusCode, err: fmt.Errorf("读取响应体失败: %w", err)}
	}
	bodyStr := strings.TrimSpace(string(body))

	result, msg, ok := parseLoginResponse(bodyStr)
	if !ok {
		// 例如门户返回 HTML 登录页：保留片段便于排障，按未成功处理
		if r := []rune(bodyStr); len(r) > 120 {
			bodyStr = string(r[:120]) + "…"
		}
		return loginOutcome{status: resp.StatusCode, msg: bodyStr}
	}

	return loginOutcome{
		status:    resp.StatusCode,
		success:   result == 1,
		alreadyUp: msg == "512",
		msg:       msg,
	}
}

// nextBackoffIdx 返回退避序列的下一档下标（-1 表示尚未退避过）
func nextBackoffIdx(cur int) int {
	if cur+1 >= len(loginBackoff) {
		return len(loginBackoff) - 1
	}
	return cur + 1
}

// jitter 给退避时长加 ±20% 抖动，避免多台设备同时重试
func jitter(d time.Duration) time.Duration {
	delta := float64(d) * 0.2
	return d + time.Duration((rand.Float64()*2-1)*delta)
}

func getLoginInfo(cfg *Config) (ip, mac string, err error) {
	// 如果启用了路由器模式（两个字段都非空）
	if cfg.RouterIP != "" && cfg.RouterMAC != "" {
		logInfo("🌐 使用路由器模式进行认证")
		return cfg.RouterIP, cfg.RouterMAC, nil
	}

	// 否则使用本机信息
	logInfo("💻 使用本机模式进行认证")
	ip, err = getLocalIP()
	if err != nil {
		return "", "", fmt.Errorf("获取本机IP失败: %w", err)
	}
	mac, err = getMACForIP(ip)
	if err != nil {
		logWarn("⚠️ %v，回退为自动选择网卡", err)
		mac, err = getMACAddress()
		if err != nil {
			return "", "", fmt.Errorf("获取本机MAC失败: %w", err)
		}
	}
	return ip, mac, nil
}

func printHelp() {
	fmt.Println(`广西大学校园网自动登陆程序参数说明：
必须参数：
-user      用户名（必须提供）
-passwd    密码（必须提供）

可选参数：
-nettype   运营商类型（telecom, unicom, cmcc），不加参数则使用校园网
-ip        路由器IP（必须与-mac一起使用）
-mac       路由器MAC（必须与-ip一起使用）
-log       把日志同时写入文件（默认只打印到控制台）
-logfile   日志文件路径（默认程序目录下的 logs/GXU_Net_AutoLogin.log）
-help      显示此帮助信息

示例（Linux）：
./GXU_Net_AutoLogin -user 1807210721 -passwd mypassword
/opt/GXU_Net_AutoLogin/GXU_Net_AutoLogin -user 1807210721 -passwd mypassword -nettype telecom
./GXU_Net_AutoLogin -user 1807210721 -passwd mypassword -log -logfile /var/log/gxu.log
./GXU_Net_AutoLogin -user 1807210721 -passwd mypassword -ip 172.16.6.6 -mac 36:88:8A:99:A4:CC

示例（Windows）：
GXU_Net_AutoLogin.exe -user 1807210721 -passwd mypassword
C:\\Program Files\\GXU_Net_AutoLogin\\GXU_Net_AutoLogin.exe -user 1807210721 -passwd mypassword -nettype telecom
C:\\Program Files\\GXU_Net_AutoLogin\\GXU_Net_AutoLogin.exe -user 1807210721 -passwd mypassword -ip 172.16.6.6 -mac 36:88:8A:99:A4:CC
`)
}

func main() {
	// 定义命令行参数
	var (
		user    string
		passwd  string
		nettype string
		ip      string
		mac     string
		help    bool
		logFlag bool
		logPath string
	)

	flag.StringVar(&user, "user", "", "用户名")
	flag.StringVar(&passwd, "passwd", "", "密码")
	flag.StringVar(&nettype, "nettype", "", "运营商类型（telecom, unicom, cmcc）")
	flag.StringVar(&ip, "ip", "", "路由器IP（必须与-mac一起使用）")
	flag.StringVar(&mac, "mac", "", "路由器MAC（必须与-ip一起使用）")
	flag.BoolVar(&logFlag, "log", false, "把日志同时写入文件")
	flag.StringVar(&logPath, "logfile", "", "日志文件路径（默认程序目录下的 logs/GXU_Net_AutoLogin.log）")
	flag.BoolVar(&help, "help", false, "显示帮助信息")
	flag.Parse()

	// 显示帮助信息（此时还没解析配置，也就没有日志文件）
	if help {
		printHelp()
		os.Exit(0)
	}

	// 解析配置来源。这一步只收集参数、不打印：等日志文件开好之后再统一输出启动信息，
	// 这样日志文件里从第一行起就是完整的一次会话
	var cfg *Config
	source := ""
	switch {
	case user == "" && passwd == "":
		// 从配置文件加载
		c, err := loadConfig()
		if err != nil {
			fmt.Println("❌ 错误:", err)
			fmt.Printf("💡 请编辑 %s 后重新运行本程序。\n", configFileName)
			os.Exit(1)
		}
		cfg, source = c, "配置文件"

	case user != "" && passwd != "":
		// 从命令行参数加载
		// 校验运营商类型
		if nettype != "" {
			valid := false
			switch strings.ToLower(nettype) {
				case "telecom", "unicom", "cmcc":
					valid = true
			}
			if !valid {
				fmt.Printf("❌ 错误：运营商类型必须为telecom, unicom, cmcc（不区分大小写），当前值: %s\n", nettype)
				os.Exit(1)
			}
		}

		// 校验路由器IP/MAC
		if (ip != "" && mac == "") || (ip == "" && mac != "") {
			fmt.Println("❌ 错误：必须同时提供ip和mac参数，两者缺一不可")
			os.Exit(1)
		}

		cfg, source = &Config{
			User:      user,
			Password:  passwd,
			NetType:   nettype,
			RouterIP:  ip,
			RouterMAC: mac,
		}, "命令行参数"

	default:
		// 只提供了其中一个参数
		fmt.Println("❌ 错误：必须同时提供user和passwd参数，或者都不提供（通过配置文件）")
		fmt.Println("💡 请使用 -help 查看参数说明")
		os.Exit(1)
	}

	// 日志：配置文件里的 Log_To_File 与命令行 -log 取并集，-logfile 优先于 Log_File。
	// 两种启动方式都要能开——服务部署走的是命令行参数，根本不读 .env
	logEnabled := cfg.LogToFile || logFlag
	if logPath == "" {
		logPath = cfg.LogPath
	}
	logFilePath := setupLogFile(logEnabled, logPath)

	logInfo("🚀广西大学校园网自动登陆程序 By：GTX690战术核显卡导弹（www.nekopara.uk）")
	logInfo("✅ %s加载成功！", source)
	logInfo("用户: %s", cfg.User)
	logInfo("密码: ******（%d 字符）", len([]rune(cfg.Password)))
	logInfo("运营商: %s", cfg.NetType)
	if cfg.RouterIP != "" && cfg.RouterMAC != "" {
		logInfo("路由器模式: IP=%s, MAC=%s", cfg.RouterIP, cfg.RouterMAC)
	}
	if logFilePath != "" {
		logInfo("📝 日志文件: %s（单文件上限 %d MiB，保留 %d 个备份）", logFilePath, logMaxSize>>20, logBackups)
	}

	// 获取用于登录的 IP 和 MAC（自动判断模式）
	ipAddr, macAddr, err := getLoginInfo(&Config{
		RouterIP:  cfg.RouterIP,
		RouterMAC: cfg.RouterMAC,
	})
	if err != nil {
		logError("❌ %v", err)
		os.Exit(1)
	}

	logInfo("✅ 守护进程启动：认证IP=%s | 认证MAC=%s", ipAddr, macAddr)
	logInfo("   探测 %s（超时 %s｜在线间隔 %s｜失败后 %s｜连续 %d 次失败判定断网）",
		probeURL, probeTimeout, intervalOnline, intervalFast, failThreshold)
	logInfo("   连续断网超过 %.0f 分钟进入静默（探测 %.0f 秒｜每 %.0f 分钟尝试一次登录）",
		quietAfter.Minutes(), quietProbeInterval.Seconds(), quietLoginInterval.Minutes())

	// 主循环：连续失败才判定断网；探测间隔自适应；登录失败按退避重试；
	// 长时间断网（如校园网 00:00-06:00 禁网时段）自动进入静默，避免整夜空转
	loginCfg := &Config{User: cfg.User, Password: cfg.Password, NetType: cfg.NetType}
	fails, fast, backoffIdx := 0, false, -1
	downLogged, quietLogged := false, false
	sessionAssumed := false // 服务器确认过会话（成功或 512）后为 true：此时探测失败不再加速探测
	var nextLogin, outageSince time.Time
	var stat outageStat // 本次断网期间按原因累计的失败次数

	for {
		ok, fail := isNetworkOK()
		if ok {
			if downLogged || quietLogged { // 仅在状态切换时打印，避免刷屏
				logInfo("✅ 网络已恢复（断网持续 %s%s）",
					time.Since(outageSince).Round(time.Second), stat.summary())
			}
			fails, fast, backoffIdx = 0, false, -1
			downLogged, quietLogged, sessionAssumed = false, false, false
			nextLogin, outageSince = time.Time{}, time.Time{}
			stat.reset()
			time.Sleep(intervalOnline)
			continue
		}

		fails++
		if outageSince.IsZero() {
			// 本次断网的第一笔失败：记下起点、清空统计，并把失败原因写出来。
			// 原先只报"判定断网"，看不出是超时、DNS 还是探测端点返回了异常状态码
			outageSince = time.Now()
			stat.reset()
			logWarn("⚠️ 探测失败（%s），连续 %d 次失败即判定断网", fail.detail, failThreshold)
		}
		stat.add(fail)

		// 长时断网静默：探测放慢、登录尝试固定低频，直到网络真的能出去为止。
		// 不按日历判断，因此假期里网络正常时不会进入静默，假期里真断网也能按常规节奏快速恢复。
		if time.Since(outageSince) >= quietAfter {
			if !quietLogged {
				quietLogged = true
				logWarn("🌙 已连续断网 %.0f 分钟，进入静默（探测 %.0f 秒、每 %.0f 分钟尝试一次登录）",
					quietAfter.Minutes(), quietProbeInterval.Seconds(), quietLoginInterval.Minutes())
			}
			if time.Now().After(nextLogin) {
				o := login(loginCfg, ipAddr, macAddr)
				logInfo("· 静默期登录尝试：%s", o.desc())
				nextLogin = time.Now().Add(quietLoginInterval)
			}
			// 睡到"下次探测"与"下次登录尝试"中较早的一个，避免尝试时刻被探测周期推后
			wait := quietProbeInterval
			if d := time.Until(nextLogin); d < wait {
				wait = d
			}
			time.Sleep(wait)
			continue
		}

		// 会话已确认时探测失败更可能来自探测端点本身，保持在线档间隔，避免高频空探
		if !sessionAssumed {
			fast = true
		}
		// 只有"连续失败达到阈值"且"到了本次允许尝试的时间点"才重连：
		// 前者挡掉单次抖动，后者挡掉冷却期内的重复请求。
		if fails >= failThreshold && time.Now().After(nextLogin) {
			if !downLogged {
				downLogged = true
				logWarn("⚠️ 连续 %d 次探测失败，判定断网，开始重连", fails)
			}
			o := login(loginCfg, ipAddr, macAddr)
			switch {
			case o.success:
				// 认证成功：进入冷却期，避免探测端点异常时反复登录
				logInfo("✅ %s", o.desc())
				fails, backoffIdx = 0, -1
				sessionAssumed, fast = true, false
				nextLogin = time.Now().Add(loginCooldown)
			case o.alreadyUp:
				// 该 IP 已有会话：探测失败更可能来自探测端点本身，放慢节奏
				logInfo("ℹ️ %s，%s 后才会再次尝试登录，期间继续探测", o.desc(), sessionRecheck)
				sessionAssumed, fast = true, false
				nextLogin = time.Now().Add(sessionRecheck)
			default:
				backoffIdx = nextBackoffIdx(backoffIdx)
				d := jitter(loginBackoff[backoffIdx])
				nextLogin = time.Now().Add(d)
				logWarn("⚠️ %s，%s 后重试", o.desc(), d.Round(time.Second))
			}
		}

		interval := intervalOnline
		if fast {
			interval = intervalFast
		}
		time.Sleep(interval)
	}
}
