[English](cli-server.md) | 简体中文

# 无头服务端（`frpdesk serve`）

CLI 包在 Linux 服务器上提供与桌面版相同的 Frpdesk 网页界面，不创建窗口、菜单栏，
也不需要桌面会话。它与桌面版是独立构建：不依赖 GTK / WebKitGTK，也不检查或安装
应用内更新。

## 安装

从 [GitHub Releases](https://github.com/ivanusong/frpdesk/releases/latest) 下载
`frpdesk-cli-<版本>-linux-amd64.tar.gz`（x64）或
`frpdesk-cli-<版本>-linux-arm64.tar.gz`（ARM64），然后：

```bash
tar -xzf frpdesk-cli-<版本>-linux-amd64.tar.gz
sudo install -m 0755 frpdesk /usr/local/bin/frpdesk
frpdesk --version
```

压缩包内只有一个名为 `frpdesk` 的可执行文件，除标准 Linux 用户态外不需要额外的
桌面环境或共享库。

## 启动

```bash
frpdesk serve
```

默认监听 `127.0.0.1:8080`，并打印管理地址：

```
[serve] config: /home/you/.config/frpdesk/config.json
[serve] listening on: 127.0.0.1:8080
[serve] admin UI: http://127.0.0.1:8080
[serve] press Ctrl+C or send SIGTERM to stop
```

在浏览器中打开该地址即可。与桌面版一样，服务启动后会自动连接全部已启用的隧道。

### 参数

| 参数 | 默认值 | 说明 |
| --- | --- | --- |
| `--host` | `127.0.0.1` | 监听地址。只应在可信网络使用 `0.0.0.0`。 |
| `--port` | `8080` | 监听端口，1–65535。 |
| `--config-dir` | 系统用户配置目录 | 存放 `config.json` 的目录。 |
| `--help` | | 打印帮助后退出。 |
| `--version` | | 打印版本后退出。 |

Linux 默认配置路径为 `$XDG_CONFIG_HOME/frpdesk/config.json`；未设置
`XDG_CONFIG_HOME` 时为 `~/.config/frpdesk/config.json`。`--config-dir` 会在需要时
创建目录，并在启动日志中打印最终路径。配置目录和 `config.json` 会保持 `0700` /
`0600` 权限，因为配置可能包含明文 frp token。

退出码：`0` 正常退出，`1` 运行时失败（配置错误、端口被占用等），`2` 命令或参数
非法。端口冲突会立即以非零退出。

## 访问与安全

网页管理界面**没有鉴权**。任何能访问该端口的人都可以管理隧道、查看日志和修改配置。

- 保持默认的 `127.0.0.1`；服务器本身没有浏览器。
- 需要远程访问时，用 SSH 端口转发，然后在本机打开 `http://127.0.0.1:8080`：

  ```bash
  ssh -N -L 8080:127.0.0.1:8080 you@your-server
  ```

- 只有在有防火墙限制、可信的局域网中才考虑 `--host 0.0.0.0`；不要把端口直接暴露到
  公网。
- 网页没有退出按钮（也没有登录自启动、桌面更新入口），停止进程即停止服务。

## 配置与生命周期

CLI 使用与桌面版相同的 `config.json` 格式。可以把已有配置文件复制到服务器的配置
目录，但**不要**让桌面版与 `serve` 同时使用同一目录：并发写配置不受支持。

`Ctrl+C` 或 `SIGTERM` 会优雅退出：HTTP 服务停止接受新连接，全部隧道关闭后进程才结束。

## systemd 示例

```ini
# /etc/systemd/system/frpdesk.service
[Unit]
Description=Frpdesk web admin
After=network-online.target
Wants=network-online.target

[Service]
Type=simple
User=frpdesk
Group=frpdesk
ExecStart=/usr/local/bin/frpdesk serve --host 127.0.0.1 --port 8080 --config-dir /var/lib/frpdesk
Restart=on-failure
RestartSec=5s

[Install]
WantedBy=multi-user.target
```

```bash
sudo useradd --system --home /var/lib/frpdesk --shell /usr/sbin/nologin frpdesk
sudo install -d -m 0700 -o frpdesk -g frpdesk /var/lib/frpdesk
sudo systemctl daemon-reload
sudo systemctl enable --now frpdesk
journalctl -u frpdesk -f
```

服务以非特权用户运行，只监听回环地址，配置存放在 `/var/lib/frpdesk`。请按实际
系统调整用户名和路径。

## 升级

CLI 构建不包含应用内更新。替换二进制后重启服务：

```bash
sudo systemctl stop frpdesk
tar -xzf frpdesk-cli-<版本>-linux-amd64.tar.gz
sudo install -m 0755 frpdesk /usr/local/bin/frpdesk
sudo systemctl start frpdesk
```

桌面包（`frpdesk-<版本>-linux-<架构>.tar.gz`）仍是图形界面版本；CLI 包是无头服务端
构建，不进入桌面版的更新清单。