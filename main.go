package main

import (
	"bufio"
	"encoding/json"
	"fmt"
	"math/rand"
	"os"
	"strings"
	"net/http"
	"net"
	"net/url"
	"time"
	"io"
	"flag"
)

const (
	configFileName = "config.txt"

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
)

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
}

// loadConfig 加载或创建配置文件
func loadConfig() (*Config, error) {
	// 检查文件是否存在
	if _, err := os.Stat(configFileName); os.IsNotExist(err) {
		// 创建默认模板
		defaultContent := `# 校园网登陆脚本信息设置：（注意请不要改变格式）
# 用户名：（填写示例：User=1807210721）
User=
# 密码：（填写示例：Password=www.nekopara.uk）
Password=
# 运营商选择，留空选择校园网，如果需要选择运营商，电信填写telecom，联通填写unicom，移动填写cmcc
Net_Type=
# 开启路由器登陆模式：
# 如果填写以下两个参数（均非空），则使用指定的路由器IP和MAC进行认证。
# 否则使用本机IP和MAC。
# 示例：
# Router_IP=172.16.6.6
# Router_MAC=36:88:8A:99:A4:CC
Router_IP=
Router_MAC=
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

		switch key {
			case "User":
				cfg.User = value
			case "Password":
				cfg.Password = value
			case "Net_Type":
				cfg.NetType = value // 新增这一行
			case "Router_IP":
				cfg.RouterIP = value
			case "Router_MAC":
				cfg.RouterMAC = value
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

func isNetworkOK() bool {
	client := &http.Client{
		Timeout: probeTimeout,
	}
	resp, err := client.Get(probeURL)
	if err != nil {
		return false // 网络不通 / DNS 故障 / 超时
	}
	defer resp.Body.Close()

	return resp.StatusCode == 204
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
		return loginOutcome{err: err}
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
		fmt.Println("🌐 使用路由器模式进行认证")
		return cfg.RouterIP, cfg.RouterMAC, nil
	}

	// 否则使用本机信息
	fmt.Println("💻 使用本机模式进行认证")
	ip, err = getLocalIP()
	if err != nil {
		return "", "", fmt.Errorf("获取本机IP失败: %w", err)
	}
	mac, err = getMACForIP(ip)
	if err != nil {
		fmt.Printf("⚠️ %v，回退为自动选择网卡\n", err)
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
-help      显示此帮助信息

示例（Linux）：
./GXU_Net_AutoLogin -user 1807210721 -passwd mypassword
/opt/GXU_Net_AutoLogin/GXU_Net_AutoLogin -user 1807210721 -passwd mypassword -nettype telecom
./GXU_Net_AutoLogin -user 1807210721 -passwd mypassword -ip 172.16.6.6 -mac 36:88:8A:99:A4:CC

示例（Windows）：
GXU_Net_AutoLogin.exe -user 1807210721 -passwd mypassword
C:\\Program Files\\GXU_Net_AutoLogin\\GXU_Net_AutoLogin.exe -user 1807210721 -passwd mypassword -nettype telecom
C:\\Program Files\\GXU_Net_AutoLogin\\GXU_Net_AutoLogin.exe -user 1807210721 -passwd mypassword -ip 172.16.6.6 -mac 36:88:8A:99:A4:CC
`)
}

