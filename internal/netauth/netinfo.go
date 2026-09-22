package netauth

import (
	"fmt"
	"net"
	"strings"
)

// MAC 来源，界面上用它说明当前用的 MAC 是从哪来的
const (
	SourceAuto     = "自动检测" // 认证 IP 所在网卡
	SourceManual   = "手动指定" // 配置里写死的 MAC
	SourceFallback = "自动选择" // 没匹配到网卡时退而取的第一块网卡
	SourceRouter   = "路由器"  // 路由器模式下由配置指定
)

// Warnf 用于把"回退为自动选择网卡"这类非致命提示交给调用方输出
type Warnf func(format string, args ...any)

// Identity 是这次认证使用的 IP、MAC 以及 MAC 的来源
type Identity struct {
	IP     string
	MAC    string
	Source string
}

// Adapter 是一块可用于认证的网卡
type Adapter struct {
	Name string
	IP   string
	MAC  string
}

// LocalIP 通过一次 UDP 拨号取本机出口 IP
func LocalIP() (string, error) {
	conn, err := net.Dial("udp", "8.8.8.8:80")
	if err != nil {
		return "", err
	}
	defer conn.Close()
	return conn.LocalAddr().(*net.UDPAddr).IP.String(), nil
}

// MACForIP 取持有该 IP 的网卡 MAC
func MACForIP(ip string) (string, error) {
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

// FirstMAC 取第一块可用网卡的 MAC，作为 MACForIP 匹配失败时的兜底。
// 注意：多网卡 / 虚拟机环境下它可能取到与认证 IP 无关的网卡。
func FirstMAC() (string, error) {
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
	return "", fmt.Errorf("未找到可用的网卡 MAC")
}

// ListAdapters 列出已启用、非回环且有 IPv4 地址的网卡，供界面选择 MAC
func ListAdapters() []Adapter {
	interfaces, err := net.Interfaces()
	if err != nil {
		return nil
	}

	var out []Adapter
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
			out = append(out, Adapter{Name: iface.Name, IP: ipnet.IP.String(), MAC: strings.ToLower(mac)})
		}
	}
	return out
}

// ResolveIdentity 解析这次认证要用的 IP 与 MAC：
// 路由器模式（两个字段都非空）用指定的 IP/MAC；否则用本机 IP，
// MAC 依次尝试 手动指定 → 认证 IP 所在网卡 → 第一块可用网卡。
func ResolveIdentity(routerIP, routerMAC, macOverride string, warn Warnf) (Identity, error) {
	if routerIP != "" && routerMAC != "" {
		return Identity{IP: routerIP, MAC: routerMAC, Source: SourceRouter}, nil
	}

	ip, err := LocalIP()
	if err != nil {
		return Identity{}, fmt.Errorf("获取本机IP失败: %w", err)
	}

	if macOverride != "" {
		return Identity{IP: ip, MAC: macOverride, Source: SourceManual}, nil
	}

	mac, err := MACForIP(ip)
	if err != nil {
		if warn != nil {
			warn("%v，回退为自动选择网卡", err)
		}
		mac, err = FirstMAC()
		if err != nil {
			return Identity{}, fmt.Errorf("获取本机MAC失败: %w", err)
		}
		return Identity{IP: ip, MAC: mac, Source: SourceFallback}, nil
	}
	return Identity{IP: ip, MAC: mac, Source: SourceAuto}, nil
}

// NormalizeMAC 把输入的 MAC 规整成 aa:bb:cc:dd:ee:ff，供界面校验输入框
func NormalizeMAC(s string) (string, error) {
	hw, err := net.ParseMAC(strings.TrimSpace(s))
	if err != nil || len(hw) != 6 {
		return "", fmt.Errorf("MAC 地址格式不正确：%s", s)
	}
	return strings.ToLower(hw.String()), nil
}
