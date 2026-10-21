[English](server.md) | [简体中文](server_zh-CN.md)

# 用 Docker Compose 部署服务端

部署 Frpdesk 服务端需要的东西都在 [`server/`](../server) 目录里：frps，加上自动签发并续期
泛域名证书的 Traefik。你只需要改 `.env` 一个文件，不用碰 YAML 或 TOML。

前提条件：

- 一台装了 Docker 和 compose 插件的 Linux 服务器。
- 一个你拥有的域名，且有权限添加 DNS 记录。
- DNS 服务商的 API 凭证。泛域名证书只能通过 DNS-01 校验签发，这一步绕不开。

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
| `ACME_EMAIL` | `you@example.com` | Let's Encrypt 续期通知邮箱。 |
| `DNS_PROVIDER` | `cloudflare` | 也可用 `alidns`、`dnspod` 等，见 [lego 服务商列表](https://go-acme.github.io/lego/dns/)。 |
| 该服务商对应的凭证 | `CF_DNS_API_TOKEN=...` | 只填与 `DNS_PROVIDER` 匹配的那一组。 |

凭证请只授予该域名的 DNS 修改权限，不要使用账号级主密钥。

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
docker compose logs -f traefik    # 观察证书签发过程，结束后 Ctrl+C
```

首次启动需要等待 DNS-01 校验，最多一分钟。日志不再报错即完成。证书存放在
`letsencrypt/acme.json`，之后自动续期。

防火墙放通 `443`、`80`、`FRPS_BIND_PORT` 和 `FRPS_REMOTE_PORTS`。

## 5. 在 Frpdesk 中填写

| 字段 | 值 |
| --- | --- |
| 服务器地址 | `example.com` 或服务器 IP |
| 服务器端口 | `7000` |
| 鉴权 Token | 你的 `FRPS_TOKEN` |
| 支持 HTTP 域名 | 启用 |
| 根域名 | `example.com` |
| 访问协议 | `https` |
| 访问端口 | 留空 |

名为 `blog` 的服务对应 `https://blog.example.com`。

## 工作原理

只有对外的 HTTP 入口经过 Traefik。Frpdesk 直连 frps 的 `FRPS_BIND_PORT`，因为 frps 在这
一个端口上同时复用控制连接与 HTTP 入口。

```
浏览器     --https-->  Traefik:443  --http-->  frps  --隧道-->  Frpdesk
Frpdesk    --tls------------------------->  frps:7000（不经代理）
```

frps 自身无法为 HTTP 隧道终结 TLS：`vhostHTTPSPort` 只是面向 `https` 类型代理的 SNI 透传
端口，而 Frpdesk 只会创建 `http` 类型代理。这就是 `https://` 必须有前置代理的原因。

compose 已经配好了两个对真实流量很关键的选项，不需要你操心：

- `transport.tls.force = true`：拒绝不使用 TLS 的控制连接。Frpdesk 始终使用 TLS，且这不会
  影响 Traefik 后方的 HTTP 入口。
- `vhostHTTPTimeout = -1`：取消 vhost 网关默认 60 秒的限制，SSE、流式响应和大文件上传不会
  被截断。

## 排查

- **`docker compose up` 报某个变量缺失。** `.env` 里有必填项没填，报错会指出变量名。
- **证书一直签不下来。** `docker compose logs traefik` 会给出 ACME 错误。绝大多数是 DNS
  凭证填错或缺失，或 DNS 生效慢于 `ACME_DELAY_BEFORE_CHECK`。Let's Encrypt 对失败有频率
  限制，先修好凭证再重试，不要反复重启。
- **`502 Bad Gateway`。** frps 还没起来，或启动后改过 `FRPS_BIND_PORT`。
  执行 `docker compose logs frps`。
- **返回 frps 自己的 404 页面。** `Host` 头丢了，检查 `docker-compose.yml` 里的
  `passhostheader` 标签仍为 `true`。
- **HTTPS 能访问，但 Frpdesk 一直显示「正在连接」。** 控制连接不经过 Traefik，检查
  `FRPS_BIND_PORT` 是否已在防火墙放通且客户端可达，以及 `FRPS_TOKEN` 是否完全一致。
- **TCP/UDP 服务启动失败。** 远程端口必须在 `FRPS_REMOTE_PORTS` 范围内，且未被其它服务占用。
- **泛域名证书覆盖不到某个子域名。** `*.example.com` 只覆盖一层，`a.b.example.com` 需要
  单独的证书。

## 升级

```bash
docker compose pull
docker compose up -d
```

frps 版本需不低于 Frpdesk 内置的 frp 客户端版本，因此不要把镜像钉到比 README 中标注的
客户端版本更旧。