func main() {
	fmt.Printf("🚀广西大学校园网自动登陆程序 By：GTX690战术核显卡导弹（www.nekopara.uk）\n")
	// 定义命令行参数
	var (
		user    string
		passwd  string
		nettype string
		ip      string
		mac     string
		help    bool
	)

	flag.StringVar(&user, "user", "", "用户名")
	flag.StringVar(&passwd, "passwd", "", "密码")
	flag.StringVar(&nettype, "nettype", "", "运营商类型（telecom, unicom, cmcc）")
	flag.StringVar(&ip, "ip", "", "路由器IP（必须与-mac一起使用）")
	flag.StringVar(&mac, "mac", "", "路由器MAC（必须与-ip一起使用）")
	flag.BoolVar(&help, "help", false, "显示帮助信息")
	flag.Parse()

	// 显示帮助信息
	if help {
		printHelp()
		os.Exit(0)
	}

	// 检查必须参数
	if (user == "" && passwd == "") {
		// 从配置文件加载
		cfg, err := loadConfig()
		if err != nil {
			fmt.Println("❌ 错误:", err)
			fmt.Println("💡 请编辑 config.txt 后重新运行本程序。")
			os.Exit(1)
		}
		fmt.Printf("✅ 配置加载成功！\n")
		fmt.Printf("用户: %s\n", cfg.User)
		fmt.Printf("密码: ******（%d 字符）\n", len([]rune(cfg.Password)))
		fmt.Printf("运营商: %s\n", cfg.NetType)
		if cfg.RouterIP != "" && cfg.RouterMAC != "" {
			fmt.Printf("路由器模式: IP=%s, MAC=%s\n", cfg.RouterIP, cfg.RouterMAC)
		}

		// 修复：将配置文件中的值赋给命令行变量
		user = cfg.User
		passwd = cfg.Password
		nettype = cfg.NetType
		ip = cfg.RouterIP
		mac = cfg.RouterMAC
	} else if user != "" && passwd != "" {
		// 从命令行参数加载
		cfg := &Config{
			User:      user,
			Password:  passwd,
			NetType:   nettype,
			RouterIP:  ip,
			RouterMAC: mac,
		}

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

		// 显示配置
		fmt.Printf("✅ 命令行参数加载成功！\n")
		fmt.Printf("用户: %s\n", cfg.User)
		fmt.Printf("密码: ******（%d 字符）\n", len([]rune(cfg.Password)))
		fmt.Printf("运营商: %s\n", cfg.NetType)
		if cfg.RouterIP != "" && cfg.RouterMAC != "" {
			fmt.Printf("路由器模式: IP=%s, MAC=%s\n", cfg.RouterIP, cfg.RouterMAC)
		}

		// 使用命令行参数配置
		cfg.User = user
		cfg.Password = passwd
		cfg.NetType = nettype
		cfg.RouterIP = ip
		cfg.RouterMAC = mac
	} else {
		// 只提供了其中一个参数
		fmt.Println("❌ 错误：必须同时提供user和passwd参数，或者都不提供（通过配置文件）")
		fmt.Println("💡 请使用 -help 查看参数说明")
		os.Exit(1)
	}

	// 获取用于登录的 IP 和 MAC（自动判断模式）
	ipAddr, macAddr, err := getLoginInfo(&Config{
		RouterIP:  ip,
		RouterMAC: mac,
	})
	if err != nil {
		fmt.Printf("❌ %v\n", err)
		os.Exit(1)
	}

	fmt.Printf("✅ 守护进程启动：认证IP=%s | 认证MAC=%s\n", ipAddr, macAddr)
	fmt.Printf("   探测 %s\n", probeURL)
	fmt.Printf("   超时 %s｜在线间隔 %s｜失败后 %s｜连续 %d 次失败判定断网\n",
		probeTimeout, intervalOnline, intervalFast, failThreshold)

	// 主循环：连续失败才判定断网；探测间隔自适应；登录失败按退避重试
	loginCfg := &Config{User: user, Password: passwd, NetType: nettype}
	fails, fast, backoffIdx := 0, false, -1
	downLogged := false
	sessionAssumed := false // 服务器确认过会话（成功或 512）后为 true：此时探测失败不再加速探测
	var nextLogin, downSince time.Time

	for {
		if isNetworkOK() {
			if downLogged { // 仅在状态切换时打印，避免刷屏
				fmt.Printf("✅ 网络已恢复（断网持续 %s）\n", time.Since(downSince).Round(time.Second))
			}
			fails, fast, backoffIdx, downLogged = 0, false, -1, false
			sessionAssumed = false
			nextLogin, downSince = time.Time{}, time.Time{}
		} else {
			fails++
			// 会话已确认时探测失败更可能来自探测端点本身，保持在线档间隔，避免高频空探
			if !sessionAssumed {
				fast = true
			}
			// 只有"连续失败达到阈值"且"到了本次允许尝试的时间点"才重连：
			// 前者挡掉单次抖动，后者挡掉冷却期内的重复请求。
			if fails >= failThreshold && time.Now().After(nextLogin) {
				if !downLogged {
					downLogged = true
					downSince = time.Now()
					fmt.Printf("⚠️ 连续 %d 次探测失败，判定断网，开始重连\n", fails)
				}
				o := login(loginCfg, ipAddr, macAddr)
				switch {
				case o.success:
					// 认证成功：进入冷却期，避免探测端点异常时反复登录
					fmt.Printf("✅ %s\n", o.desc())
					fails, backoffIdx = 0, -1
					downLogged, downSince = false, time.Time{}
					sessionAssumed, fast = true, false
					nextLogin = time.Now().Add(loginCooldown)
				case o.alreadyUp:
					// 该 IP 已有会话：探测失败更可能来自探测端点本身，放慢节奏
					fmt.Printf("ℹ️ %s，%s 后才会再次尝试登录，期间继续探测\n", o.desc(), sessionRecheck)
					sessionAssumed, fast = true, false
					nextLogin = time.Now().Add(sessionRecheck)
				default:
					backoffIdx = nextBackoffIdx(backoffIdx)
					d := jitter(loginBackoff[backoffIdx])
					nextLogin = time.Now().Add(d)
					fmt.Printf("⚠️ %s，%s 后重试\n", o.desc(), d.Round(time.Second))
				}
			}
		}

		interval := intervalOnline
		if fast {
			interval = intervalFast
		}
		time.Sleep(interval)
	}
}
