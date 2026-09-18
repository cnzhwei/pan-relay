# wopan-cli

沃家云盘（WoPan / 中国联通云盘）轻量级多线程命令行客户端。

专门针对海外中转 VPS、轻量服务器及无图形环境设计的单文件绿色工具。摆脱传统全功能网盘（如 OpenList / Alist）沉重的 Web 服务、数据库和常驻后台负担，实现**即开即用、多线程高速传输、Token 自动续期与无痕操作**。

---

## 核心特性

- **单静态二进制（纯绿色免依赖）**：基于 Go 原生开发，`CGO_ENABLED=0` 纯静态编译，零外部动态库依赖，原生兼容 **Alpine Linux（musl）**、**Debian / Ubuntu（glibc）** 以及 CentOS 等所有主流发行版；
- **多线程分块并发上传**：基于沃家云盘底层 8MB 切片协议（`upload2C`），通过并发 Goroutine 线程池（默认 4 线程）推送分块，彻底跑满 VPS 到联通骨干网的物理上行带宽；
- **多线程分段并发下载**：基于 HTTP `Range` 多连接并发抓取分段数据并局部合并写入，极大提升回拉与中转下载速度；
- **实时终端进度看板**：传输过程中实时显示完成百分比、已传大小、传输速率（MB/s）与耗时统计；
- **Token 自动续期与持久化闭环**：基于 `refresh_token` 实现长效免密登录；检测到短效 `access_token` 过期时自动换新并回写本地小配置文件，无需人工扫码或干预；
- **路径化语义交互**：支持类似 Linux 本地文件系统的路径导航（如 `ls /emby/movies`），支持自动路径层级递归解析。

---

## 快速上手

### 1. 配置登录凭据

`wopan-cli` 默认按顺序查找以下位置的配置文件：
1. 命令行参数指定：`-c /path/to/config.json`
2. 当前目录：`./wopan_config.json`
3. 用户目录：`~/.config/wopan-cli/config.json` 或 `~/.wopan_config.json`
4. 环境变量：`WOPAN_REFRESH_TOKEN` 与 `WOPAN_ACCESS_TOKEN`

**配置文件格式（`wopan_config.json`）示例**：
```json
{
  "refresh_token": "xxxxxxxx-xxxx-xxxx-xxxx-xxxxxxxxxxxx",
  "access_token": "yyyyyyyy-yyyy-yyyy-yyyy-yyyyyyyyyyyy",
  "zone_url": "https://tjupload.pan.wo.cn"
}
```

---

## 命令参考

### 1. 账号与状态
```bash
# 查看当前登录账号及用户 ID
wopan-cli whoami

# 强制主动刷新当前登录凭据
wopan-cli refresh
```

### 2. 浏览与目录管理
```bash
# 浏览根目录
wopan-cli ls /

# 浏览指定目录路径或目录 ID
wopan-cli ls /emby/movies
wopan-cli ls 482bad1ef0cf40fcbaee8d426c5c9b43

# 创建新目录 (父目录 目录名)
wopan-cli mkdir /emby/movies "新建合集"

# 删除指定文件 FID 或目录 ID
wopan-cli rm <id_or_fid>
```

### 3. 多线程高速上传
```bash
# 默认 4 线程上传到网盘根目录
wopan-cli upload /data/movies/流浪地球.mkv /

# 指定目标路径并开启 6 线程并发上传
wopan-cli upload /data/movies/流浪地球.mkv /emby/movies/ -t 6
```

### 4. 多线程高速下载
```bash
# 通过文件 FID 或网盘完整路径下载到本地当前目录 (默认 4 线程)
wopan-cli download /emby/movies/流浪地球.mkv ./

# 指定并发数下载到目标路径
wopan-cli download <file_fid> /data/download/ -t 8
```

---

## 自动化构建与 CI/CD

本项目已集成 GitHub Actions 自动化构建流水线：
- 每次推送代码或发布标签时，自动交叉编译：
  - `wopan-cli-linux-amd64`（x86_64 静态无依赖二进制）
  - `wopan-cli-linux-arm64`（aarch64 静态无依赖二进制）
  - `wopan-cli-darwin-arm64`（Apple Silicon）
  - `wopan-cli-windows-amd64.exe`（Windows 64位）
- 打 Git 标签（如 `v1.0.0`）自动触发 GitHub Release 打包交付。
