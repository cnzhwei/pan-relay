# pan-relay

<p align="center">
  <strong>全能多网盘轻量多线程中转与命令行同步工具</strong>
</p>

<p align="center">
  <a href="https://github.com/cnzhwei/pan-relay/releases"><img src="https://img.shields.io/github/v/release/cnzhwei/pan-relay?color=blue&logo=github" alt="Release"></a>
  <a href="https://github.com/cnzhwei/pan-relay/actions"><img src="https://img.shields.io/github/actions/workflow/status/cnzhwei/pan-relay/build-and-release.yml?logo=github&label=build" alt="Build Status"></a>
  <a href="https://github.com/cnzhwei/pan-relay/blob/main/LICENSE"><img src="https://img.shields.io/badge/License-MIT-green.svg" alt="License"></a>
  <img src="https://img.shields.io/badge/Language-Go%201.24-blue.svg?logo=go" alt="Go Version">
  <img src="https://img.shields.io/badge/Arch-x86__64%20%7C%20arm64-orange.svg" alt="Architecture">
</p>

---

## 📖 项目定位与更名演进

在家庭影音自动化（Emby / Jellyfin / Plex）与云端资产归档的实际场景中，影视与媒体资源往往分散在多个公共云盘中（如**夸克网盘**分享、**百度网盘**资源库），而最终的稳定高速播放源则归档在不限速、不耗费家庭宽带上传流量的**中国联通沃家云盘（WoPan）**。

本项目最初为针对沃家云盘的轻量客户端（`wopan-cli`）。随着夸克网盘大文件绕过机制、跨盘全自动 Relay 接力流水线、以及**百度网盘（Baidu Netdisk）**双模组件的全面加入，工具已演进为通用的**多网盘中转与同步中心**，正式更名为 **`pan-relay`**。

### 为什么选择 `pan-relay` 而不是传统 Alist / OpenList？

| 对比维度 | 传统 Alist / OpenList | **pan-relay** |
| :--- | :--- | :--- |
| **运行时依赖** | 强依赖 Web 容器、SQLite/MySQL 数据库、Node/Web 前端 | **零依赖，纯静态单一可执行文件（约 7MB）** |
| **内存与资源** | 常驻占用 150MB ~ 300MB+ 内存 | **运行即起、传输即走，空闲 0 内存占用** |
| **跨系统兼容** | 需要 Docker 或特定 Linux 发行版依赖 | **原生兼容 Alpine（musl）与 Debian/Ubuntu（glibc）** |
| **跨盘中转** | 手动登录 Web 界面配置复制任务，需配置公网端口 | **命令行单行直接调用 (`relay`)，支持脚本批量管道** |
| **小盘 VPS 容灾** | 批量任务容易撑爆中转 VPS 本地临时磁盘 | **严格串行接力，传输校验后秒删本地暂存，绝不爆盘** |
| **下载限制绕过** | 夸克 Web 端受限几百 MB，百度受限普通单线程 | **客户端原生指纹封装 + HTTP Range 多连接分段并发加速** |

---

## ✨ 核心特性

- **🚀 纯静态绿色二进制**：Go 原生开发，`CGO_ENABLED=0` 静态编译。在 **Alpine Linux（musl / Busybox）**、**Debian / Ubuntu**、CentOS 及 macOS 上复制即用；
- **🔄 一键全自动跨盘中转流水线 (`relay`)**：
  - 支持 `quark -> wopan`、`baidu -> wopan` 单行命令调度；
  - 自动完成“源网盘多线程高速拉取 -> 目标网盘多线程切片推流 -> 远端校验 -> 自动彻底删除本地临时缓存”全闭环；
- **📦 沃家云盘 (WoPan) 核心**：
  - 基于联通底层 8MB 切片（`upload2C`）并发推流，跑满海外 VPS 往国内联通骨干网物理上行；
  - 基于 `refresh_token` 实现长效自动换新短效 Token，一次配置终身免维护；
- **📥 夸克网盘 (Quark) 原生组件**：
  - 深度模拟 Quark-Cloud-Drive 官方 PC 客户端 UA 与加密签名，**彻底绕过非 VIP 网页端大文件下载限制**；
  - 多线程 HTTP Range 并发拉取并自动合并；
