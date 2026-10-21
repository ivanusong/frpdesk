English | [简体中文](README_zh-CN.md)

<img src="build/appicon.png" width="72" height="72" alt="">

# Frpdesk

Frpdesk is a desktop client for [frp](https://github.com/fatedier/frp). Expose local websites, APIs, and TCP/UDP services through a graphical interface, without writing client configuration files or running frpc separately.

![screenshot](./assets/screenshot.png)

## Features

- HTTP, TCP, and UDP tunnels
- Multiple FRPS server configurations
- Service status, logs, and HTTP request statistics
- Menu-bar background running on macOS
- Built-in HTTP proxy with CORS and Host rewriting
- Chinese and English interface, with in-app updates

## Install

Download the matching package from [GitHub Releases](https://github.com/ivanusong/frpdesk/releases/latest):

| Platform | Package | Installation |
| --- | --- | --- |
| macOS 12+ (Apple Silicon) | `darwin-arm64.zip` | Extract and move the app to Applications |
| macOS 12+ (Intel) | `darwin-amd64.zip` | Extract and move the app to Applications |
| Windows (x64) | `windows-amd64.exe` | Download and run |
| Windows (ARM64) | `windows-arm64.exe` | Download and run |
| Linux (x64) | `linux-amd64.tar.gz` | Extract and run `./frpdesk` |
| Linux (ARM64) | `linux-arm64.tar.gz` | Extract and run `./frpdesk` |

Package names include the app name and version before the platform suffix shown above. You can check for updates inside the app after installation.

Linux packages target Ubuntu 24.04 or compatible desktop systems and require GTK 4 and WebKitGTK 6.0. On Ubuntu, install them with `sudo apt install libgtk-4-1 libwebkitgtk-6.0-4`. License documents for Windows and Linux are available in the separate `licenses.zip` release asset.

## Server Setup (FRPS)

Frpdesk is a client only; you still need a running [frps](https://github.com/fatedier/frp) server. Use a recent frps release; Frpdesk embeds frp v0.71.0 as its client, so the server should be that version or newer.

Recommended minimal `frps.toml`:

```toml
bindAddr = "0.0.0.0"
bindPort = 7000                 # Server port in Frpdesk

auth.method = "token"
auth.token = "a-long-random-secret"   # Auth token in Frpdesk

# HTTP tunnels: subdomain entry. Required only if you enable HTTP domains.
vhostHTTPPort = 80
subDomainHost = "example.com"   # Base domain in Frpdesk (no "*." prefix)

# Ports TCP/UDP services may occupy on the server. Cover the remote ports you plan to use.
allowPorts = [
  { start = 2000, end = 3000 },
  { single = 3001 },
  { start = 4000, end = 50000 },
]
```

Then point a wildcard DNS record at the server so `*.example.com` resolves to its public IP, and open the firewall for `bindPort`, `vhostHTTPPort`, and every remote port in `allowPorts`.

Notes on how the Frpdesk server form maps to FRPS:

- **Server address / port / auth token** are `bindAddr` reachability, `bindPort`, and `auth.token`. Frpdesk always authenticates with the token method; leaving the token empty only works if FRPS has no token configured.
- **Base domain** must equal `subDomainHost`, without the `*.` prefix.
- **Access scheme / port** only describe the public URL shown in the app; they do not enable TLS on FRPS and do not turn on `vhostHTTPPort` or `vhostHTTPSPort`. If FRPS serves `vhostHTTPPort` directly, choose `http` and that port; if FRPS sits behind a reverse proxy or gateway that terminates HTTPS, choose `https` and the public port of that entry.
- Frpdesk enables client-side TLS by default, so FRPS needs no certificate for the control connection. To reject plaintext connections, set `transport.tls.force = true` on the server.
- `allowPorts` is optional but recommended; with it set, a TCP/UDP service fails to register when its remote port falls outside the range or is already taken by another proxy. Without `allowPorts`, any non-reserved port can be used.

Optional dashboard (not required by Frpdesk):

```toml
webServer.addr = "127.0.0.1"
webServer.port = 7500
webServer.user = "admin"
webServer.password = "another-secret"
```

See the [frps reference configuration](https://github.com/fatedier/frp/blob/dev/conf/frps_full_example.toml) for the full option list.

## Quick Start

You need access to a running **FRPS server**, including its address, port, and authentication token if enabled.

1. Open Frpdesk, go to **Servers**, and add your FRPS server details; the field meanings are described in Server Setup above.
2. Add an HTTP, TCP, or UDP service, choose the target server, and enter the local IP and port (for example, `127.0.0.1:3000`).
3. For HTTP, enter a subdomain prefix; for TCP/UDP, enter a remote port. HTTP requires the server's HTTP domain settings and wildcard DNS to be configured first; enable **HTTP domains** in the server configuration and fill in the public entry details to match.
4. Enable the server and service, then use the displayed address to access your local service. Check status and logs in the app if the connection fails.

## License

[Apache-2.0](LICENSE). Third-party notices are in [THIRD_PARTY_NOTICES.md](THIRD_PARTY_NOTICES.md).
