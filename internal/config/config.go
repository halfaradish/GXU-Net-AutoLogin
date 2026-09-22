// Package config 负责 .env 的解析、校验与写回。
//
// 格式沿用最初的手写 .env：键名大小写不敏感、值可以用引号包起来、未识别的键
// 静默忽略——老版本二进制读到新版本写出的文件也不会出错。
package config

import (
	"bufio"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

const (
	// FileName 是配置文件名
	FileName = ".env"

	// LegacyFileName 是老版本用的配置文件名；只在提示改名时用到
	LegacyFileName = "config.txt"

	// Mode 是配置文件的权限
	Mode = 0644
)

// Config 保存一次认证所需的全部设置
type Config struct {
	User     string
	Password string
	NetType  string // 空 = 校园网，否则 telecom / unicom / cmcc

	// 路由器模式（两个字段都非空时启用）
	RouterIP  string
	RouterMAC string

	// 日志
	LogToFile bool
	LogPath   string

	// 手动指定的认证 MAC；空 = 自动使用认证 IP 所在网卡的 MAC
	MacAddress string

	// ── 以下仅托盘版使用，命令行版读取但不做处理 ──
	Autostart      bool // 开机自启动
	MinimizeToTray bool // 关闭窗口时最小化至托盘
	StartMinimized bool // 启动后不弹窗
}

// Defaults 返回高级选项的初始默认值（不含账号密码）
func Defaults() *Config {
	return &Config{
		MinimizeToTray: true,
		StartMinimized: true,
	}
}

// Path 返回配置文件的完整路径；dir 为空时退化为当前目录下的 .env
func Path(dir string) string {
	if dir == "" {
		return FileName
	}
	return filepath.Join(dir, FileName)
}

// Read 解析配置文件。文件不存在时返回的错误满足 errors.Is(err, os.ErrNotExist)，
// 不做模板创建、也不做必填校验——托盘首启需要的是"空配置"而不是一个失败。
func Read(dir string) (*Config, error) {
	content, err := os.ReadFile(Path(dir))
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, fmt.Errorf("%s: %w", Path(dir), os.ErrNotExist)
		}
		return nil, fmt.Errorf("无法读取配置文件: %v", err)
	}
	return Parse(string(content))
}

// Load 按命令行版的语义加载配置：文件不存在时创建模板并报错，发现旧的
// config.txt 时提示改名，最后做必填校验。
func Load(dir string) (*Config, error) {
	cfg, err := Read(dir)
	if err == nil {
		if verr := Validate(cfg); verr != nil {
			return nil, verr
		}
		return cfg, nil
	}
	if !errors.Is(err, os.ErrNotExist) {
		return nil, err
	}

	// 老版本用的是 config.txt：提示改名，别让已有账号密码看起来"丢了"
	if _, legacy := os.Stat(filepath.Join(dirOrDot(dir), LegacyFileName)); legacy == nil {
		return nil, fmt.Errorf("配置已改用 %s，但检测到旧的 %s：请改成 %s 后重新运行（键名大小写不限）",
			FileName, LegacyFileName, FileName)
	}

	if werr := Save(dir, Defaults()); werr != nil {
		return nil, fmt.Errorf("无法创建配置文件: %v", werr)
	}
	return nil, fmt.Errorf("未找到配置文件，配置文件 '%s' 已创建，请先填写上网信息后重新运行程序", FileName)
}

// Save 把配置原子地写回 <dir>/.env：先写同目录下的临时文件再改名，
// 中途失败不会留下半个文件把原有配置弄坏。
func Save(dir string, cfg *Config) error {
	path := Path(dir)
	tmp := path + ".tmp"

	if err := os.WriteFile(tmp, []byte(Render(cfg)), Mode); err != nil {
		return err
	}
	if err := os.Rename(tmp, path); err != nil {
		os.Remove(tmp)
		return err
	}
	return nil
}

// Validate 检查必填项（账号、密码）与运营商取值
func Validate(cfg *Config) error {
	if cfg.User == "" || cfg.Password == "" {
		return fmt.Errorf("请在 '%s' 中填写用户名和密码", FileName)
	}
	if !ValidNetType(cfg.NetType) {
		return fmt.Errorf("错误：运营商类型必须为空、telecom、unicom或cmcc（不区分大小写），当前值: %s", cfg.NetType)
	}
	return nil
}

// ValidNetType 报告运营商取值是否合法（空、telecom、unicom、cmcc，大小写不限）
func ValidNetType(t string) bool {
	switch strings.ToLower(t) {
	case "", "telecom", "unicom", "cmcc":
		return true
	}
	return false
}

// NetTypeAccount 把账号与运营商拼成认证用的 user_account
func NetTypeAccount(user, netType string) string {
	if netType == "" {
		return user
	}
	return user + "@" + netType
}

