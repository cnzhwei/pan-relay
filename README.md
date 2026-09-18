# wopan-cli

<p align="center">
  <strong>沃家云盘（WoPan / 中国联通云盘）轻量级多线程命令行中转工具</strong>
</p>

<p align="center">
  <a href="https://github.com/cnzhwei/wopan-cli/releases"><img src="https://img.shields.io/github/v/release/cnzhwei/wopan-cli?color=blue&logo=github" alt="Release"></a>
  <a href="https://github.com/cnzhwei/wopan-cli/actions"><img src="https://img.shields.io/github/actions/workflow/status/cnzhwei/wopan-cli/build-and-release.yml?logo=github&label=build" alt="Build Status"></a>
  <a href="https://github.com/cnzhwei/wopan-cli/blob/main/LICENSE"><img src="https://img.shields.io/badge/License-MIT-green.svg" alt="License"></a>
  <img src="https://img.shields.io/badge/Language-Go%201.24-blue.svg?logo=go" alt="Go Version">
  <img src="https://img.shields.io/badge/Arch-x86__64%20%7C%20arm64-orange.svg" alt="Architecture">
</p>

---

## 📖 项目背景与痛点

在构建基于家庭影音库（Emby / Jellyfin / Plex）的影视中转与云端备份方案时，许多用户会使用海外 VPS 或轻量云主机作为“资源下载与转存节点”，最终将媒体资源归档至**中国联通沃家云盘（WoPan）**。

过去主流的方案是在 VPS 上搭建 **OpenList** 或 **Alist**。但在实际生产和中转场景中，这种方式存在明显的瓶颈与痛点：
1. **服务沉重**：Alist / OpenList 包含完整的 Web 前端、多种存储驱动适配层、数据库（SQLite/MySQL）和常驻 Daemon 守护，往往占用 150MB~300MB 内存，配置复杂；
2. **大文件传输单线程受限**：WebDAV 或普通 HTTP 单线程在跨国或骨干网抖动时，TCP 拥塞窗口极易折半，导致传输速度严重受限；
3. **临时测试与自动化困难**：在一些低配 VPS 或测试机器上，临时传一个文件需要重新部署 Docker、配置数据库、挂载存储，极其繁琐。

**`wopan-cli` 为此而生：**
它是一个**独立的、单静态二进制、开箱即用**的命令行客户端。**不带 Web 界面、不依赖数据库、无任何外部依赖**，支持 8MB 分片多线程并发推流与 HTTP Range 多线程并发下载，专为极速上传、回拉与脚本自动化中转设计！

---

## ✨ 核心特性

- **🚀 单静态绿色二进制**：基于 Go 语言原生编写，采用 `CGO_ENABLED=0` 纯静态编译，无任何动态链接库依赖。在 **Alpine Linux（musl）**、**Debian / Ubuntu（glibc）**、CentOS 及 macOS 上扔进去直接就能跑；
- **⚡ 多线程并发切片上传**：深入适配联通云盘底层 `upload2C` 协议，采用 Goroutine 线程池（默认 4 线程，支持自定义）并发上传 8MB 数据块，彻底跑满 VPS 往联通骨干机房的物理上行带宽；
- **📥 多线程并发分段下载**：自动探测服务端 HTTP `Range` 支持，多连接并发拉取并原位合并写入，大文件下载速度成倍提升；
- **📊 实时终端速度看板**：传输过程中提供平滑的终端进度看板，实时输出传输百分比、已完成/总大小、平均传输速率（MB/s）及耗时；
- **🔑 Token 自动续期与持久化闭环**：基于 `refresh_token` 实现永久免密免登录。短效 `access_token` 过期时，客户端自动调用联通官方接口换新并回写本地配置文件，**一次配置，终身免干预**；
- **📂 类本地文件系统语义交互**：支持 `/emby/movies` 等直观的虚拟路径导航与递归层级解析，支持远程新建目录与删除。

---

## 📦 安装指南

### 方式一：一键自动安装（最推荐）

支持任何 **Linux（x86_64 / aarch64）** 或 **macOS（Apple Silicon）**，自动识别系统架构并安装至 `/usr/local/bin`：

```bash
curl -fsSL https://raw.githubusercontent.com/cnzhwei/wopan-cli/main/install.sh | sh
```

### 方式二：手动下载预编译 Release 包

