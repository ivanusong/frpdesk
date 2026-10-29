English | [简体中文](cli-server_zh-CN.md)

# Headless Server (`frpdesk serve`)

The CLI package runs the same Frpdesk web interface on a Linux server, without a
window, menu bar, or desktop session. It is a separate build from the desktop app:
it needs no GTK or WebKitGTK, and it never checks for or installs app updates.

## Install

Download `frpdesk-cli-<version>-linux-amd64.tar.gz` (x64) or
`frpdesk-cli-<version>-linux-arm64.tar.gz` (ARM64) from
[GitHub Releases](https://github.com/ivanusong/frpdesk/releases/latest), then:

```bash
tar -xzf frpdesk-cli-<version>-linux-amd64.tar.gz
sudo install -m 0755 frpdesk /usr/local/bin/frpdesk
frpdesk --version
```

The archive contains exactly one executable named `frpdesk`; it does not need a
desktop environment or any shared libraries beyond a standard Linux userland.

## Start

```bash
frpdesk serve
```

By default it listens on `127.0.0.1:8080` and prints the admin URL:

```
[serve] config: /home/you/.config/frpdesk/config.json
[serve] listening on: 127.0.0.1:8080
[serve] admin UI: http://127.0.0.1:8080
[serve] press Ctrl+C or send SIGTERM to stop
```

Open that URL in a browser. All enabled tunnels are connected automatically at
startup, just like the desktop app.

### Options

| Option | Default | Description |
| --- | --- | --- |
| `--host` | `127.0.0.1` | Listen address. Use `0.0.0.0` only on a trusted network. |
| `--port` | `8080` | Listen port, 1–65535. |
| `--config-dir` | system user config directory | Directory containing `config.json`. |
| `--help` | | Show usage and exit. |
| `--version` | | Print the version and exit. |

On Linux the default config path is `$XDG_CONFIG_HOME/frpdesk/config.json`, or
`~/.config/frpdesk/config.json` when `XDG_CONFIG_HOME` is unset. `--config-dir`
creates the directory if needed and prints the resolved path at startup. The
config directory and `config.json` are kept at `0700` / `0600` because the
configuration can contain plaintext frp tokens.

Exit codes: `0` normal shutdown, `1` runtime failure (bad config, port in use),
`2` invalid command or arguments. A port conflict fails immediately with a
non-zero exit.

## Access and security

The web interface has **no authentication**. Anyone who can reach the port can
manage tunnels, read logs, and change configuration.

- Keep the default `127.0.0.1` binding; the server itself has no browser.
- For remote access, forward the port over SSH and open `http://127.0.0.1:8080`
  on your own machine:

  ```bash
  ssh -N -L 8080:127.0.0.1:8080 you@your-server
  ```

- `--host 0.0.0.0` is appropriate only on a trusted LAN with firewall rules that
  limit who can reach the port. Do not expose it directly to the public internet.
- The UI has no logout and no quit button; stop the process to shut it down.

## Configuration and lifecycle

The CLI uses the same `config.json` format as the desktop app. You can copy an
existing file into the server's config directory, but do **not** run the desktop
app and `serve` against the same directory at the same time: concurrent writers
are not supported.

`Ctrl+C` or `SIGTERM` shuts down gracefully: the HTTP server stops accepting
connections and all tunnels are closed before the process exits.

## systemd example

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

The service runs as an unprivileged user, listens only on loopback, and keeps
its configuration in `/var/lib/frpdesk`. Adjust the user and path to match your
system.

## Upgrade

There is no in-app updater in the CLI build. Replace the binary and restart:

```bash
sudo systemctl stop frpdesk
tar -xzf frpdesk-cli-<version>-linux-amd64.tar.gz
sudo install -m 0755 frpdesk /usr/local/bin/frpdesk
sudo systemctl start frpdesk
```

The desktop packages (`frpdesk-<version>-linux-<arch>.tar.gz`) remain the GUI
build. The CLI archives are the headless server build and are not part of the
desktop update manifest.