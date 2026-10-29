[English](server.md) | [简体中文](server_zh-CN.md)

# Server Deployment with Docker Compose

Everything you need to run a Frpdesk server is in the [`server/`](../server) directory: a
single-container Compose stack that runs frps. You edit one file, `.env`, and never touch
YAML or TOML.

Requirements:

- A Linux server with Docker and the compose plugin.
- A domain you own, with wildcard DNS if you use HTTP domain tunnels.

## 1. Copy the files to the server

```bash
scp -r server/ you@your-server:~/frpdesk-server
ssh you@your-server
cd frpdesk-server
```

## 2. Fill in `.env`

```bash
cp .env.example .env
openssl rand -hex 32      # use this as FRPS_TOKEN
nano .env
```

The values you must set:

| Variable | Example | Notes |
| --- | --- | --- |
| `FRPS_DOMAIN` | `example.com` | Your base domain, no `*.` prefix. Also the **Base domain** in Frpdesk. |
| `FRPS_TOKEN` | `9f1c...` | Any long random string. Also the **Auth token** in Frpdesk. |
| `FRPS_BIND_PORT` | `7000` | Also the **Server port** in Frpdesk. |

`FRPS_REMOTE_PORTS` defaults to `20000-20099` and is the range TCP/UDP services may use.
Widen it if you need more ports; both TCP and UDP are published.

## 3. DNS records

At your DNS provider, create two `A` records pointing at the server's public IP:

- `example.com`
- `*.example.com`

The wildcard is what makes `blog.example.com` resolve once a tunnel is enabled. The apex
record is needed separately, because a wildcard does not match the bare domain.

## 4. Start it

```bash
docker compose up -d
docker compose logs -f frps    # confirm it is listening, then Ctrl+C
```

Open the firewall for `FRPS_BIND_PORT` and `FRPS_REMOTE_PORTS`. If you run your own reverse
proxy on the same host, open the ports it listens on (usually `80` and `443`) as well.

## 5. Fill in Frpdesk

| Field | Value |
| --- | --- |
| Server address | `example.com` or the server IP |
| Server port | `7000` |
| Auth token | your `FRPS_TOKEN` |
| HTTP domains | enabled |
| Base domain | `example.com` |
| Access scheme | `http` |
| Access port | `7000` (your `FRPS_BIND_PORT`) |

A service named `blog` becomes `http://blog.example.com:7000` with the default port.

If you terminate TLS with your own reverse proxy, switch **Access scheme** to `https` and
set **Access port** to the proxy's public port (leave it empty for standard port `443`).

## HTTPS

frps only provides a plaintext entry for HTTP tunnels and cannot terminate TLS itself:
`vhostHTTPSPort` is only an SNI passthrough for `https`-type proxies, and Frpdesk only creates
`http` proxies. TLS termination is therefore up to a reverse proxy you provide; this project
does not include one.

Your reverse proxy must:

1. Forward to frps on `FRPS_BIND_PORT` — the control connection and the HTTP entry share that
   one port.
2. Preserve the original `Host` header; frps routes tunnels by it.
3. Set no read or write timeout cap, otherwise SSE, streaming responses and large uploads are
   cut off.
4. Keep Frpdesk's control connection direct — do not proxy it. frps enforces
   `transport.tls.force = true`, and Frpdesk always establishes the control connection over
   TLS.

Keep `FRPS_BIND_PORT` reachable from remote Frpdesk clients, even if the reverse proxy runs
on the same host.

## How it works

```
browser  --http-->  frps:7000  --tunnel-->  Frpdesk
Frpdesk  --tls-->   frps:7000  (same port, multiplexed)
```

The compose stack already sets two options that matter for real traffic, so you do not have
to think about them:

- `transport.tls.force = true` rejects control connections without TLS. Frpdesk always uses
  TLS, and this does not affect the HTTP entry on the same port.
- `vhostHTTPTimeout = -1` removes the 60 second vhost gateway limit, so SSE, streaming
  responses and large uploads are not cut off.

## Troubleshooting

- **`docker compose up` refuses to start, complaining about a variable.** `.env` is missing
  a required value; the message names it.
- **frps' own 404 page.** The subdomain is unknown or its tunnel is not enabled. If you use
  your own reverse proxy, it may also be dropping the `Host` header.
- **`502 Bad Gateway` from your own reverse proxy.** frps is not up yet, or `FRPS_BIND_PORT`
  was changed after start. `docker compose logs frps`.
- **Frpdesk stays "connecting".** The control connection does not go through a reverse proxy.
  Check that `FRPS_BIND_PORT` is open in the firewall and reachable from your machine, and that
  `FRPS_TOKEN` matches exactly.
- **A TCP/UDP service fails to start.** Its remote port must fall inside `FRPS_REMOTE_PORTS`
  and not be taken by another service.

## Updating

```bash
docker compose pull
docker compose up -d
```

If upgrading from the previous two-container stack, configure your own TLS termination first,
then run `docker compose up -d --remove-orphans` to remove the old proxy container. Existing
certificate files and unused values in `.env` are left untouched; this stack no longer uses them.

frps must be at least as new as the frp client built into Frpdesk, so do not pin an image
older than the client version noted in the README.
