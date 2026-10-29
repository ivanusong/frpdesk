[English](server.md) | [简体中文](server_zh-CN.md)

# 用 Docker Compose 部署服务端

部署 Frpdesk 服务端需要的东西都在 [`server/`](../server) 目录里：一个只跑 frps 的单容器
Compose 栈。你只需要改 `.env` 一个文件，不用碰 YAML 或 TOML。

前提条件：

- 一台装了 Docker 和 compose 插件的 Linux 服务器。
- 一个你拥有的域名（使用 HTTP 域名隧道时需要泛解析）。

## 1. 把文件传到服务器

```bash
scp -r server/ you@your-server:~/frpdesk-server
ssh you@your-server
cd frpdesk-server
```

## 2. 填写 `.env`

```bash
cp .env.example .env
openssl rand -hex 32      # 输出用作 FRPS_TOKEN
nano .env
```

必须填写的变量：

| 变量 | 示例 | 说明 |
| --- | --- | --- |
| `FRPS_DOMAIN` | `example.com` | 根域名，不带 `*.` 前缀。同时是 Frpdesk 里的「根域名」。 |
| `FRPS_TOKEN` | `9f1c...` | 任意长随机串。同时是 Frpdesk 里的「鉴权 Token」。 |
| `FRPS_BIND_PORT` | `7000` | 同时是 Frpdesk 里的「服务器端口」。 |

`FRPS_REMOTE_PORTS` 默认为 `20000-20099`，是 TCP/UDP 服务可用的端口范围。需要更多端口时
调大即可，TCP 与 UDP 都会映射。

## 3. DNS 记录

在 DNS 服务商处添加两条 `A` 记录，都指向服务器公网 IP：

- `example.com`
- `*.example.com`

泛解析决定了隧道启用后 `blog.example.com` 能否解析到。顶点域名要单独加，因为泛解析不匹配
裸域名本身。

## 4. 启动

```bash
docker compose up -d
docker compose logs -f frps    # 确认已开始监听，结束后 Ctrl+C
```

防火墙放通 `FRPS_BIND_PORT` 和 `FRPS_REMOTE_PORTS`。如果自己另跑反向代理，再放通它监听的
端口（通常是 `80` 和 `443`）。

## 5. 在 Frpdesk 中填写

| 字段 | 值 |
| --- | --- |
| 服务器地址 | `example.com` 或服务器 IP |
| 服务器端口 | `7000` |
| 鉴权 Token | 你的 `FRPS_TOKEN` |
| 支持 HTTP 域名 | 启用 |
| 根域名 | `example.com` |
| 访问协议 | `http` |
| 访问端口 | `7000`（实际 `FRPS_BIND_PORT`） |

使用默认端口时，名为 `blog` 的服务对应 `http://blog.example.com:7000`。

如果你自己用反向代理终结了 TLS，把「访问协议」改成 `https`，「访问端口」填反代的公网
端口（标准端口 `443` 留空即可）。

## HTTPS

frps 只为 HTTP 隧道提供明文入口，自身无法终结 TLS：`vhostHTTPSPort` 只是面向 `https`
类型代理的 SNI 透传，而 Frpdesk 只会创建 `http` 类型代理。因此 TLS 终止由你自备的反向代理
负责，本项目不再内置。

你的反向代理需要满足：

1. 转发目标是 frps 的 `FRPS_BIND_PORT`——控制连接与 HTTP 入口复用同一个端口。
2. 保留原始 `Host` 头，frps 依据它路由到对应隧道。
3. 不设置读写超时上限，否则 SSE、流式响应和大文件上传会被截断。
4. Frpdesk 的控制连接必须直连 `FRPS_BIND_PORT`，不要经过反向代理。frps 强制
   `transport.tls.force = true`，Frpdesk 始终以 TLS 建立控制连接。

即使反向代理与 frps 同机，也要保持远程 Frpdesk 客户端能够访问 `FRPS_BIND_PORT`。

## 工作原理

```
浏览器     --http-->  frps:7000  --隧道-->  Frpdesk
Frpdesk    --tls-->   frps:7000  （同一端口复用）
```

compose 已经配好了两个对真实流量很关键的选项，不需要你操心：

- `transport.tls.force = true`：拒绝不使用 TLS 的控制连接。Frpdesk 始终使用 TLS，且这不
  影响同一端口上的 HTTP 入口。
- `vhostHTTPTimeout = -1`：取消 vhost 网关默认 60 秒的限制，SSE、流式响应和大文件上传不会
  被截断。

## 排查

- **`docker compose up` 报某个变量缺失。** `.env` 里有必填项没填，报错会指出变量名。
- **返回 frps 自己的 404 页面。** 子域名未知或对应隧道未启用；如果你用了自备反向代理，
  也可能是反代丢了 `Host` 头。
- **你自备的反向代理返回 `502`。** frps 还没起来，或启动后改过 `FRPS_BIND_PORT`。执行
  `docker compose logs frps`。
- **Frpdesk 一直显示「正在连接」。** 控制连接不经过反向代理。检查 `FRPS_BIND_PORT` 是否
  已在防火墙放通且客户端可达，以及 `FRPS_TOKEN` 是否完全一致。
- **TCP/UDP 服务启动失败。** 远程端口必须在 `FRPS_REMOTE_PORTS` 范围内，且未被其它服务占用。

## 升级

```bash
docker compose pull
docker compose up -d
```

从旧版双容器方案升级时，先准备好自己的 TLS 终止，再执行
`docker compose up -d --remove-orphans` 移除旧代理容器。已有证书文件及 `.env` 中不再使用的
变量会保留，本栈不再读取它们。

frps 版本需不低于 Frpdesk 内置的 frp 客户端版本，因此不要把镜像钉到比 README 中标注的
客户端版本更旧。
