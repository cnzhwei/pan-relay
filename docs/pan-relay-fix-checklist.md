# pan-relay 修复 Checklist

> 建立时间：2026-09-25
> 范围：`v2.2.0`（Emby 源端零磁盘中转与 UA 可配置集成）代码与发布元数据
> 原则：先写回归测试，再修复；只把有实际工具输出证明的项目标记为完成。

## 修复项

- [x] **P0｜流式 Range 响应严格校验**
  - 文件：`main.go`、新增 `internal/stream/`
  - 问题：`--stream` 接受 `200 OK`，未验证 `Content-Range`，服务端忽略 Range 时可能重复上传文件开头。
  - 验收：Range 请求必须返回 `206`，且 `Content-Range` 的起止位置与请求完全一致；否则任务失败。

- [x] **P0｜流式分片禁止短读/零填充**
  - 文件：`main.go`、新增 `internal/stream/`
  - 问题：`io.ErrUnexpectedEOF` 被忽略，短数据会以零字节补齐后上传。
  - 验收：实际读取字节数必须等于分片大小；短读、EOF、连接中断均返回错误，不得上传该分片。

- [x] **P1｜上传失败时取消其他 worker**
  - 文件：`internal/uploader/uploader.go`
  - 问题：单个分片失败后其他分片仍可能继续上传，产生远端半成品。
  - 验收：首个不可恢复分片错误触发 worker 取消；函数返回明确错误；父 context 取消不会被误报成功。远端半成品清理另列为独立待办。

- [x] **P1｜上传失败清理 WoPan 远端半成品**
  - 文件：`internal/uploader/uploader.go`、WoPan client 删除/abort 接口
  - 问题：本轮已取消其他 worker，但尚未实现失败后的远端对象清理。
  - 验收：上传失败后使用已返回的 FID 调用 WoPan 删除接口；删除失败会保留在返回错误中，不伪报成功。

- [x] **P1｜并发与输入边界校验**
  - 文件：`main.go`、`internal/uploader/uploader.go`
  - 问题：线程数无上限；`UploadStream` 对空文件、nil provider 等输入保护不足。
  - 验收：CLI 并发限制在安全范围；上传 API 拒绝非法文件大小和 nil provider。

- [x] **P1｜增加流式 Range 回归测试**
  - 文件：`internal/stream/range_test.go`、`internal/uploader/uploader_test.go`
  - 验收：覆盖 206 正常响应、200 忽略 Range、Content-Range 错位、短读；上传输入边界测试覆盖非法大小和 nil provider。

- [x] **P2｜修复版本元数据漂移**
  - 文件：`VERSION`
  - 问题：文件仍记录 `1.4.0`，代码和发布为 `2.1.0`。
  - 验收：`VERSION` 与当前发布版本一致。

- [x] **P2｜修复百度路径 JSON 拼接**
  - 文件：`internal/baidu/client.go`
  - 问题：路径使用字符串拼接构造 JSON，特殊字符可能导致请求体非法。
  - 验收：使用标准 JSON 编码；增加引号、反斜杠等特殊字符测试。

- [x] **P2｜统一配置持久化目录**
  - 文件：`internal/config/config.go`、`internal/quark/client.go`
  - 问题：WoPan/Quark 保存路径仍使用旧的 `~/.config/wopan-cli/`。
  - 验收：无既有配置路径时统一保存到 `~/.config/pan-relay/`。

- [x] **P1｜FID 中转保留夸克原始文件名**
  - 文件：`internal/quark/client.go`、`main.go`
  - 问题：直接使用 Quark FID 中转时，目标文件名会退化为 FID。
  - 验收：下载信息返回 `FileName`，Relay 使用服务端文件名并保留扩展名。

- [x] **P2｜补充发布安装完整性说明**
  - 文件：`install.sh`、`README.md`
  - 问题：安装脚本下载发布包后没有 checksum/signature 校验。
  - 当前进展：CI 生成 `SHA256SUMS.txt`，安装脚本拒绝无校验文件的 Release，README 增加手动校验命令；`v2.2.0` 已实际发布并回读资产验证。
  - 验收：`v2.2.0` 含 4 个平台归档和 `SHA256SUMS.txt`；Linux amd64 下载包 SHA-256 校验通过，解压后显示 `pan-relay v2.2.0`。

- [x] **P2｜修复发布归档内部文件名**
  - 文件：`.github/workflows/build-and-release.yml`
  - 问题：发布归档内部文件名为带平台后缀的构建名，`install.sh` 却只查找 `pan-relay`。
  - 验收：CI 打包时统一将 Linux/macOS 二进制归档为 `pan-relay`，Windows 归档为 `pan-relay.exe`。

- [x] **P2｜修复 Release 二进制重复版本前缀**
  - 文件：`.github/workflows/build-and-release.yml`
  - 问题：标签 `v2.1.1` 原样写入 `main.Version` 后，CLI 再添加 `v`，导致发布二进制显示 `vv2.1.1`。
  - 验收：CI 去除标签前缀后注入版本；正式 Release 二进制显示单一 `v2.1.2`。

- [x] **P1｜集成 Emby 源端零磁盘中转与可配置 UA**
  - 文件：`internal/emby/`、`main.go`、`README.md`、`docs/emby-integration.md`
  - 范围：Emby 登录、片名/IMDb/TMDB/ItemId 查询、PlaybackInfo、严格 Range 分片读取，并接入现有 WoPan `UploadStream`。
  - 验收：支持 `emby:<query> ... --stream`；HTTP `User-Agent` 与 `X-Emby-Authorization` 的 Client/Device/Version 可分别由配置、环境变量或 CLI 覆盖；多结果查询拒绝自动选错条目。

## 运行环境备注

- Lightvela 当前仍运行 `pan-relay v2.0.0`，本 checklist 不直接替换生产二进制。
- v2.2.0 仅发布源码和 Release，不自动部署到生产节点。

## 完成规则

- `[x]` 仅表示代码已修改且对应测试/验证命令通过。
- 未关闭项目不能在发布说明中表述为“已修复”。
