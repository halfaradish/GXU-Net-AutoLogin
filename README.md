# 🌐 GXU-Net-AutoLogin

> 广西大学校园网自动登录 & 断网重连守护程序  
> 路由器模式 · 运营商选择 · Windows 托盘版 · 配置文件/命令行双模式

[![License: AGPL v3](https://img.shields.io/badge/License-AGPL%20v3-blue.svg)](https://www.gnu.org/licenses/agpl-3.0)
![Go Version](https://img.shields.io/badge/Go-1.20%2B-informational)
![Platform](https://img.shields.io/badge/Platform-Windows%20|%20Linux%20|%20macOS-lightgrey)

广西大学校园网在高峰期常会断连，手动重登既麻烦又影响挂机任务。本程序使用 **Go 语言** 编写，轻量高效，可实现：
- ✅ **断网自动检测并重连**
- ✅ **支持校园网 + 三大运营商（电信/联通/移动）**
- ✅ **路由器模式：指定 IP/MAC 登录（适配宿舍共享上网）**
- ✅ **配置文件 or 命令行参数，灵活部署**
- ✅ **Windows 托盘版：图形界面配置，状态与日志一目了然（[见下文](#-windows-托盘版)）**

当前版本 **v1.1.0**（新增 Windows 托盘版），发布记录与产物见 [Releases](https://github.com/halfaradish/GXU-Net-AutoLogin/releases)。

---

## 🙏 鸣谢

**原作者**：GTX690战术核显卡导弹（[nekopara.uk](https://www.nekopara.uk)）—— 由衷感谢作者提供的校园网认证机制与最初的命令行实现
**上游仓库**：[github.com/Chocola-X/GXU-Net-AutoLogin](https://github.com/Chocola-X/GXU-Net-AutoLogin)

## 📦 快速开始

### 方法一：Windows 托盘版（桌面用户推荐）

1. 打开 [Releases](https://github.com/halfaradish/GXU-Net-AutoLogin/releases)，下载 `GXU_Net_AutoLogin_1.1.0_windows_amd64.zip`
2. **解压**到想长期存放的目录（例如 `D:\Tools\GXU-Net-AutoLogin\`），不要直接在压缩包里运行
3. 双击 `GXU_Net_AutoLogin_Tray.exe`：还没配置过账号时它会直接弹出主界面，填好学号、密码、运营商，点「保存并应用」——写回 `.env` 并立即认证一次，不用重启程序
4. 想开机就自动挂上，展开「高级选项」勾选 **开机自启动**（写当前用户注册表，不需要管理员权限）

配置好之后，程序按「启动后不显示主界面」的设置静默驻留托盘，左键点托盘图标随时打开界面。完整说明见 [Windows 托盘版](#-windows-托盘版)。

> 💡 托盘版**只有 Windows 64 位**有；32 位 Windows、Linux、macOS 请用下面的命令行版。
> 💡 发布产物未做代码签名，首次运行若被 SmartScreen 拦下，选「更多信息 → 仍要运行」即可。

### 方法二：Linux / macOS 命令行版

Release 里的 Linux / macOS 资产是**不带扩展名的裸二进制**，文件名带版本与架构（如 `GXU_Net_AutoLogin_1.1.0_linux_amd64`）：

```bash
chmod +x GXU_Net_AutoLogin_1.1.0_linux_amd64
sudo mv GXU_Net_AutoLogin_1.1.0_linux_amd64 /usr/local/bin/GXU_Net_AutoLogin

mkdir -p ~/gxu-net-autologin && cd ~/gxu-net-autologin   # .env 与 logs/ 会落在这里
GXU_Net_AutoLogin    # 首次运行：生成 .env 模板并提示填写，然后退出
vim .env             # 填 USER / PASSWORD（键名与 .env.example 相同）
GXU_Net_AutoLogin    # 再次运行：进入守护，断网自动重连
```

也可以完全不用配置文件，账号密码直接走命令行参数（此时**不再读 `.env`**）：

```bash
GXU_Net_AutoLogin -user 1807210721 -passwd mypassword -nettype cmcc
```

> 💡 默认日志只打印到控制台；长期挂机建议开文件日志：`-log -logfile /var/log/gxu-net-autologin.log`
> 💡 systemd 部署时 stdout 照样有日志，`journalctl -u gxu-net-autologin -f` 就能看，配置文件与日志目录的相对路径都以工作目录为准，详见 [配置说明](#-配置说明)

### 方法三：从源码编译

命令行版（Go 1.20+，零第三方依赖）：

```bash
git clone https://github.com/halfaradish/GXU-Net-AutoLogin.git
cd GXU-Net-AutoLogin
go build -ldflags="-s -w" -o GXU_Net_AutoLogin main.go
./GXU_Net_AutoLogin
```

托盘版（Windows，Go 1.25+，首次编译需联网拉 `lxn/walk`）：

```bat
cd tray
go build -ldflags="-s -w -H=windowsgui" -o GXU_Net_AutoLogin_Tray.exe .
```

### 发布产物一览

| 资产 | 内容 | 适用 |
|---|---|---|
| `GXU_Net_AutoLogin_<版本>_windows_amd64.zip` | `GXU_Net_AutoLogin.exe`（命令行）+ `GXU_Net_AutoLogin_Tray.exe`（托盘） | Windows 64 位 |
| `GXU_Net_AutoLogin_<版本>_windows_386.exe` | 命令行版 | 32 位 Windows（无托盘版） |
| `GXU_Net_AutoLogin_<版本>_linux_{amd64,arm64}` | 命令行版 | Linux |
| `GXU_Net_AutoLogin_<版本>_darwin_{amd64,arm64}` | 命令行版 | macOS（有构建产物，未在真机验证） |
| `checksums.txt` | 各资产 SHA-256 | 校验用 |

多平台发布用 [GoReleaser](https://goreleaser.com)（`.goreleaser.yaml`）：本地出全部平台产物 `goreleaser release --snapshot --clean`（落到 `dist/`）；push `v*` tag 时 `.github/workflows/release.yml` 自动构建并发 Release（资产带版本号）。

---

## ⚙️ 配置说明

两种方式：**`.env` 配置文件**（命令行版与托盘版共用同一份）或**命令行参数**（仅命令行版）。

### 方式 1：配置文件 `.env`

**放在哪**

- 命令行版读**当前工作目录**下的 `.env`（不是 exe 所在目录——systemd 部署时记得设 `WorkingDirectory`）；文件不存在时会自动生成一份模板、提示填写后退出
- 托盘版先看启动时的工作目录有没有 `.env`，没有才用 exe 所在目录，并把该目录作为自己的配置目录（工作目录也会切过去，所以 `.env` 与 `logs/` 都落在同一处）；托盘版不生成模板，直接在界面里填、点「保存并应用」写出

**键一览**

| 键 | 说明 | 留空时 |
|---|---|---|
| `USER` | 校园网账号（学号 / 工号） | 必填 |
| `PASSWORD` | 密码 | 必填 |
| `NET_TYPE` | 运营商：`telecom`（电信）/ `unicom`（联通）/ `cmcc`（移动），大小写不限 | 校园网 |
| `ROUTER_IP` | 路由器模式：认证用的 IP | 用本机 IP |
| `ROUTER_MAC` | 路由器模式：认证用的 MAC | 用本机 MAC |
| `MAC_ADDRESS` | 手动指定认证 MAC（仅本机模式；路由器模式用 `ROUTER_MAC`） | 自动取认证 IP 所在网卡的 MAC；匹配不到再回退到第一块可用网卡 |
| `LOG_TO_FILE` | 是否同时写日志文件 | 只打印到控制台（托盘版忽略此项，一律写文件） |
| `LOG_FILE` | 日志文件路径 | 工作目录下 `logs/GXU_Net_AutoLogin.log`（托盘版为配置目录下） |
| `AUTOSTART` | 开机自启动（**仅托盘版**） | 关闭 |
| `MINIMIZE_TO_TRAY` | 关闭窗口时最小化至托盘（**仅托盘版**） | 开启 |
| `START_MINIMIZED` | 启动后不显示主界面（**仅托盘版**） | 开启 |

`ROUTER_IP` 与 `ROUTER_MAC` 必须**同时非空**才启用路由器模式，只填一个不生效、会退回本机 IP/MAC（托盘版会拦下提示；用 `-ip` / `-mac` 参数时只给一个会直接报错退出）。

**格式**

```ini
# 键名不区分大小写（USER= 与 User= 等价），值可以用引号包起来（PASSWORD="p@ss#word"）
USER=1807210721
PASSWORD=your_password_here
NET_TYPE=cmcc
ROUTER_IP=172.16.6.6
ROUTER_MAC=36:88:8A:99:A4:CC
MAC_ADDRESS=
LOG_TO_FILE=true
LOG_FILE=
AUTOSTART=false
MINIMIZE_TO_TRAY=true
START_MINIMIZED=true
```

- 键名**不区分大小写**，`#` 开头是注释，未识别的键静默忽略，旧版的 `config.txt` 改名成 `.env` 即可继续用
- 开关值认 `true` / `1` / `yes` / `on` 与 `false` / `0` / `no` / `off`；**留空 = 保持默认**（所以 `MINIMIZE_TO_TRAY`、`START_MINIMIZED` 留空即为开启）
- 日志文件**自动轮转**：单文件写满 5 MiB 后更名为 `.log.1`，旧份依次后移，只保留最近的 `.log.1`、`.log.2`，长期挂机不会撑满磁盘；日志里不会出现明文密码

### 方式 2：命令行参数（仅命令行版）

`-user` 与 `-passwd` **必须成对提供**；一旦提供，程序就**完全不再读 `.env`**（连 `LOG_TO_FILE` 也不生效），适合服务部署。

| 参数 | 说明 |
|---|---|
| `-user` | 用户名 |
| `-passwd` | 密码 |
| `-nettype` | 运营商（`telecom` / `unicom` / `cmcc`，不区分大小写；填错直接报错退出） |
| `-ip` / `-mac` | 路由器模式，必须同时提供，只填一个报错退出 |
| `-log` | 把日志同时写入文件 |
| `-logfile` | 日志文件路径（需与 `-log` 一起用，单独给无效） |
| `-help` | 查看全部参数 |

```bash
# 基础用法
./GXU_Net_AutoLogin -user 1807210721 -passwd your_password

# 含运营商 + 路由器模式
./GXU_Net_AutoLogin -user 1807210721 -passwd mypassword -nettype cmcc \
  -ip 172.16.6.6 -mac 36:88:8A:99:A4:CC

# 把日志同时写入文件（服务部署建议指定绝对路径）
./GXU_Net_AutoLogin -user 1807210721 -passwd mypassword -log -logfile /var/log/gxu-net-autologin.log
```

> 💡 参数会留在 shell 历史与进程列表（`ps`）里，介意的话改用 `.env` 方式。

systemd 部署示例（账号密码放 `WorkingDirectory` 下的 `.env`，不必写进 `ExecStart`）：

```ini
[Unit]
Description=GXU Net AutoLogin
After=network-online.target
Wants=network-online.target

[Service]
WorkingDirectory=/opt/GXU_Net_AutoLogin
ExecStart=/opt/GXU_Net_AutoLogin/GXU_Net_AutoLogin -log -logfile /var/log/gxu-net-autologin.log
Restart=always
RestartSec=5

[Install]
WantedBy=multi-user.target
```

---

## 🖥️ Windows 托盘版

托盘版是给不想碰命令行的用户准备的图形版本：常驻系统托盘、无黑窗口（GUI 子系统编译），账号密码在界面里填一次，改完「保存并应用」立刻生效。它和命令行版共用同一份 `.env` 与 `internal/` 核心逻辑，两边可以交替使用同一份配置。

**获取**：Release 的 `GXU_Net_AutoLogin_<版本>_windows_amd64.zip` 里带 `GXU_Net_AutoLogin_Tray.exe`，**仅 Windows 64 位**——32 位 Windows、Linux、macOS 都没有托盘版，请用命令行版。

**第一次用**

1. 把 `GXU_Net_AutoLogin_Tray.exe` 解压到想长期存放的目录（例如 `D:\Tools\GXU-Net-AutoLogin\`），双击运行
2. 还没配置过账号时，它会直接显示主界面（此时 `.env` 还不存在）
3. 填账号、密码，选运营商（校园网账号保持默认，运营商宽带账号选对应项），点「保存并应用」→ 写回 `.env` 并立即认证一次
4. 展开「高级选项」勾选 **开机自启动**，之后开机自动驻留托盘

配置好之后，按「启动后不显示主界面」的设置静默驻留托盘（默认开启），启动时弹一次气泡告诉你它在跑；左键点托盘图标即可打开界面。

> 💡 **配置目录**：优先用**启动时的工作目录**里的 `.env`，没有才用 exe 所在目录；程序会把工作目录切到那里，`.env` 与 `logs/` 都相对它。
> 💡 托盘版没有控制台，**日志一律写文件**（默认 `<配置目录>\logs\GXU_Net_AutoLogin.log`，5 MiB 轮转保留 2 份）；日志文件不可写时会降级为只留在界面内存里，不会拖垮守护。

### 界面

**基础配置**：账号、密码、运营商

**高级选项**（默认收起，点「高级选项 ▾」展开；开合状态记在注册表 `HKCU\Software\GXU-Net-AutoLogin`，下次启动沿用）：

| 选项 | 说明 |
|---|---|
| 开机自启动 | 写入当前用户注册表启动项 |
| 关闭窗口时最小化至托盘 | 取消勾选后点 X 即退出 |
| 启动后不显示主界面（驻留托盘） | 勾选后启动只驻留托盘 |
| 日志目录 | 目录可自选（带「浏览…」），文件名固定为 `GXU_Net_AutoLogin.log` |
| 路由器 IP / 路由器 MAC | 同时填写才启用路由器模式 |
| MAC 地址 | 自动（认证 IP 所在网卡）/ 从网卡列表选 / 手动输入 |

**运行状态**：连接状态（在线 / 断网 / 静默 / 认证中）、状态详情、认证地址与认证 MAC（含来源）、路由器信息、最近一次认证结果、运行统计（本次在线与断网时长、连续失败次数、累计断网次数、当前探测间隔、下次登录倒计时）

**日志**：内置实时日志（界面最多渲染 2000 行，更早的看日志文件；按级别「全部 / WARN 及以上 / ERROR」与关键字过滤，可暂停滚动），以及「打开日志文件 / 打开目录 / 清空日志」（清空会连轮转备份一起删）

**按钮**：保存并应用、立即重连、高级选项 ▾、恢复默认配置、退出

**托盘图标**：左键点击打开主界面；右键菜单为 显示主界面 / 立即重连 / 打开日志文件 / 打开日志目录 / 退出

### 行为约定

- **保存并应用**：写回 `.env` → 同步开机自启注册表项 → 重建探测循环 → **立即发起一次认证**（不等探测判定），全程不重启进程。账号或密码为空、路由器 IP/MAC 只填一个时会拦下并提示
- **恢复默认配置**：只重置高级选项里的 7 项（开机自启、关闭最小化至托盘、启动不显示主界面、日志目录、路由器 IP、路由器 MAC、MAC 地址），账号、密码、运营商不动，并且立即生效
- **关闭窗口**：默认最小化到托盘继续守护；取消勾选「关闭窗口时最小化至托盘」后，点 X 即退出
- **单实例**：重复启动不会起第二个进程，只会把已有实例的主界面唤出来
- **开机自启**：写入 `HKCU\Software\Microsoft\Windows\CurrentVersion\Run`（当前用户，不需要管理员）；已配置账号后每次启动按配置重写一遍，程序挪了位置也能自愈
- **气泡提醒只有两处**：静默启动驻留托盘时、关闭窗口最小化到托盘时各一次。断线、恢复、进入静默**不弹窗**——状态看主界面「运行状态」区或托盘悬停提示，过程记在日志里
- **密码是明文**：与命令行版共用同一份 `.env`，没有做凭据加密；`.env` 已在 `.gitignore` 里，别手动提交

### 命令行参数（一般用不到）

```bat
GXU_Net_AutoLogin_Tray.exe -show   :: 启动后直接显示主界面（忽略「启动后不显示主界面」）
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

- **网络检测**：定时请求 `http://connect.rom.miui.com/generate_204`（返回 204 表示联网正常），单次探测超时 2 秒。
  稳定在线时每 5 秒探测一次，出现失败后改为 1 秒一次；**连续 2 次失败**才判定断网，
  避免一次网络抖动（WiFi 下实测出现过 619ms 的延迟尖峰）就触发重连
- **断网重连**：判定断网后立即发起登录；登录请求带 5 秒超时，失败按 1s→2s→3s→5s→8s→12s→18s→27s→40s→60s
  **缓坡**退避（约 2 分钟到顶；原先 6 档 48 秒就顶到 60 秒，断网一分钟后所有重试都被锁在最大档上），
  每档另加 ±20% 抖动，避免多台设备同时重试
- **网络一变就立刻重试**：探测失败**类别**变了（例如从"完全不通"变成"能拿到门户的 HTTP 响应"，说明链路已通、只差认证）、
  认证 IP/MAC 变了（换网 / DHCP 续租）、手动"立即重连"——三种信号都会把退避清零并当场再试一次，
  于是恢复不再需要等满一个退避档（原先网络第 90 秒恢复，最坏要拖到第 162 秒）。变化信号带 10 秒去抖，
  抖动链路来回跳时按噪声丢掉，不会变成一秒一发登录请求
- **长时断网静默**：连续断网超过 20 分钟后自动进入静默（探测 60 秒一次、每 5 分钟尝试一次登录；
  挨着登录尝试的那次探测会提前醒来，以免尝试时刻被 60 秒周期推后），
  避免校园网夜间禁网时段（00:00–06:00）整夜空转。**不按星期几或钟点判断**——断网策略会随学期、
  假期、在校人数变化，因此只依据"已经断网多久"：假期里网络正常就不会进入静默，假期里真断网也能
  按常规节奏在 1 分钟内恢复；网络一恢复（探测成功）立即回到常规节奏
- **MAC 获取**：路由器模式直接用配置里的 `ROUTER_IP` / `ROUTER_MAC`；本机模式的优先级是
  手动指定（`MAC_ADDRESS`）→ **持有认证 IP 的那块网卡**的 MAC → 第一块可用网卡，
  避免多网卡 / 虚拟机环境下取到无关网卡；当前用的是哪一种，界面与日志里都会标出来
  （自动检测 / 手动指定 / 自动选择 / 路由器）
- **认证身份每轮刷新**：开机自启时网络还没就绪不再启动失败退出，改为循环里持续重试，网络一恢复自动接上；
  每次登录前还会再解析一次 IP/MAC，避免拿 DHCP 续租前的旧 IP 去认证
- **日志**：每行带本地时间戳与级别（`2026-09-20 00:12:33 [WARN] …`），文案为纯文本（不含 emoji），与控制台输出保持一致。
  可选写入文件（`LOG_TO_FILE` / `-log`，路径 `LOG_FILE` / `-logfile`），5 MiB 自动轮转保留 2 份；
  托盘版另在内存里保留最近 2000 条供界面实时查看。探测失败会记下原因（超时 / DNS / 连接失败 / 非 204），
  网络恢复时给出本次断网的时长与失败原因统计，便于回溯夜间禁网、DNS 故障这类问题。
  日志中**不会出现明文密码**（登录请求的 URL 不会被写进日志）

> 参考官方文档：[Linux系统宽带客户端-2024.12.30日后使用](https://net.gxu.edu.cn/info/1360/2293.htm)

---

## 🛠️ 功能特性

| 特性 | 说明 |
|------|------|
| ✅ 自动重连 | 判定断网后立即登录，失败按 1s→60s 缓坡退避（约 2 分钟到顶，含 ±20% 抖动）；网络一变（断网原因变了、换了 IP、手动重连）立刻重试 |
| ✅ 自适应探测 | 在线 5s 一次、异常 1s 一次（超时 2s），请求量约为固定 1s 探测的 1/5 |
| ✅ 长时断网静默 | 连续断网 20 分钟后转入 60s 探测 + 5 分钟一次登录，夜间禁网不空转 |
| ✅ 日志可选落文件 | 带时间戳/级别，5 MiB 轮转保留 2 份；`LOG_TO_FILE` 或 `-log` 开启 |
| ✅ 多运营商支持 | `telecom` / `unicom` / `cmcc` |
| ✅ 路由器模式 | 指定任意 IP/MAC 登录 |
| ✅ Windows 托盘版 | 图形界面配置 + 状态与日志查看，托盘常驻，改完立即生效、无需重启（仅 Windows 64 位） |
| ✅ 极低资源占用 | Go 编译为静态二进制；命令行版内存 < 10MB，托盘版约 20–30MB |
| ✅ 跨平台 | 命令行版 Linux / Windows / macOS（macOS 有构建产物，未在真机验证）；托盘版仅 Windows 64 位 |

---

## 🧱 代码结构

```
main.go                 命令行版入口
internal/config         .env 的解析、校验、原子写回
internal/logging        日志：控制台 / 轮转文件 / 内存环形缓冲
internal/netauth        探测、登录、认证身份（IP/MAC）解析
internal/daemon         探测循环与状态机（可取消、可查询状态）
tray/                   托盘版（独立 Go module，依赖 lxn/walk 等）
docs/                   维护者文档：托盘版实现说明（docs/tray.md）等
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
