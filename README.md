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

---

## 📦 快速开始

### 方法一：使用预编译二进制

1. 从 [Release](https://github.com/Chocola-X/GXU-Net-AutoLogin/releases) 下载对应的二进制文件
2. 运行 `GXU_Net_AutoLogin`
3. 首次运行会生成 `config.txt`，按提示填写账号密码
4. 再次运行即可后台守护

### 方法二：使用包管理器安装

Arch Linux 用户可从 [AUR](https://aur.archlinux.org/packages/gxu-net-autologin) 安装：

```bash
[yay/paru] -S gxu-net-autologin
```

配置文件路径为 `/etc/gxu-net-autologin/config.txt`。

### 方法三：从源码编译（推荐 Linux 用户）

确保已安装 [Go 1.20+](https://golang.org/dl/)

```bash
git clone https://github.com/Chocola-X/GXU-Net-AutoLogin.git
cd GXU-Net-AutoLogin
go build -ldflags="-s -w" -o GXU_Net_AutoLogin main.go
./GXU_Net_AutoLogin
```

首次运行将自动生成 `config.txt`，编辑后重新运行即可。

---

## ⚙️ 配置说明

### 方式 1：配置文件（`config.txt`）

程序首次运行会自动生成模板，设置示例如下：

```ini
# 校园网登录脚本信息设置：（注意请不要改变格式）
User=1807210721
Password=your_password_here
# 运营商：留空=校园网，telecom=电信，unicom=联通，cmcc=移动
Net_Type=cmcc
# 路由器模式（两者需同时填写才生效）：
Router_IP=172.16.6.6
Router_MAC=36:88:8A:99:A4:CC
```

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
```

> 💡 **注意**：`-ip` 和 `-mac` 必须**同时提供**，否则视为无效。

查看全部参数：
```bash
./GXU_Net_AutoLogin -help
```

---
## 💡 Windows隐藏运行的黑窗口

在Windows系统中，如果你对弹出的黑窗口感到不爽，可以使用VBScript隐藏它：

1. 在程序的同目录，创建一个文本文件
2. 填写以下内容：
   ```vbs
   CreateObject("Wscript.Shell").Run "GXU_Net_AutoLogin.exe", 0, False
   ```
3. 将文件命名为 `run.vbs`（或你喜欢的名字）

这样运行 `run.vbs` 就不会显示黑窗口了。

如果你需要设置计划任务，记得使用**绝对路径，并制定对应的参数**，例如：

```vbs
CreateObject("Wscript.Shell").Run "D:\GXU_Net_AutoLogin.exe -user 2103990721 -passwd 072102 -nettype unicom -ip 10.165.23.233 -mac 00:11:22:33:44:55", 0, False
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
- **长时断网静默**：连续断网超过 20 分钟后自动进入静默（探测 60 秒一次、每 5 分钟尝试一次登录），
  避免校园网夜间禁网时段（00:00–06:00）整夜空转。**不按星期几或钟点判断**——断网策略会随学期、
  假期、在校人数变化，因此只依据"已经断网多久"：假期里网络正常就不会进入静默，假期里真断网也能
  按常规节奏在 1 分钟内恢复；网络一恢复（探测成功）立即回到常规节奏
- **MAC 获取**：自动读取**持有认证 IP 的那块网卡**的 MAC，避免多网卡 / 虚拟机环境下取到无关网卡

> 参考官方文档：[Linux系统宽带客户端-2024.12.30日后使用](https://net.gxu.edu.cn/info/1360/2293.htm)

---

## 🛠️ 功能特性

| 特性 | 说明 |
|------|------|
| ✅ 自动重连 | 判定断网后立即登录，失败按 1s→60s 退避重试 |
| ✅ 自适应探测 | 在线 5s 一次、异常 1s 一次，请求量约为固定 1s 探测的 1/5 |
| ✅ 长时断网静默 | 连续断网 20 分钟后转入 60s 探测 + 5 分钟一次登录，夜间禁网不空转 |
| ✅ 多运营商支持 | `telecom` / `unicom` / `cmcc` |
| ✅ 路由器模式 | 指定任意 IP/MAC 登录|
| ✅ 极低资源占用 | Go 编译为静态二进制，内存 < 10MB |
| ✅ 跨平台 | Linux / Windows 均可运行 |

---

## 📜 许可证

本项目采用 [GNU Affero General Public License v3.0 (AGPL-3.0)](LICENSE) 开源协议。

> 如果你修改了代码并用于网络服务（如部署为公共代理），**必须公开修改后的源码**。

---

## 🙏 鸣谢

- 广西大学信息网络中心 提供的 [Linux 登录接口](https://net.gxu.edu.cn/info/1360/2293.htm)
- MIUI 的 `generate_204` 网络探测机制

---

> **作者**：GTX690战术核显卡导弹（[nekopara.uk](https://www.nekopara.uk)）  
> **仓库**：[github.com/Chocola-X/GXU-Net-AutoLogin](https://github.com/Chocola-X/GXU-Net-AutoLogin)

🚀 **Enjoy your stable campus network!**
