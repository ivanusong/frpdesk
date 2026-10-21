[English](README.md) | 简体中文

<img src="build/appicon.png" width="72" height="72" alt="">

# Frpdesk

Frpdesk 是 [frp](https://github.com/fatedier/frp) 的桌面客户端，通过图形界面将本地网站、API 和 TCP/UDP 服务开放到公网，无需手写客户端配置文件或单独运行 frpc。

![screenshot](./assets/screenshot.png)

## 功能

- HTTP、TCP、UDP 隧道
- 多 FRPS 服务器配置
- 服务状态、日志与 HTTP 请求统计
- macOS 菜单栏后台常驻
- 内置 HTTP 代理，支持 CORS 与 Host 改写
- 中英文界面与应用内更新

## 安装

前往 [GitHub Releases](https://github.com/ivanusong/frpdesk/releases/latest)，下载适合你设备的安装包：

| 平台 | 安装包 | 安装方式 |
| --- | --- | --- |
| macOS 12+（Apple Silicon） | `darwin-arm64.zip` | 解压后将应用拖入「应用程序」 |
| macOS 12+（Intel） | `darwin-amd64.zip` | 解压后将应用拖入「应用程序」 |
| Windows（x64） | `windows-amd64.exe` | 下载后直接运行 |
| Windows（ARM64） | `windows-arm64.exe` | 下载后直接运行 |
| Linux（x64） | `linux-amd64.tar.gz` | 解压后运行 `./frpdesk` |
| Linux（ARM64） | `linux-arm64.tar.gz` | 解压后运行 `./frpdesk` |

完整文件名会在上述平台后缀前包含应用名称和版本号。安装后可在应用内检查更新。

Linux 包面向 Ubuntu 24.04 或兼容的桌面系统，需要 GTK 4 与 WebKitGTK 6.0；Ubuntu 可执行 `sudo apt install libgtk-4-1 libwebkitgtk-6.0-4` 安装。Windows 和 Linux 的许可文档可下载发布页中单独的 `licenses.zip` 附件。

## 服务端准备（FRPS）

Frpdesk 只是客户端，仍需一台可用的 [frps](https://github.com/fatedier/frp) 服务器。建议使用较新版本；Frpdesk 内置的 frp 客户端为 v0.71.0，服务端版本应不低于该版本。

推荐的最小 `frps.toml`：

```toml
bindAddr = "0.0.0.0"
bindPort = 7000                 # 对应应用中的「服务器端口」

auth.method = "token"
auth.token = "a-long-random-secret"   # 对应应用中的「鉴权 Token」

# HTTP 隧道的子域名入口，仅在启用「支持 HTTP 域名」时需要
vhostHTTPPort = 80
subDomainHost = "example.com"   # 对应应用中的「根域名」，不带 "*."

# TCP/UDP 服务可占用的服务端端口范围，覆盖你计划使用的远程端口
allowPorts = [
  { start = 2000, end = 3000 },
  { single = 3001 },
  { start = 4000, end = 50000 },
]
```

随后把泛域名解析 `*.example.com` 指向该服务器的公网 IP，并放通防火墙中的 `bindPort`、`vhostHTTPPort` 以及 `allowPorts` 内的所有远程端口。

应用内服务器表单与 FRPS 配置的对应关系：

- **服务器地址 / 端口 / 鉴权 Token** 分别对应 `bindAddr` 的可达地址、`bindPort`、`auth.token`。Frpdesk 固定使用 token 鉴权方式，Token 留空只在服务端未配置 token 时才能连上。
- **根域名** 必须与 `subDomainHost` 一致，且不带 `*.` 前缀。
- **访问协议 / 访问端口** 只描述对外展示的访问地址，不会为 FRPS 开启 TLS，也不会启用 `vhostHTTPPort` 或 `vhostHTTPSPort`。FRPS 直接对外提供 `vhostHTTPPort` 时选 `http` 并填写该端口；FRPS 位于反向代理或网关之后、由它终结 HTTPS 时，按实际公开入口选 `https` 和公开端口。
- Frpdesk 默认启用客户端 TLS，控制连接无需服务端证书；若要拒绝明文连接，在服务端设置 `transport.tls.force = true`。
- `allowPorts` 可选但建议配置：配置后，TCP/UDP 服务的远程端口超出范围或已被其它代理占用时会注册失败；未配置时任意非保留端口均可使用。

可选的 Dashboard（Frpdesk 不依赖）：

```toml
webServer.addr = "127.0.0.1"
webServer.port = 7500
webServer.user = "admin"
webServer.password = "another-secret"
```

完整配置项见 [frps 参考配置](https://github.com/fatedier/frp/blob/dev/conf/frps_full_example.toml)。

## 快速使用

使用前需要准备一台可连接的 **FRPS 服务器**，以及服务器地址、端口和鉴权 Token（如已启用）。

1. 打开 Frpdesk，在「配置」中添加 FRPS 服务器信息，字段含义见上文的「服务端准备」。
2. 新增 HTTP、TCP 或 UDP 服务，选择目标服务器，填写本地 IP 和端口，例如 `127.0.0.1:3000`。
3. HTTP 服务填写子域名前缀；TCP/UDP 服务填写远程端口。使用 HTTP 前，需要先在服务端配置 HTTP 域名入口和泛域名解析，再在应用的服务器配置中启用「支持 HTTP 域名」并填写对应的公开入口信息。
4. 启用服务器和服务，通过界面显示的访问地址连接本地服务。连接失败时可在应用内查看状态与日志。

## 许可证

[Apache-2.0](LICENSE)。第三方声明见 [THIRD_PARTY_NOTICES.md](THIRD_PARTY_NOTICES.md)。
