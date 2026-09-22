# 🌐 GXU-Net-AutoLogin

> 广西大学校园网自动登录 & 断网重连守护程序  
> 路由器模式 · 运营商选择 · 配置文件/命令行双模式

[![License: AGPL v3](https://img.shields.io/badge/License-AGPL%20v3-blue.svg)](https://www.gnu.org/licenses/agpl-3.0)
![Go Version](https://img.shields.io/badge/Go-1.20%2B-informational)
![Platform](https://img.shields.io/badge/Platform-Linux%20|%20Windows-lightgrey)

广西大学校园网在高峰期常会断连，手动重登既麻烦又影响挂机任务。本程序使用 **Go 语言** 编写，轻量高效，可实现：
- ✅ **断网自动检测并重连**
- ✅ **支持校园网 + 三大运营商（电信/联通/移动）**
- ✅ **路由器模式：指定 IP/MAC 登录（适配宿舍共享上网）**
- ✅ **配置文件 or 命令行参数，灵活部署**
- ✅ **Windows 托盘版：图形界面配置、状态与日志一目了然（[见下文](#-windows-托盘版)）**

---

## 📦 快速开始

### 方法一：使用预编译二进制

1. 从 [Release](https://github.com/Chocola-X/GXU-Net-AutoLogin/releases) 下载对应的二进制文件
2. 运行 `GXU_Net_AutoLogin`
3. 首次运行会生成 `.env`，按提示填写账号密码（可参考 `.env.example`）
4. 再次运行即可后台守护

### 方法二：从源码编译（推荐 Linux 用户）

确保已安装 [Go 1.20+](https://golang.org/dl/)

```bash
git clone https://github.com/Chocola-X/GXU-Net-AutoLogin.git
cd GXU-Net-AutoLogin
go build -ldflags="-s -w" -o GXU_Net_AutoLogin main.go
./GXU_Net_AutoLogin
```

首次运行将自动生成 `.env`，编辑后重新运行即可。

多平台发布用 [GoReleaser](https://goreleaser.com)（`.goreleaser.yaml`）：`goreleaser release --snapshot --clean` 本地出全部平台产物到 `dist/`；push `v*` tag 时 `.github/workflows/release.yml` 自动构建并发 Release（资产带版本号）。

---

## ⚙️ 配置说明

### 方式 1：配置文件（`.env`）

程序首次运行会自动生成 `.env` 模板（内容与仓库里的 `.env.example` 一致），也可以直接复制示例文件改名：

```bash
cp .env.example .env    # 然后填写 USER / PASSWORD
```

```ini
# 校园网登录脚本信息设置：（注意请不要改变格式）
USER=1807210721
PASSWORD=your_password_here
# 运营商：留空=校园网，telecom=电信，unicom=联通，cmcc=移动
NET_TYPE=cmcc
# 路由器模式（两者需同时填写才生效）：
ROUTER_IP=172.16.6.6
ROUTER_MAC=36:88:8A:99:A4:CC
# 手动指定认证 MAC：留空=自动取认证 IP 所在网卡的 MAC
MAC_ADDRESS=
# 日志：留空/false=只打印到控制台，true=同时写入文件
LOG_TO_FILE=true
# 日志文件路径：留空=程序目录下的 logs/GXU_Net_AutoLogin.log
LOG_FILE=
# 下面三项只对 Windows 托盘版生效（命令行版忽略）
AUTOSTART=false          # 开机自启动
MINIMIZE_TO_TRAY=true    # 关闭窗口时最小化至托盘
START_MINIMIZED=true     # 启动后不显示主界面（驻留托盘，启动时弹一次气泡）
```

> 💡 **键名不区分大小写**（`USER=` 与 `User=` 等价），值也可以用引号包起来（`PASSWORD="xxx"`）；
> 旧版的 `config.txt` 改名成 `.env` 即可继续用。

> 💡 日志文件会**自动轮转**：单文件写满 5 MiB 后更名为 `.log.1`，旧份依次后移，只保留最近的 `.log.1`、`.log.2`，长期挂机不会撑满磁盘。日志里不会出现明文密码。

### 方式 2：命令行参数（适合服务部署）

```bash
# 基础用法
./GXU_Net_AutoLogin -user 1807210721 -passwd your_password

# 完整示例（含运营商+路由器）
./GXU_Net_AutoLogin \
  -user 1807210721 \
  -passwd mypassword \
  -nettype cmcc \
  -ip 172.16.6.6 \
  -mac 36:88:8A:99:A4:CC

# 把日志同时写入文件（服务部署建议指定绝对路径）
./GXU_Net_AutoLogin -user 1807210721 -passwd mypassword -log -logfile /var/log/gxu-net-autologin.log
```

> 💡 **注意**：`-ip` 和 `-mac` 必须**同时提供**，否则视为无效。
>
> 💡 用 systemd 部署时也可以不开文件日志，直接 `journalctl -u gxu-net-autologin -f` 看输出（stdout 一直都有日志）。

查看全部参数：
```bash
./GXU_Net_AutoLogin -help
```

---

## 🖥️ Windows 托盘版

不想碰命令行、也不想每次带 `-user/-passwd` 参数的用户可以用托盘版：常驻系统托盘，账号密码在界面里填一次，改完**保存并应用**立刻生效。

### 打包与运行

```bat
cd tray && go build -ldflags="-s -w -H=windowsgui" -o GXU_Net_AutoLogin_Tray.exe .
```

发布版用 GoReleaser 出 `GXU_Net_AutoLogin_<版本>_windows_amd64.zip`（`dist/`），zip 里同时带命令行版与托盘版两个 exe：

| 文件 | 说明 |
|---|---|
| `GXU_Net_AutoLogin.exe` | 命令行版（与原版行为一致；Linux 也用这个） |
| `GXU_Net_AutoLogin_Tray.exe` | 托盘版，GUI 子系统，没有黑窗口 |

把 `GXU_Net_AutoLogin_Tray.exe` 放到想长期存放的目录，双击即可。首次运行会弹出主界面让你填账号密码；之后按「启动后不显示主界面」的设置静默驻留托盘（此时会弹一次气泡告诉你它在跑）。

> 💡 **配置目录**：优先用**启动时的工作目录**里的 `.env`，没有才用 exe 所在目录。

### 界面

- **基础配置**：校园网账号、密码、运营商
- **高级选项**（默认收起，点「高级选项 ▾」展开，开合状态会记住）：开机自启动、关闭窗口时最小化至托盘、启动后不显示主界面（驻留托盘）、自定义日志目录（文件名固定为 `GXU_Net_AutoLogin.log`）、路由器 IP/MAC、MAC 地址（自动 / 从网卡列表选 / 手动输入）
- **运行状态**：连接状态（在线 / 断网 / 静默 / 认证中）、认证 IP 与 MAC（含来源）、路由器信息、最近一次认证结果、本次在线与断网时长、累计断网次数、当前探测间隔、下次登录倒计时
- **日志**：内置实时日志（按级别与关键字过滤、暂停滚动），以及「打开日志文件 / 打开目录 / 清空日志」
- **按钮**：保存并应用、立即重连、恢复默认配置、退出
- **托盘图标**：左键点击打开主界面；右键菜单为 显示主界面 / 立即重连 / 打开日志文件 / 打开日志目录 / 退出。**气泡提醒只在两个时刻出现**：启动后驻留托盘时、关闭窗口最小化到托盘时各一次；断线、恢复、进入静默**不弹窗**，状态看主界面「运行状态」区或图标悬停提示，过程记在日志里

### 行为约定

- **保存并应用**：写回 `.env` → 同步开机自启注册表项 → 重建探测循环 → **立即探测并发起一次认证**，全程不重启进程
- **关闭窗口**：默认最小化到托盘继续守护；取消勾选「关闭窗口时最小化至托盘」后，点 X 即退出
- **恢复默认配置**：只重置上面那 6 项高级设置，账号、密码、运营商不动，并且立即生效
- **单实例**：重复启动不会起第二个进程，只会把已有实例的主界面唤出来
- **开机自启**：写入 `HKCU\Software\Microsoft\Windows\CurrentVersion\Run`（当前用户，不需要管理员）；每次启动按配置重写一遍，程序挪了位置也能自愈
- **日志**：托盘版没有控制台，日志一律写文件（默认 `logs\GXU_Net_AutoLogin.log`，5 MiB 轮转保留 2 份）

托盘版还认这几个 `.env` 键（命令行版读到会忽略）：

```ini
AUTOSTART=false          # 开机自启动
MINIMIZE_TO_TRAY=true    # 关闭窗口时最小化至托盘
START_MINIMIZED=true     # 启动后不显示主界面（true = 驻留托盘并在启动时弹一次气泡；还没填账号时无论如何都会显示主界面）
MAC_ADDRESS=             # 手动指定认证 MAC，留空=自动取认证 IP 所在网卡
```

命令行参数（一般用不到）：

```bat
GXU_Net_AutoLogin_Tray.exe -show   :: 启动后直接显示主界面（忽略"启动后不显示主界面"）
GXU_Net_AutoLogin_Tray.exe -quit   :: 让正在运行的实例退出
```

---
## 🔧 技术原理

程序通过向广西大学认证服务器发送标准 ePortal 登录请求实现联网：

```
GET http://172.17.0.2:801/eportal/portal/login?
  callback=dr1003&
  login_method=1&
  user_account=账号[@运营商]&
  user_password=密码&
  wlan_user_ip=终端IP&
  wlan_user_mac=设备MAC（无冒号小写）&
  ...
```

- **网络检测**：定时请求 `http://connect.rom.miui.com/generate_204`（返回 204 表示联网正常）。
  稳定在线时每 5 秒探测一次，出现失败后改为 1 秒一次；**连续 2 次失败**才判定断网，
  避免一次网络抖动（WiFi 下实测出现过 619ms 的延迟尖峰）就触发重连
- **断网重连**：判定断网后立即发起登录；登录请求带 5 秒超时，失败按 1s→2s→5s→10s→30s→60s 退避重试
- **长时断网静默**：连续断网超过 20 分钟后自动进入静默（探测 60 秒一次、每 5 分钟尝试一次登录；
  挨着登录尝试的那次探测会提前醒来，以免尝试时刻被 60 秒周期推后），
  避免校园网夜间禁网时段（00:00–06:00）整夜空转。**不按星期几或钟点判断**——断网策略会随学期、
  假期、在校人数变化，因此只依据"已经断网多久"：假期里网络正常就不会进入静默，假期里真断网也能
  按常规节奏在 1 分钟内恢复；网络一恢复（探测成功）立即回到常规节奏
- **MAC 获取**：自动读取**持有认证 IP 的那块网卡**的 MAC，避免多网卡 / 虚拟机环境下取到无关网卡
- **日志**：每行带本地时间戳与级别（`2026-09-20 00:12:33 [WARN] …`），文案为纯文本（不含 emoji），与控制台输出保持一致。
  可选写入文件（`Log_To_File` / `-log`，路径 `Log_File` / `-logfile`），5 MiB 自动轮转保留 2 份；
  探测失败会记下原因（超时 / DNS / 连接失败 / 非 204），网络恢复时给出本次断网的时长与失败原因统计，
  便于回溯夜间禁网、DNS 故障这类问题。日志中**不会出现明文密码**（登录请求的 URL 不会被写进日志）

> 参考官方文档：[Linux系统宽带客户端-2024.12.30日后使用](https://net.gxu.edu.cn/info/1360/2293.htm)

---

## 🛠️ 功能特性

| 特性 | 说明 |
|------|------|
| ✅ 自动重连 | 判定断网后立即登录，失败按 1s→60s 退避重试 |
| ✅ 自适应探测 | 在线 5s 一次、异常 1s 一次，请求量约为固定 1s 探测的 1/5 |
| ✅ 长时断网静默 | 连续断网 20 分钟后转入 60s 探测 + 5 分钟一次登录，夜间禁网不空转 |
| ✅ 日志可选落文件 | 带时间戳/级别，5 MiB 轮转保留 2 份；`LOG_TO_FILE` 或 `-log` 开启 |
| ✅ 多运营商支持 | `telecom` / `unicom` / `cmcc` |
| ✅ 路由器模式 | 指定任意 IP/MAC 登录|
| ✅ Windows 托盘版 | 图形界面配置 + 状态与日志查看，托盘常驻，改完立即生效、无需重启 |
| ✅ 极低资源占用 | Go 编译为静态二进制；命令行版内存 < 10MB，托盘版约 20–30MB |
| ✅ 跨平台 | Linux / Windows 均可运行（托盘版仅 Windows） |

---

## 🧱 代码结构

```
main.go                 命令行版入口（行为与原版一致）
internal/config         .env 的解析、校验、原子写回
internal/logging        日志：控制台 / 轮转文件 / 内存环形缓冲
internal/netauth        探测、登录、认证身份（IP/MAC）解析
internal/daemon         探测循环与状态机（可取消、可查询状态）
tray/                   托盘版（独立 Go module，只依赖 lxn/walk）
.goreleaser.yaml        GoReleaser 配置：多平台矩阵 + Windows 合并 zip
.github/workflows/      push v* tag 时自动构建并发布 Release
```

命令行版与托盘版共用 `internal/` 下的核心逻辑和同一份 `.env`；托盘版是**独立的 Go module**（`tray/go.mod`），因此命令行版依旧零第三方依赖，Linux / 命令行版那条编译路径不受影响。

```bash
# 命令行版：仍是单文件编译，不需要联网拉依赖（Go 1.20+）
go build -ldflags="-s -w" -o GXU_Net_AutoLogin main.go

# 托盘版：首次编译需要联网拉 walk（国内走 GOPROXY 镜像即可；需要 Go 1.25+）
cd tray && go build -ldflags="-s -w -H=windowsgui" -o GXU_Net_AutoLogin_Tray.exe .
```

> 💡 托盘版依赖 `tray/rsrc.syso`（内含 Common Controls v6 清单与图标）。walk 没有这个清单连主窗口都建不出来，
> 所以它已经入库；万一需要重新生成：装 `go install github.com/akavel/rsrc@latest`，
> 然后在 `tray/` 下执行 `rsrc -arch amd64 -manifest tray.exe.manifest -ico assets/icon.ico -o rsrc.syso`。

## 🙏 鸣谢

**作者**：GTX690战术核显卡导弹（[nekopara.uk](https://www.nekopara.uk)）  提供的校园网机制和源码
**源仓库**：[github.com/Chocola-X/GXU-Net-AutoLogin](https://github.com/Chocola-X/GXU-Net-AutoLogin)