- **🛡️ 百度网盘 (Baidu Netdisk) 双模支持**：
  - **模式一（Cookie / BDUSS）**：直接粘贴浏览器抓取的 Cookie 即可高速拉取，免去申请开发者 App 的繁琐认证；
  - **模式二（开放平台 OAuth）**：支持官方 `access_token` 与 `refresh_token` 标准鉴权；
  - 支持多线程并发下载大文件；
- **📊 实时终端速度看板**：传输过程中平滑输出完成百分比、已完成/总大小、平均传输速率（MB/s）及耗时统计。

---

## 📦 快速安装

### 方式一：一键自动安装（最推荐）

在任何 **Linux（x86_64 / aarch64）** 或 **macOS（Apple Silicon）** 上执行：

```bash
curl -fsSL https://raw.githubusercontent.com/cnzhwei/pan-relay/main/install.sh | sh
```

### 方式二：从 Release 手动下载

前往 [GitHub Releases 页面](https://github.com/cnzhwei/pan-relay/releases) 下载最新预编译包：

```bash
# 以 Linux x86_64 为例：
curl -sL https://github.com/cnzhwei/pan-relay/releases/latest/download/pan-relay-linux-amd64.tar.gz | tar -xz
chmod +x pan-relay
sudo mv pan-relay /usr/local/bin/
```

每个正式 Release 同时提供 `SHA256SUMS.txt`。手动下载时请在安装前校验：

```bash
curl -fsSLO https://github.com/cnzhwei/pan-relay/releases/latest/download/SHA256SUMS.txt
sha256sum --check SHA256SUMS.txt --ignore-missing
```

校验文件缺失、没有对应平台条目或校验失败时，不要运行该二进制；一键安装脚本会默认拒绝未提供校验文件的旧 Release。

---

## 🔐 凭据配置说明

各网盘凭据相互独立，按需配置。

### 1. 沃家云盘凭据 (`wopan_config.json`)

查找顺序：命令行 `-c` > `./wopan_config.json` > `~/.config/pan-relay/wopan.json` > 环境变量 `WOPAN_REFRESH_TOKEN`。
```json
{
  "refresh_token": "你的-refresh-token-uuid",
  "zone_url": "https://tjupload.pan.wo.cn"
}
```

### 2. 夸克网盘凭据 (`quark_config.json`)

查找顺序：命令行 `--quark-config` > `./quark_config.json` > `~/.config/pan-relay/quark.json` > 环境变量 `QUARK_COOKIE`。
```json
{
  "cookie": "_UP_A4A_11_=...; __puus=...; __pus=..."
}
```

### 3. 百度网盘凭据 (`baidu_config.json`)

查找顺序：命令行 `--baidu-config` > `./baidu_config.json` > `~/.config/pan-relay/baidu.json` > 环境变量 `BAIDU_COOKIE` / `BAIDU_ACCESS_TOKEN`。

- **方式 A（推荐：网页 Cookie 免配置）**：
  在浏览器登录 `pan.baidu.com`，F12 复制 Cookie 字符串（包含 `BDUSS=...`）：
  ```json
  {
    "cookie": "BDUSS=xxxxxx; STOKEN=yyyyyy"
  }
  ```
- **方式 B（官方开放平台 / Alist 模式）**：
  ```json
  {
    "access_token": "126.xxxxxx",
    "refresh_token": "127.yyyyyy",
    "client_id": "你的-App-Key",
    "client_secret": "你的-App-Secret"
  }
  ```

---

## 🛠️ 详细命令手册

全局通用参数：
- `-c, --config <path>`：指定 WoPan 配置文件；
- `--quark-config <path>`：指定 Quark 配置文件；
- `--baidu-config <path>`：指定 Baidu 配置文件；
- `-t, --threads <num>`：并发线程数（默认 4，推荐 4~8）；
- `-h, --help`：查看帮助；
- `version`：查看当前版本。

---

### 一、 跨网盘一键极速中转流水线 (`relay`) —— 最强核心！

在 VPS 上单行命令拉取源盘资源直推目标网盘，完成后自动销毁本地切片：

```bash
# 1. 【强烈推荐】开启 --stream 零磁盘流式穿透中转（边下边传、不占 VPS 硬盘、突破小鸡空间限制）
pan-relay relay quark:/来自：分享/山海情/EP01.mkv wopan:/emby/tv/山海情/ --stream -t 6
pan-relay relay baidu:/我的影视/奥本海默.2023.mkv wopan:/emby/movies/ --stream -t 6

# 2. 传统落盘暂存中转模式（先完整下载至本地临时切片，再推流上传并自动清理）
pan-relay relay quark:/来自：分享/山海情/EP01.mkv wopan:/emby/tv/山海情/ -t 6

# 3. 支持通过夸克 FID 直接中转
pan-relay relay quark:56c288c923b74cc88f2ffcc08292310c wopan:/emby/movies/ --stream -t 4
```

*中转过程输出效果：*
```text
[Relay Pipeline] Starting relay: QUARK [EP01.mkv] -> WOPAN [/emby/tv/山海情/] (Threads: 6)

[Stage 1/2] Downloading from QUARK...
  Progress: 100.0% ( 1931.8 / 1931.8 MB) | Avg Speed: 22.40 MB/s | Total Time: 86.2s
[✓] Download completed successfully: /tmp/relay_14205_EP01.mkv

[Stage 2/2] Uploading to WOPAN...
  Progress: 100.0% ( 1931.8 / 1931.8 MB) | Avg Speed: 18.50 MB/s | Total Time: 104.4s
[✓] Upload completed successfully! (FID: glQtb_bUmsOlrwu6/LsURAxk4S7Qr69U)

[✓] Relay completed! Local temporary staging file deleted automatically.
```

---

### 二、 百度网盘独立操作 (`baidu`)

```bash
# 浏览百度网盘指定目录
pan-relay baidu ls /
pan-relay baidu ls /我的影视

# 多线程并发高速下载百度网盘文件
pan-relay baidu download /我的影视/电影名.mkv /data/movies/ -t 6

# 百度网盘创建目录与删除文件
pan-relay baidu mkdir /我的影视 "经典纪录片"
pan-relay baidu rm /我的影视/已废弃文件.mkv
```

---

### 三、 夸克网盘独立操作 (`quark`)

```bash
# 浏览夸克网盘目录
pan-relay quark ls /来自：分享

# 多线程并发下载到本地（自动绕过大文件限制）
pan-relay quark download /来自：分享/电影名.mkv ./ -t 6
```

---

### 四、 沃家云盘独立操作 (`wopan`)

```bash
# 检查账号信息与空间容量
pan-relay wopan whoami

# 浏览沃家网盘目录
pan-relay wopan ls /emby/movies

# 多线程并发上传本地文件
pan-relay wopan upload /data/movies/Interstellar.2014.mkv /emby/movies/ -t 6

# 多线程分段下载文件
pan-relay wopan download /emby/movies/Interstellar.2014.mkv ./ -t 4

# 目录创建与删除
pan-relay wopan mkdir /emby/movies "科幻电影"
pan-relay wopan rm 51bd55b3ad2f4dd49cd5d01ab81194ac

# 主动换新凭据
pan-relay wopan refresh
```

---

## 🎬 自动化影视中转脚本实战

在海外中转 VPS 上编写简单的定时或监听流水线：

```bash
#!/usr/bin/env bash
# 影视自动中转: 遍历待转清单逐一推送到沃家云盘
set -euo pipefail

LIST=(
  "quark:/来自：分享/英雄联盟：双C之战/S01/01.mkv"
  "quark:/来自：分享/英雄联盟：双C之战/S01/02.mkv"
  "baidu:/4K影视/流浪地球2.mkv"
)

TARGET_DIR="/emby/movies"

for item in "${LIST[@]}"; do
    echo "=================================================="
    echo "开始中转任务: $item -> $TARGET_DIR"
    pan-relay relay "$item" "wopan:$TARGET_DIR" -t 6
    echo "任务完成，本地磁盘零残留！"
done
```

---

## 📄 开源许可证

本项目基于 [MIT 许可证](https://github.com/cnzhwei/pan-relay/blob/main/LICENSE) 开源。欢迎 Star、Fork 与提交 PR！
