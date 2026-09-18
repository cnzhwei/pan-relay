# wopan-cli

<p align="center">
  <strong>沃家云盘（WoPan）与夸克网盘（Quark）轻量级多线程命令行中转工具</strong>
</p>

<p align="center">
  <a href="https://github.com/cnzhwei/wopan-cli/releases"><img src="https://img.shields.io/github/v/release/cnzhwei/wopan-cli?color=blue&logo=github" alt="Release"></a>
  <a href="https://github.com/cnzhwei/wopan-cli/actions"><img src="https://img.shields.io/github/actions/workflow/status/cnzhwei/wopan-cli/build-and-release.yml?logo=github&label=build" alt="Build Status"></a>
  <a href="https://github.com/cnzhwei/wopan-cli/blob/main/LICENSE"><img src="https://img.shields.io/badge/License-MIT-green.svg" alt="License"></a>
  <img src="https://img.shields.io/badge/Language-Go%201.24-blue.svg?logo=go" alt="Go Version">
  <img src="https://img.shields.io/badge/Arch-x86__64%20%7C%20arm64-orange.svg" alt="Architecture">
</p>

---

## 📖 项目背景与定位

在构建家庭影音库（Emby / Jellyfin / Plex）的影视中转与云端归档方案时，许多用户使用海外 VPS 或轻量主机作为“下载与转存节点”：
- **资源来源**：通常保存在**夸克网盘（Quark）**分享合集或个人空间中；
- **归档终点**：归档到不限速、不计家宽上传流量的**中国联通沃家云盘（WoPan）**。

过去在 VPS 上跑这套流程，通常需要搭建 **OpenList** 或 **Alist**。但常驻 Web 服务存在明显的痛点：
1. **服务沉重**：Alist / OpenList 包含完整的 Web 前端、多种存储驱动适配层、数据库（SQLite/MySQL）和后台 Daemon，常驻占用 150MB~300MB 内存；
2. **大文件下载单线程限速**：夸克 Web 端对非 VIP 有大小限制，单连接易被限速；
3. **流程割裂**：需要手动登录后台、触发复制、观察队列，一旦中转机到期更换就得重搭一遍环境。

**`wopan-cli` 为此打造：**
它是一个**独立的、单静态二进制、即开即用**的命令行中转工具。**不带 Web 界面、不依赖数据库、无任何外部依赖**：
- **沃家模块**：支持 8MB 分片多线程并发推流，自动换新凭据；
- **夸克模块**：模拟 PC 客户端 UA，**彻底绕过 Web 端大文件下载限制**，支持 HTTP `Range` 多连接并发分段拉取；
- **一键中转 Relay**：支持 `wopan-cli relay quark:/电影.mkv wopan:/emby/movies/`，自动完成“夸克多线程下载 -> 沃家多线程推流 -> 自动删除本地缓存”，专为低配小硬盘 VPS 量身定制！

---

## ✨ 核心特性

- **🚀 单静态绿色二进制**：Go 语言原生开发，`CGO_ENABLED=0` 纯静态编译，零外部动态库依赖。在 **Alpine Linux（musl）**、**Debian / Ubuntu（glibc）**、CentOS 及 macOS 上扔进去直接就能跑；
- **⚡ 沃家 8MB 分块并发上传**：基于联通云盘底层 `upload2C` 协议，Goroutine 线程池并发上传，跑满 VPS 往联通机房的上行带宽；
- **📥 夸克多线程分段并发下载**：模拟 Quark-Cloud-Drive 客户端凭据通道，解除网页端大文件限制，多线程 HTTP `Range` 并发拉取并自动合并；
- **🔄 一键全自动中转流水线 (`relay`)**：支持从夸克指定目录/文件一键拉取并直推沃家云盘，完成后自动销毁本地临时文件，适合几 GB 磁盘的小鸡中转动辄几十 GB 的大合集；
- **📊 实时终端速度看板**：传输过程中平滑输出完成百分比、已传大小、平均传输速率（MB/s）及耗时统计；
- **🔑 Token 自动续期与持久化闭环**：基于 `refresh_token` 实现永久免密免登录。短效 Token 过期自动换新并写回本地配置文件。

---

## 📦 快速安装

### 方式一：一键自动安装（推荐）

适用于任何 **Linux（x86_64 / aarch64）** 或 **macOS（Apple Silicon）**，自动识别架构并安装至 `/usr/local/bin`：

```bash
curl -fsSL https://raw.githubusercontent.com/cnzhwei/wopan-cli/main/install.sh | sh
```

### 方式二：手动下载 Release 预编译包

