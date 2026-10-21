[English](server.md) | [简体中文](server_zh-CN.md)

# Server Deployment with Docker Compose

Everything you need to run a Frpdesk server is in the [`server/`](../server) directory:
frps, plus Traefik which issues and renews a wildcard TLS certificate automatically. You
edit one file, `.env`, and never touch YAML or TOML.

Requirements:

- A Linux server with Docker and the compose plugin.
- A domain you own, with permission to create DNS records.
- An API credential for your DNS provider. The wildcard certificate needs a DNS-01
  challenge, so this cannot be skipped.

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
| `ACME_EMAIL` | `you@example.com` | Let's Encrypt renewal notices. |
| `DNS_PROVIDER` | `cloudflare` | Or `alidns`, `dnspod`, ... see [lego's provider list](https://go-acme.github.io/lego/dns/). |
| The credential for that provider | `CF_DNS_API_TOKEN=...` | Fill in only the pair matching `DNS_PROVIDER`. |

Create a credential scoped to this zone only, never an account-wide key.

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
docker compose logs -f traefik    # watch the certificate being issued, then Ctrl+C
```

The first start takes up to a minute while the DNS-01 challenge completes. When the logs
stop showing errors, you are done. The certificate is stored in `letsencrypt/acme.json` and
renews on its own.

Open the firewall for `443`, `80`, `FRPS_BIND_PORT`, and `FRPS_REMOTE_PORTS`.

## 5. Fill in Frpdesk

| Field | Value |
| --- | --- |
| Server address | `example.com` or the server IP |
| Server port | `7000` |
| Auth token | your `FRPS_TOKEN` |
| HTTP domains | enabled |
| Base domain | `example.com` |
| Access scheme | `https` |
| Access port | empty |

A service named `blog` becomes `https://blog.example.com`.

## How it works

Only the public HTTP entry goes through Traefik. Frpdesk connects to frps directly on
`FRPS_BIND_PORT`, because frps multiplexes the control connection and the HTTP entry on that
one port.

```
browser  --https-->  Traefik:443  --http-->  frps  --tunnel-->  Frpdesk
Frpdesk  --tls------------------------->  frps:7000  (not proxied)
```

frps itself cannot terminate TLS for HTTP tunnels: `vhostHTTPSPort` is only an SNI
passthrough for `https`-type proxies, and Frpdesk only creates `http` proxies. That is why a
front proxy is required for `https://`.

The compose stack already sets two options that matter for real traffic, so you do not have
to think about them:

- `transport.tls.force = true` rejects control connections without TLS. Frpdesk always uses
  TLS, and this does not affect the HTTP entry behind Traefik.
- `vhostHTTPTimeout = -1` removes the 60 second vhost gateway limit, so SSE, streaming
  responses and large uploads are not cut off.

## Troubleshooting

- **`docker compose up` refuses to start, complaining about a variable.** `.env` is missing
  a required value; the message names it.
- **Certificate never issued.** `docker compose logs traefik` shows the ACME error. Nearly
  always a wrong or missing DNS credential, or DNS slower than `ACME_DELAY_BEFORE_CHECK`.
  Let's Encrypt rate-limits failures, so fix the credential before retrying in a loop.
- **`502 Bad Gateway`.** frps is not up yet, or `FRPS_BIND_PORT` was changed after start.
  `docker compose logs frps`.
- **frps' own 404 page.** The `Host` header was dropped; check the `passhostheader` label is
  still `true` in `docker-compose.yml`.
- **HTTPS works but Frpdesk stays "connecting".** The control connection does not go through
  Traefik. Check that `FRPS_BIND_PORT` is open in the firewall and reachable from your
  machine, and that `FRPS_TOKEN` matches exactly.
- **A TCP/UDP service fails to start.** Its remote port must fall inside `FRPS_REMOTE_PORTS`
  and not be taken by another service.
- **Wildcard certificate does not cover a subdomain.** `*.example.com` covers one level only;
  `a.b.example.com` would need its own certificate.

## Updating

```bash
docker compose pull
docker compose up -d
```

frps must be at least as new as the frp client built into Frpdesk, so do not pin an image
older than the client version noted in the README.
