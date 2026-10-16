<img src="build/appicon.png" width="72" height="72" alt="">

# Frpdesk

[English](README.md) | 简体中文

frpdesk 是 [frp](https://github.com/fatedier/frp) 的原生桌面客户端，基于 [Wails v3](https://v3.wails.io) 构建。frp 客户端直接运行在应用内，没有独立 sidecar 进程。

## 功能

- HTTP、TCP、UDP 隧道
- 多 Frps 服务器配置
- 服务状态、日志与 HTTP 请求统计
- macOS 菜单栏后台常驻
- 内置 HTTP 代理，支持 CORS 与 Host 改写
- 简体中文 / 英文界面，可跟随系统

## 界面语言

- 顶栏语言按钮三态循环：**跟随系统 → 简体中文 → English**；默认为「跟随系统」，中文系统使用简体中文，其他语言回退英文。
- 选择会持久化：前端写入本地存储，同时保存到配置文件（`config.json` 的 `language` 字段），因此重启后**托盘菜单与后端消息**也保持同一语言。
- 切换语言即时生效：页面、卡片、表单、统计、确认框、toast 与 tooltip/aria 文案即时重绘；不会丢失未保存的表单输入，也不会重启隧道。
- 用户数据（服务名、服务器地址、域名等）与技术日志、FRP 原始错误保持原文，便于排查。

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
node scripts/verify-i18n.mjs             # 双语词典完整性与 key 对齐
```

## 贡献翻译

界面文案集中在两处，均为「语义 key → 中英双语文案」的扁平词典：

| 位置 | 用途 |
| --- | --- |
| `frontend/dist/i18n.js` 的 `DICT` | 前端所有可见文案（静态 `data-i18n*` 属性 + 动态渲染），`{0}..{n}` 为参数占位符 |
| `internal/i18n/keys.go` 的 `dict` | 后端直接可见文案（托盘菜单/状态、表单校验错误、连通性探测、更新提示等） |

新增或修改翻译的步骤：

1. 只改文案值（`zh` / `en`），不要改动 key 名；两侧都必须非空，且 `{n}` 占位符一一对应。
2. 前端静态元素用 `data-i18n`（文本）、`data-i18n-title`、`data-i18n-aria`、`data-i18n-placeholder` 标注；动态字符串用 `t("key", ...)`。
3. 后端用户可见文案用 `i18n.T("key", ...)`；技术日志（`slog` / 环形日志）保持原文，不进入词典。
4. 运行 `node scripts/verify-i18n.mjs` 与 `go test ./internal/i18n/` 校验词典完整性；再运行 `node scripts/verify-multiserver-ui.mjs` 与 `go test ./...` 回归行为。
5. 原始 FRP 错误（如代理注册失败详情）应继续作为原始文本保留（`rawErr` / 日志），不要翻译。

## 发布

推送 `vX.Y.Z` tag。版本号必须同时与 `VERSION` 和 [CHANGELOG.md](CHANGELOG.md) 最新的 `## X.Y.Z` 小节一致；
[Release workflow](.github/workflows/release.yml) 随后运行测试、构建 macOS 与 Windows 产物、根据 CHANGELOG 生成 Wails updater 的 `stable.json`，并整体发布为 GitHub Release。

发行版在 `https://github.com/ivanusong/frpdesk/releases/latest/download/stable.json` 检查更新。

## 许可证

[Apache-2.0](LICENSE)。第三方声明见 [THIRD_PARTY_NOTICES.md](THIRD_PARTY_NOTICES.md)。