// Parse 解析 .env 内容；未识别的键忽略，格式错误的行跳过
func Parse(content string) (*Config, error) {
	cfg := Defaults()

	scanner := bufio.NewScanner(strings.NewReader(content))
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
		case "mac_address":
			cfg.MacAddress = value
		case "log_file":
			cfg.LogPath = value
		case "log_to_file":
			// 留空 = 不写文件，与新增该键之前的行为一致
			v, ok, err := parseBool(key, value, "留空或 false 只打印到控制台")
			if err != nil {
				return nil, err
			}
			cfg.LogToFile = ok && v
		case "autostart":
			v, ok, err := parseBool(key, value, "留空 = 关闭")
			if err != nil {
				return nil, err
			}
			if ok {
				cfg.Autostart = v
			}
		case "minimize_to_tray":
			v, ok, err := parseBool(key, value, "留空 = 开启")
			if err != nil {
				return nil, err
			}
			if ok {
				cfg.MinimizeToTray = v
			}
		case "start_minimized":
			v, ok, err := parseBool(key, value, "留空 = 开启")
			if err != nil {
				return nil, err
			}
			if ok {
				cfg.StartMinimized = v
			}
		}
	}

	if err := scanner.Err(); err != nil {
		return nil, err
	}
	return cfg, nil
}

// parseBool 解析开关值；第二个返回值为 false 表示该键没写值，调用方应保留默认值
func parseBool(key, value, hint string) (bool, bool, error) {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "":
		return false, false, nil
	case "true", "1", "yes", "on":
		return true, true, nil
	case "false", "0", "no", "off":
		return false, true, nil
	default:
		return false, false, fmt.Errorf("错误：%s 只能填 true 或 false（%s），当前值: %s", key, hint, value)
	}
}

// Render 生成写回磁盘的内容：模板的注释保留原样，值按当前配置填好
func Render(cfg *Config) string {
	var b strings.Builder

	b.WriteString("# 校园网登陆脚本信息设置：（注意请不要改变格式）\n")
	b.WriteString("# 用户名：（填写示例：USER=1807210721）\n")
	fmt.Fprintf(&b, "USER=%s\n", quote(cfg.User))
	b.WriteString("# 密码：（填写示例：PASSWORD=www.nekopara.uk）\n")
	fmt.Fprintf(&b, "PASSWORD=%s\n", quote(cfg.Password))
	b.WriteString("# 运营商选择，留空选择校园网，如果需要选择运营商，电信填写telecom，联通填写unicom，移动填写cmcc\n")
	fmt.Fprintf(&b, "NET_TYPE=%s\n", quote(cfg.NetType))
	b.WriteString("# 开启路由器登陆模式：\n")
	b.WriteString("# 如果填写以下两个参数（均非空），则使用指定的路由器IP和MAC进行认证。\n")
	b.WriteString("# 否则使用本机IP和MAC。\n")
	b.WriteString("# 示例：\n")
	b.WriteString("# ROUTER_IP=172.16.6.6\n")
	b.WriteString("# ROUTER_MAC=36:88:8A:99:A4:CC\n")
	fmt.Fprintf(&b, "ROUTER_IP=%s\n", quote(cfg.RouterIP))
	fmt.Fprintf(&b, "ROUTER_MAC=%s\n", quote(cfg.RouterMAC))
	b.WriteString("# 手动指定认证用的 MAC：（留空 = 自动使用认证 IP 所在网卡的 MAC）\n")
	fmt.Fprintf(&b, "MAC_ADDRESS=%s\n", quote(cfg.MacAddress))
	b.WriteString("# 日志：（是否把日志同时写入文件，留空或 false 只打印到控制台）\n")
	fmt.Fprintf(&b, "LOG_TO_FILE=%t\n", cfg.LogToFile)
	b.WriteString("# 日志文件路径：（留空则使用程序目录下的 logs/GXU_Net_AutoLogin.log）\n")
	fmt.Fprintf(&b, "LOG_FILE=%s\n", quote(cfg.LogPath))
	b.WriteString("# 以下为托盘版设置（命令行版会忽略）：\n")
	b.WriteString("# 开机自启动：\n")
	fmt.Fprintf(&b, "AUTOSTART=%t\n", cfg.Autostart)
	b.WriteString("# 关闭窗口时最小化至托盘：（false = 直接退出程序）\n")
	fmt.Fprintf(&b, "MINIMIZE_TO_TRAY=%t\n", cfg.MinimizeToTray)
	b.WriteString("# 启动后不弹窗：（true = 静默驻留托盘；还没填账号时无论如何都会弹窗）\n")
	fmt.Fprintf(&b, "START_MINIMIZED=%t\n", cfg.StartMinimized)

	return b.String()
}

// quote 在必要时用双引号包住取值，避免含 # 或首尾空格的密码写出去就再也读不回来
func quote(v string) string {
	if v == "" {
		return ""
	}
	if strings.ContainsAny(v, "#\"'") || strings.TrimSpace(v) != v {
		return `"` + v + `"`
	}
	return v
}

func dirOrDot(dir string) string {
	if dir == "" {
		return "."
	}
	return dir
}
