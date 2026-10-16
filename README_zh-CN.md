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

- 顶栏语言按钮为**中文 ⇄ English** 两态显式切换：按钮显示当前语言（「中」或「EN」），tooltip/aria 说明当前语言与点击后将切换到的语言。首次启动或未保存偏好时自动跟随系统语言（中文系统用简体中文，其他回退英文），该模式由**原生端统一解析**（优先环境变量，再取平台界面语言），前端采用后端返回的解析结果，保证 WebView 界面与托盘菜单语言始终一致。点击按钮总是保存显式的 `zh-CN` 或 `en` 偏好，不会回到跟随系统模式。
- 选择会持久化：前端写入本地存储，同时保存到配置文件（`config.json` 的 `language` 字段），因此重启后**托盘菜单与后端消息**也保持同一语言。若保存失败，界面会回滚到最近一次确认一致的选择并给出可见错误，不会静默与托盘不一致。
- 切换语言即时生效：页面、卡片、表单、统计、确认框、toast 与 tooltip/aria 文案即时重绘；不会丢失未保存的表单输入，也不会重启隧道。
- 用户数据（服务名、服务器地址、域名等）与 FRP 原始错误、技术日志保持原文，便于排查。应用生成的运行日志——无论是「日志」面板还是终端诊断日志（`slog`/stderr、更新与清理消息）——固定英文，不随界面语言翻译。

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

推送 **annotated** 的 `vX.Y.Z` tag，版本号必须同时与 `VERSION` 和 [CHANGELOG.md](CHANGELOG.md) 最新的 `## X.Y.Z` 小节一致。
发布日志的唯一来源是 tag 描述：[Release workflow](.github/workflows/release.yml) 用它（含 subject 与 body 时取 body）生成 GitHub Release 描述与 Wails updater `stable.json` 的 notes（即应用内更新提示），随后运行测试、构建 macOS 与 Windows 产物并整体发布为 GitHub Release；轻量 tag 或描述为空会在发布前失败。

[CHANGELOG.md](CHANGELOG.md) 仍需随版本维护并参与版本一致性校验，但不再作为发布日志来源。

```bash
git tag -a vX.Y.Z -F notes.md   # notes.md 即 GitHub Release / 应用内更新提示
git push origin vX.Y.Z
```

发行版在 `https://github.com/ivanusong/frpdesk/releases/latest/download/stable.json` 检查更新。

## 许可证

[Apache-2.0](LICENSE)。第三方声明见 [THIRD_PARTY_NOTICES.md](THIRD_PARTY_NOTICES.md)。