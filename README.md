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

## Quick Start

You need access to a running **FRPS server**, including its address, port, and authentication token if enabled.

1. Open Frpdesk, go to **Servers**, and add your FRPS server details.
2. Add an HTTP, TCP, or UDP service, choose the target server, and enter the local IP and port (for example, `127.0.0.1:3000`).
3. For HTTP, enter a subdomain prefix; for TCP/UDP, enter a remote port. HTTP requires the server's HTTP domain settings and wildcard DNS to be configured first; enable **HTTP domains** in the server configuration and fill in the public entry details to match.
4. Enable the server and service, then use the displayed address to access your local service. Check status and logs in the app if the connection fails.

## License

[Apache-2.0](LICENSE). Third-party notices are in [THIRD_PARTY_NOTICES.md](THIRD_PARTY_NOTICES.md).
