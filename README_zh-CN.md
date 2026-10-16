<img src="build/appicon.png" width="72" height="72" alt="">

# Frpdesk

frpdesk 是 [frp](https://github.com/fatedier/frp) 的原生桌面客户端，基于 [Wails v3](https://v3.wails.io) 构建。frp 客户端直接运行在应用内，没有独立 sidecar 进程。

## 功能

- HTTP、TCP、UDP 隧道
- 多 Frps 服务器配置
- 服务状态、日志与 HTTP 请求统计
- macOS 菜单栏后台常驻
- 内置 HTTP 代理，支持 CORS 与 Host 改写

## 开发

要求：Go 1.25+、Wails v3 CLI，以及 Node.js（用于前端校验）。

```bash
wails3 dev          # 本地运行
./build.sh          # 构建 macOS arm64/amd64 与 Windows amd64
go test ./...       # 运行测试
```

前端校验（零 npm 依赖，CI 同样执行）：

```bash
node scripts/verify-multiserver-ui.mjs   # 多 Frps UI 行为验证
```

## 发布

推送 `vX.Y.Z` tag。版本号必须同时与 `VERSION` 和 [CHANGELOG.md](CHANGELOG.md) 最新的 `## X.Y.Z` 小节一致；
[Release workflow](.github/workflows/release.yml) 随后运行测试、构建 macOS 与 Windows 产物、根据 CHANGELOG 生成 Wails updater 的 `stable.json`，并整体发布为 GitHub Release。

发行版在 `https://github.com/ivanusong/frpdesk/releases/latest/download/stable.json` 检查更新。

## 许可证

[Apache-2.0](LICENSE)。第三方声明见 [THIRD_PARTY_NOTICES.md](THIRD_PARTY_NOTICES.md)。