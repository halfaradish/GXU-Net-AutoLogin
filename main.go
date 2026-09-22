// 广西大学校园网自动登陆程序（命令行版）。
//
// 行为与最早的版本一致：读 .env 或命令行参数，常驻探测并自动重连。
// 核心逻辑放在 internal/ 下，图形（托盘）版在 tray/ 目录，两者共用同一份 .env。
package main

import (
	"flag"
	"fmt"
	"os"
	"os/signal"
	"syscall"

	"github.com/halfaradish/GXU-Net-AutoLogin/internal/config"
	"github.com/halfaradish/GXU-Net-AutoLogin/internal/daemon"
	"github.com/halfaradish/GXU-Net-AutoLogin/internal/logging"
	"github.com/halfaradish/GXU-Net-AutoLogin/internal/netauth"
)

// version 由打包脚本通过 -ldflags "-X main.version=…" 注入
var version = "dev"

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
C:\\Program Files\\GXU_Net_AutoLogin\\GXU_Net_AutoLogin.exe -user 1807210721 -passwd mypassword -ip 172.16.6.6 -mac 36:88:8A:99:A4:CC`)
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
	var cfg *config.Config
	source := ""
	switch {
	case user == "" && passwd == "":
		// 从配置文件加载
		c, err := config.Load("")
		if err != nil {
			fmt.Println("错误:", err)
			fmt.Printf("请编辑 %s 后重新运行本程序。\n", config.FileName)
			os.Exit(1)
		}
		cfg, source = c, "配置文件"

	case user != "" && passwd != "":
		// 从命令行参数加载
		if !config.ValidNetType(nettype) {
			fmt.Printf("错误：运营商类型必须为telecom, unicom, cmcc（不区分大小写），当前值: %s\n", nettype)
			os.Exit(1)
		}
		if (ip != "" && mac == "") || (ip == "" && mac != "") {
			fmt.Println("错误：必须同时提供ip和mac参数，两者缺一不可")
			os.Exit(1)
		}
		cfg, source = &config.Config{
			User:      user,
			Password:  passwd,
			NetType:   nettype,
			RouterIP:  ip,
			RouterMAC: mac,
		}, "命令行参数"

	default:
		// 只提供了其中一个参数
		fmt.Println("错误：必须同时提供user和passwd参数，或者都不提供（通过配置文件）")
		fmt.Println("请使用 -help 查看参数说明")
		os.Exit(1)
	}

	// 日志：配置文件里的 LOG_TO_FILE 与命令行 -log 取并集，-logfile 优先于 LOG_FILE。
	// 两种启动方式都要能开——服务部署走的是命令行参数，根本不读 .env
	logEnabled := cfg.LogToFile || logFlag
	if logPath == "" {
		logPath = cfg.LogPath
	}

	log := logging.New(logging.Options{Console: os.Stdout})
	defer log.Close()

	logFilePath := ""
	if logEnabled {
		abs, err := log.RetargetFile(logPath)
		if err != nil {
			log.Warn("%v（仅打印到控制台）", err)
		} else {
			logFilePath = abs
		}
	}

	log.Info("广西大学校园网自动登陆程序 By：GTX690战术核显卡导弹（www.nekopara.uk）")
	log.Info("%s加载成功！", source)
	log.Info("用户: %s", cfg.User)
	log.Info("密码: ******（%d 字符）", len([]rune(cfg.Password)))
	log.Info("运营商: %s", cfg.NetType)
	if cfg.RouterIP != "" && cfg.RouterMAC != "" {
		log.Info("路由器模式: IP=%s, MAC=%s", cfg.RouterIP, cfg.RouterMAC)
	}
	if logFilePath != "" {
		log.Info("日志文件: %s（单文件上限 %d MiB，保留 %d 个备份）", logFilePath, log.MaxSizeMiB(), log.Backups())
	}

	// 启动守护：解析认证 IP/MAC 并进入探测循环（失败即退出，与原行为一致）
	d := daemon.New(log, cfg)
	if err := d.Start(); err != nil {
		log.Error("%v", err)
		os.Exit(1)
	}

	t := daemon.DefaultTiming()
	log.Info("   探测 %s（超时 %s｜在线间隔 %s｜失败后 %s｜连续 %d 次失败判定断网）",
		netauth.ProbeURL, netauth.ProbeTimeout, t.IntervalOnline, t.IntervalFast, t.FailThreshold)
	log.Info("   连续断网超过 %.0f 分钟进入静默（探测 %.0f 秒｜每 %.0f 分钟尝试一次登录）",
		t.QuietAfter.Minutes(), t.QuietProbeInterval.Seconds(), t.QuietLoginInterval.Minutes())

	// 常驻运行，收到退出信号（Ctrl+C / SIGTERM）时优雅停止
	sig := make(chan os.Signal, 1)
	signal.Notify(sig, os.Interrupt, syscall.SIGTERM)
	<-sig

	log.Info("收到退出信号，正在停止")
	d.Stop()
}
