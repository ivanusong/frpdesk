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

推送 **annotated** 的 `vX.Y.Z` tag，版本号与 `VERSION` 一致即可。tag 描述就是发布说明，无需维护 changelog 文件：[Release workflow](.github/workflows/release.yml) 用它（含 subject 与 body 时取 body）生成 GitHub Release 描述与 Wails updater `stable.json` 的 notes（即应用内更新提示），随后运行测试、构建 macOS 与 Windows 产物并整体发布为 GitHub Release；轻量 tag 或描述为空会在发布前失败。

```bash
git tag -a v0.3.0 -m "frpdesk v0.3.0

- 本次改动要点
- 几行即可"
git push origin v0.3.0
```

发行版在 `https://github.com/ivanusong/frpdesk/releases/latest/download/stable.json` 检查更新。

## 许可证

[Apache-2.0](LICENSE)。第三方声明见 [THIRD_PARTY_NOTICES.md](THIRD_PARTY_NOTICES.md)。