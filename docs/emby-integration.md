# Emby 源端集成

## 数据路径

```text
Emby 登录/查询
  → PlaybackInfo 获取媒体源与大小
  → HTTP 206 Range 严格读取
  → 内存分片
  → WoPan UploadStream
```

使用 `--stream` 时不会在 pan-relay 本地创建视频临时文件。

## User-Agent 与客户端身份

```text
HTTP User-Agent:
  --emby-ua > EMBY_USER_AGENT > emby.json:user_agent > Client/Version

X-Emby-Authorization:
  Client  ← --emby-client / EMBY_CLIENT / emby.json:client
  Device  ← --emby-device / EMBY_DEVICE / emby.json:device
  Version ← --emby-version / EMBY_CLIENT_VER / emby.json:version
```

示例：

```bash
pan-relay relay emby:tt1234567 wopan:/emby/movies/ --stream \
  --emby-ua 'Hills Lite/1.3.0' \
  --emby-client 'Hills Lite' \
  --emby-device Windows \
  --emby-version 1.3.0
```

Emby 查询支持：

- 片名：`emby:片名`
- IMDb：`emby:tt1234567`
- TMDB：`emby:tmdb:12345`
- ItemId：`emby:item:服务器条目ID`

如果片名返回多个结果，程序会拒绝自动选择，需改用 IMDb、TMDB 或 ItemId 精确指定。

## 服务端适配策略

`service` 只描述服务端协议差异，客户端身份仍由 `client`、`device`、`version` 和 User-Agent 独立控制。

```json
{
  "base_url": "https://sw.vemby01.de:443",
  "username": "your_username",
  "password": "your_password",
  "service": "vemby",
  "client": "Hills Lite",
  "device": "Windows",
  "version": "1.3.0",
  "user_agent": "Hills Lite/1.3.0",
  "item_lookup": "detail"
}
```

内置策略：

- `guai`：默认兼容策略，优先使用 `Ids + Fields` 查询，并保留 `/emby` 认证路径回退；
- `vemby`：标准详情查询 `/Users/{user}/Items/{id}`，认证使用 `/Users/AuthenticateByName`。

也可以使用环境变量覆盖：`EMBY_SERVICE`、`EMBY_ITEM_LOOKUP`。Vemby 的官方 `Items/{id}/Download` 可能返回 403，pan-relay 不依赖该接口，而是使用带认证的 `Videos/{id}/stream` Range 直通流。

## 凭据隔离

默认配置路径：

```text
~/.config/pan-relay/emby.json
```

也可以使用环境变量：`EMBY_URL`、`EMBY_USER`、`EMBY_PASS`、`EMBY_USER_AGENT`、`EMBY_CLIENT`、`EMBY_DEVICE`、`EMBY_CLIENT_VER`。

凭据、Token、会话和媒体下载内容不进入 Git 仓库，也不会写入普通日志。