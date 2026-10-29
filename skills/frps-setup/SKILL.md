---
name: frps-setup
description: 在用户可操作的 Linux 服务器上，用本仓库 server/ 的 frps 单容器 Docker Compose 部署 Frpdesk 服务端并输出客户端配置。用户要求部署 Frpdesk/frps 服务端时使用；仅调客户端配置或部署其他服务时不适用。
---

# 部署 Frpdesk 服务端

目标：在用户可操作的目标 Linux 服务器上，用仓库 `server/` 的 Compose 模板（单个 frps 容器）完成部署，最后给出 Frpdesk 客户端配置。TLS 终止不在本 skill 范围内，由用户自备反向代理负责。

## 资源定位

以下路径相对本 skill 文件解析，与当前工作目录无关：

- `../../server/docker-compose.yml`、`../../server/.env.example`、`../../server/frps.toml`：部署模板
- `../../docs/server_zh-CN.md`：字段含义与排查细节，拿不准时再读

若这些同级仓库文件不存在，说明需要先取得完整 Frpdesk 仓库，不要凭记忆重建配置文件。只在目标服务器（本机或用户已能直接执行命令的机器）上操作，不扩展为跨机器 SSH 方案。

## 开工前探测（只读）

先在服务器上查明并简报，不改动任何状态：

- 系统版本；`docker` 与 `docker compose` 插件是否可用、daemon 是否在运行。非 Linux 或 Docker/Compose 不可用时，先说明缺失的部署前置条件，不继续启动，不擅自安装软件或改动系统。
- 端口占用：bind 端口（默认 7000）与远程端口段（默认 20000-20099，TCP/UDP），用 `ss -ltnp`/`ss -lunp` 查。80/443 不是本栈必需端口，只有用户打算自备反向代理时才相关。
- 是否已有 frps/frpdesk 容器、已有部署目录（找含 `docker-compose.yml` + `.env` 的 Frpdesk 服务端目录）并复用。

## 只问缺失的信息

只问域名和用户没给的其它必填值，其余全部用 `.env.example` 默认值。`FRPS_TOKEN` 优先用工具支持的私密输入方式处理；不支持就让用户在服务器上自行编辑 `.env` 填入，不要求贴到聊天。全程不打印 `.env` 内容、渲染后的 Compose、`FRPS_TOKEN`。

## 文件操作原则

- 模板文件（`docker-compose.yml`、`.env.example`、`frps.toml`）逐个检查，仅复制目标目录中缺失的文件；目录已存在也应补齐缺失文件。
- `.env` 不存在才创建：复制 `.env.example`，`FRPS_TOKEN` 用 `openssl rand -hex 32` 生成后写入，不回显。
- 已存在的 `.env` 保留有效原值（尤其 `FRPS_TOKEN`），只补缺失或尚未填写的必填项；口令为空时生成，已有口令不重新生成。
- 确需修改现有文件：先备份（如 `docker-compose.yml.bak-20250101`），且只新增缺失键或本次明确授权的键。

## 端口与网络冲突

先区分占用者：同一部署的现有容器监听预期端口属于正常状态，可以复用；bind 端口或远程端口段被 Nginx 等其他服务占用时，报告占用进程，请用户选择处理方式（换端口、让出端口等）。不直接停别人的服务，不改动现有反向代理网关。服务器防火墙和 DNS 记录一律不自动修改，启动前说明所需配置，结束时报告仍待完成的项：

- 需放通的端口：bind 端口、远程端口段；用户打算自备反向代理时，再加上反代监听的端口（通常 80/443）。
- 需创建的 DNS `A` 记录：`<域名>` 和 `*.<域名>`，指向服务器公网 IP。

## 执行与启动

授权已足够且探测无冲突时，直接执行，不要每步确认：

```bash
docker compose config --quiet   # 校验配置
docker compose up -d
```

有界查看 frps 日志（如 `docker compose logs frps`，最多一两分钟），确认已开始监听且无持续报错；不做循环重启。起不来就去读日志找原因，修复后再试一次。

## 验收

- `docker compose ps` 服务 Up；有界查看日志无持续报错。
- DNS：`dig +short blog.<域名>`（任选子域）解析到服务器 IP。
- frps 端口可达：如 `nc -zv <服务器IP> <bind端口>`。
- 可选：`curl -H 'Host: blog.<域名>' http://<服务器IP>:<bind端口>/` 探 HTTP 入口；未建隧道时返回 frps 的 404 属正常。

判定约束：没有真实 Frpdesk 客户端连上并跑通一条隧道前，不得声称端到端通过；收到 HTTP 404 不能断定 Host 丢失——未知子域名或尚未启用隧道时 frps 本就返回 404。TLS 由用户自备反向代理负责，用户未提供反代时不得声称 https 可用。

## 输出客户端配置

按实际值给出，不输出鉴权口令的值，只给读取路径与键名：

| Frpdesk 字段 | 值 |
| --- | --- |
| 服务器地址 | 实际服务器地址（域名或 IP） |
| 服务器端口 | 实际 bind 端口（默认 7000） |
| 鉴权 Token | `<部署目录>/.env` 的 `FRPS_TOKEN`，自行查看 |
| 支持 HTTP 域名 | 启用 |
| 根域名 | 实际 `FRPS_DOMAIN` |
| 访问协议 | `http`（用户自备反代终结 TLS 时填 `https`） |
| 访问端口 | 实际 bind 端口（默认 `7000`）；自备反代时填其公网端口，标准 HTTPS 端口 `443` 可留空 |

另附远程端口范围（默认 20000-20099）、待放通端口和待创建 DNS 记录清单。