访问 [GitHub Releases 页面](https://github.com/cnzhwei/wopan-cli/releases) 下载对应平台的打包文件：

```bash
# 以 Linux x86_64 为例：
curl -sL https://github.com/cnzhwei/wopan-cli/releases/latest/download/wopan-cli-linux-amd64.tar.gz | tar -xz
chmod +x wopan-cli
sudo mv wopan-cli /usr/local/bin/
```

### 方式三：源码编译（需要本地已安装 Go 1.20+）

```bash
git clone https://github.com/cnzhwei/wopan-cli.git
cd wopan-cli
CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o bin/wopan-cli .
```

---

## 🔐 凭据配置指南

`wopan-cli` 仅需一组长效 `refresh_token` 即可运行。

### 1. 凭据从哪里获取？

#### 途径 A：从已有的 OpenList / Alist 导出（最方便）
如果你已经部署了 OpenList 或 Alist 并且成功挂载了沃家云盘：
- **方式 1（管理后台）**：打开后台 `存储` -> 找到 `/wopan` -> 点击编辑，直接复制 `刷新令牌 (refresh_token)`。
- **方式 2（SQLite 数据库提取）**：
  ```bash
  sqlite3 /path/to/data.db 'SELECT addition FROM x_storages WHERE driver="WoPan"'
  ```
  在返回的 JSON 中复制 `"refresh_token"` 字段的值。

#### 途径 B：从联通云盘网页端抓取
1. 在电脑浏览器上打开 [中国联通云盘官网 (pan.wo.cn)](https://pan.wo.cn/) 并扫码或验证码登录；
2. 按 `F12` 打开开发者工具，切换到 `Application (应用)` -> `Local Storage (本地存储)`；
3. 搜索或查看键名 `refresh_token`（或 `userInfo`），复制对应的 UUID 字符串。

---

### 2. 配置保存方式

`wopan-cli` 支持以下四种配置方式，优先级从高到低：

#### 选项 1：当前工作目录放置 `wopan_config.json`
在当前执行目录下创建 `wopan_config.json`：
```json
{
  "refresh_token": "你的-refresh-token-uuid",
  "zone_url": "https://tjupload.pan.wo.cn"
}
```

#### 选项 2：全局用户配置（跨目录通用）
将配置文件放入 `~/.config/wopan-cli/config.json`：
```bash
mkdir -p ~/.config/wopan-cli
cat << 'EOF' > ~/.config/wopan-cli/config.json
{
  "refresh_token": "你的-refresh-token-uuid",
  "zone_url": "https://tjupload.pan.wo.cn"
}
EOF
chmod 600 ~/.config/wopan-cli/config.json
```

#### 选项 3：通过环境变量指定（非常适合 Docker 与脚本流水线）
```bash
export WOPAN_REFRESH_TOKEN="你的-refresh-token-uuid"
# 可选：家庭空间 ID（不填默认个人空间）
# export WOPAN_FAMILY_ID="xxxx"
```

#### 选项 4：命令行显式指定配置文件路径
```bash
wopan-cli -c /opt/secrets/my_wopan.json whoami
```

---

## 🛠️ 详细命令参考

全局通用参数：
- `-c, --config <path>`：指定配置文件路径；
- `-t, --threads <num>`：并发线程数（默认 4，推荐 4~8）；
- `-h, --help`：查看帮助。

### 1. 验证登录状态 (`whoami`)

测试连接并输出当前账号信息，若短效 Token 已过期，将自动触发静默续期：
```bash
wopan-cli whoami
```
*输出示例：*
```text
=== WoPan Account Info ===
User Name : 联通用户_185xxxx
User ID   : 18569452810
Reg Time  : 2023-08-06 15:35:47
Config    : ~/.config/wopan-cli/config.json
```

### 2. 浏览网盘目录 (`ls`)

支持按根目录 `/`、多级路径（如 `/emby/movies`）或原始目录 ID 浏览：
```bash
# 浏览网盘根目录
wopan-cli ls /

# 浏览指定子目录
wopan-cli ls /emby/movies

# 浏览指定目录 ID
wopan-cli ls 482bad1ef0cf40fcbaee8d426c5c9b43
```
*输出示例：*
```text
=== WoPan Directory [/emby/movies] (3 items) ===
  TYPE    SIZE          MODIFIED              ID / FID                            NAME
  -------------------------------------------------------------------------------------------------
  [DIR ]  -             2026-07-22 14:04:01   482bad1ef0cf40fcbaee8d426c5c9b43    流浪地球2 (2023)
  [FILE]  14.25 GB      2026-08-15 19:22:10   glQtb_bUmsOlrwu6/LsURAxk4S7Qr69U    奥本海默.mkv
  [FILE]  8.12 GB       2026-09-02 11:05:33   KlmNa_xZmsPlrwu9/PsVRAxk9S2Tr81A    沙丘2.mkv
```

### 3. 多线程并发上传 (`upload`)

将本地文件推送到网盘指定目录。支持多线程并发切片上传，自动显示进度与实时速率：
```bash
# 默认 4 线程上传到网盘根目录
wopan-cli upload ./The.Matrix.1999.mkv /

# 上传到指定网盘路径，并开 6 线程提速
wopan-cli upload ./Interstellar.2014.mkv /emby/movies/ -t 6

# 上传到指定目录 ID
wopan-cli upload ./video.mp4 482bad1ef0cf40fcbaee8d426c5c9b43 -t 8
```
*终端看板展示：*
```text
Uploading Interstellar.2014.mkv (12.45 GB) -> WoPan Dir [/emby/movies] with 6 threads...
  Progress:  68.5% ( 8530.0 / 12748.8 MB) | Speed: 32.40 MB/s | Elapsed: 263s
```

### 4. 多线程并发下载 (`download`)

支持通过**网盘完整路径**或**文件 FID** 进行多线程 Range 分段加速下载：
```bash
# 通过网盘路径直接下载到当前目录
wopan-cli download /emby/movies/奥本海默.mkv ./

# 通过文件 FID 下载并指定保存路径与 8 线程加速
wopan-cli download "glQtb_bUmsOlrwu6/LsURAxk4S7Qr69U" /data/movies/奥本海默.mkv -t 8
```
*说明：未完成的下载文件会自动带上 `.download.tmp` 后缀，校验无误后原子性重命名为正式文件，中途失败绝不产生损坏脏数据。*

### 5. 远程目录与文件管理 (`mkdir` / `rm`)
```bash
# 在 /emby/movies 目录下新建 "科幻电影" 文件夹
wopan-cli mkdir /emby/movies "科幻电影"

# 删除指定文件 FID 或目录 ID
wopan-cli rm 51bd55b3ad2f4dd49cd5d01ab81194ac
```

### 6. 主动凭据换新 (`refresh`)
```bash
# 手动触发一次 Token 轮转与配置文件更新
wopan-cli refresh
```

---

## 🎬 家庭影音自动化中转实战

结合海外中转 VPS 与沃家云盘的典型自动化脚本示例：

```bash
#!/usr/bin/env bash
# 影音自动中转流水线示例: 下载完成后自动并发上传 WoPan 并清理本地
set -euo pipefail

DOWNLOAD_DIR="/data/downloads"
REMOTE_TARGET="/emby/movies"

# 1. 遍历下载完成的视频文件
for file in "$DOWNLOAD_DIR"/*.mkv; do
    [ -f "$file" ] || continue
    filename="$(basename "$file")"
    echo "[+] 开始中转上传: $filename"

    # 2. 调用 wopan-cli 6线程并发极速推流
    wopan-cli upload "$file" "$REMOTE_TARGET" -t 6

    # 3. 上传成功后立即删除本地文件，释放小盘 VPS 磁盘空间
    echo "[✓] 上传完成，清理本地缓存: $file"
    rm -f "$file"
done
```

---

## ❓ 常见问题 (FAQ)

#### Q1: `refresh_token` 会过期吗？需要定期重新扫码抓取吗？
**答：** 联通云盘的 `refresh_token` 属于长效会话令牌（有效期通常数月乃至更长），只要账号没有在大版本强制下线或改密码，`wopan-cli` 每次执行都会自动用它续期短效 `access_token` 并自动落盘保存更新后的 Token，无需人工频繁干预。

#### Q2: 为什么选择并发线程数为 4~8？开到 32 线程会不会更快？
**答：** 沃家云盘的分块单片固定为 8MB。对于大多数 100M~500M 带宽的 VPS，**4~8 线程并发已经能够充分把 TCP 拥塞窗口填满并跑满物理上行**。并发开得过高不仅会增加客户端 CPU 切换开销，还容易触发服务端的连接并发频控保护。

#### Q3: 提示 `certificate verify failed` 证书问题？
**答：** 检查系统时间是否精准。联通网盘 API 校验严格的时间戳签名，若 VPS 本地时钟偏差超过数分钟，会导致鉴权失败。运行 `ntpdate ntp.aliyun.com` 校准系统时间即可解决。

#### Q4: Alpine Linux 运行提示缺少动态库？
**答：** `wopan-cli` 发布版默认采用 `CGO_ENABLED=0` 纯静态编译，没有任何 `glibc` 依赖，经过实机在 Alpine（musl libc）与 Debian 12 验证，均为免依赖原生执行。

---

## 📄 开源许可证

本项目基于 [MIT 许可证](https://github.com/cnzhwei/wopan-cli/blob/main/LICENSE) 开源。欢迎 Star、Fork 与提交 PR！