从 [GitHub Releases 页面](https://github.com/cnzhwei/wopan-cli/releases) 下载：

```bash
# Linux x86_64 为例：
curl -sL https://github.com/cnzhwei/wopan-cli/releases/latest/download/wopan-cli-linux-amd64.tar.gz | tar -xz
chmod +x wopan-cli
sudo mv wopan-cli /usr/local/bin/
```

---

## 🔐 凭据配置指南

`wopan-cli` 的凭据均可独立配置，按需加载。

### 1. 沃家云盘凭据 (`wopan_config.json`)

配置文件查找顺序：命令行 `-c` > 当前目录 `./wopan_config.json` > `~/.config/wopan-cli/config.json` > 环境变量 `WOPAN_REFRESH_TOKEN`。

**获取方式：** 从现有 OpenList/Alist 存储编辑页面复制 `刷新令牌 (refresh_token)`。
```json
{
  "refresh_token": "xxxxxxxx-xxxx-xxxx-xxxx-xxxxxxxxxxxx",
  "zone_url": "https://tjupload.pan.wo.cn"
}
```

### 2. 夸克网盘凭据 (`quark_config.json`)

配置文件查找顺序：命令行 `--quark-config` > 当前目录 `./quark_config.json` > `~/.config/wopan-cli/quark.json` > 环境变量 `QUARK_COOKIE`。

**获取方式：** 
- 从已有 Alist/OpenList 的 `/quark` 存储配置中复制完整 `cookie`；
- 或在电脑浏览器登录 [pan.quark.cn](https://pan.quark.cn/)，按 `F12` 复制 Cookie。
```json
{
  "cookie": "_UP_A4A_11_=...; __puus=...; __pus=..."
}
```

---

## 🛠️ 详细命令手册

全局通用参数：
- `-c, --config <path>`：指定 WoPan 配置文件路径；
- `--quark-config <path>`：指定 Quark 配置文件路径；
- `-t, --threads <num>`：并发线程数（默认 4，推荐 4~8）。

---

### 一、 跨网盘全自动中转 (`relay`) —— 最强功能！

自动从夸克下载指定文件，边传边缓冲，上传沃家云盘成功后自动销毁本地临时切片：

```bash
# 从夸克指定路径中转到沃家云盘 /emby/movies/ 目录 (6线程并发)
wopan-cli relay quark:/来自：分享/山海情/EP01.mkv wopan:/emby/tv/山海情/ -t 6

# 支持使用夸克文件 FID 进行中转
wopan-cli relay quark:56c288c923b74cc88f2ffcc08292310c wopan:/emby/movies/ -t 4
```
*中转过程输出看板：*
```text
[Relay Pipeline] Starting relay: Quark [EP01.mkv] -> WoPan [/emby/tv/山海情/] (Threads: 6)
[Stage 1/2] Downloading from Quark...
  Progress: 100.0% ( 3273.8 / 3273.8 MB) | Avg Speed: 24.50 MB/s | Total Time: 133.6s
[✓] Download completed successfully: /tmp/wopan_relay_12891_EP01.mkv

[Stage 2/2] Uploading to WoPan...
  Progress: 100.0% ( 3273.8 / 3273.8 MB) | Avg Speed: 18.20 MB/s | Total Time: 179.8s
[✓] Upload completed successfully! (FID: glQtb_bUmsOlrwu6/LsURAxk4S7Qr69U)

[✓] Relay completed! Local temporary staging file deleted automatically.
```

---

### 二、 夸克网盘组件命令 (`quark`)

```bash
# 1. 浏览夸克网盘根目录
wopan-cli quark ls /

# 2. 浏览指定目录层级 (支持中文路径)
wopan-cli quark ls /来自：分享/山海情

# 3. 多线程并发从夸克下载文件 (绕过 Web 文件大小限制)
wopan-cli quark download /来自：分享/电影名.mkv ./ -t 6

# 4. 根据文件 FID 直接多线程下载
wopan-cli quark download 56c288c923b74cc88f2ffcc08292310c /data/movies/ -t 8
```

---

### 三、 沃家云盘核心命令 (`wopan`)

```bash
# 1. 检查账号状态与空间
wopan-cli whoami

# 2. 浏览沃家网盘目录
wopan-cli ls /emby/movies

# 3. 多线程并发上传本地文件到沃家网盘
wopan-cli upload /data/movies/Interstellar.2014.mkv /emby/movies/ -t 6

# 4. 多线程下载沃家网盘文件 (支持 Range 分段加速)
wopan-cli download /emby/movies/Interstellar.2014.mkv ./ -t 4

# 5. 远程创建与删除目录
wopan-cli mkdir /emby/movies "科幻电影"
wopan-cli rm 51bd55b3ad2f4dd49cd5d01ab81194ac

# 6. 主动触发凭据轮转刷新
wopan-cli refresh
```

---

## ❓ 常见问题 (FAQ)

#### Q1: 夸克网盘报错 `download file size limit` 是怎么回事？
**答：** 夸克对 Web 端限制普通用户下载超过几百 MB 的大文件。`wopan-cli` 内部完整模拟了官方 PC 客户端专用 UA 与加密签名通道，在服务端直接被识别为客户端握手，因此支持免 VIP 下载几十 GB 的原画视频！

#### Q2: 夸克的 Cookie 会过期吗？
**答：** 夸克 Cookie 中的核心鉴权是 `__puus` / `__pus`。`wopan-cli` 在每次调用 API 时，如果检测到服务端响应了更新后的 Set-Cookie，会**自动更新并回写本地的 `quark_config.json`**，具备半持久保活能力。

#### Q3: 中转大文件会不会撑爆 VPS 磁盘？
**答：** 使用 `wopan-cli relay` 时，任务是逐文件串行接力进行的：单个文件拉取完毕后立即推送到沃家，推流完毕校验通过后**立即将本地切片彻底删除**。只要 VPS 剩余磁盘大于最大的单个视频文件（例如保留 15GB 空间），就能源源不断中转完几百 GB 的剧集！

---

## 📄 开源许可证

本项目基于 [MIT 许可证](https://github.com/cnzhwei/wopan-cli/blob/main/LICENSE) 开源。欢迎 Star、Fork 与提交 PR！
