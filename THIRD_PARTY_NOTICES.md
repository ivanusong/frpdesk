# 第三方软件许可与声明（THIRD_PARTY_NOTICES）

本文件随 frpdesk 安装包与许可文档包一起分发，记录当前实际使用的第三方组件、固定版本、
许可证与本地修改声明。frpdesk **自有代码由 frpdesk contributors 按根目录 `LICENSE`（Apache License 2.0）授权**；
**根许可不覆盖下列第三方源码**，各第三方组件保留其原始许可证，全文集中存放于
`licenses/`（均为原始文本，未改写）。

> **范围说明**：本文件覆盖 Go 模块的全部直接依赖（`go.mod` require）、Wails 本地副本内
> 内置的已知许可组件、前端内联图标，以及已单独发现的间接依赖 `hashicorp/yamux`。
> 这**不是一次完整的传递依赖审计**：Go 模块的其余间接依赖未逐一致谢；完整模块清单可用
> `go list -m all` 获取，并按第 5 节方式在本地模块缓存核对各版本源码中的 LICENSE。
> 如与对应版本源码中的许可文本不一致，以源码中的原始文本为准。

## 1. Go 直接依赖（go.mod require，4 个）

| 组件 | 实际版本 | 许可证 | 版权/来源 | 许可全文 |
| --- | --- | --- | --- | --- |
| github.com/fatedier/frp | v0.71.0（本地 fork，见第 2.1 节） | Apache-2.0 | 上游 https://github.com/fatedier/frp | `licenses/Apache-2.0.txt` |
| github.com/fatedier/golib | v0.8.2 | Apache-2.0 | 上游 https://github.com/fatedier/golib | `licenses/Apache-2.0-golib.txt` |
| github.com/skip2/go-qrcode | v0.0.0-20200617195104-da1b6568686e | MIT | Copyright (c) 2014 Tom Harwood | `licenses/MIT-go-qrcode.txt` |
| github.com/wailsapp/wails/v3 | v3.0.0-beta.28（本地 fork，见第 2.2 节） | MIT | Copyright (c) 2018-Present Lea Anthony | `licenses/MIT-wails.txt` |

固定版本源码获取（直接查看对应版本的 LICENSE；frp 与 wails 用 `replace` 指向本仓库
`third_party/`，见第 2 节）：

```bash
go mod download github.com/fatedier/golib@v0.8.2
ls "$(go env GOMODCACHE)/github.com/fatedier/golib@v0.8.2/LICENSE"

go mod download github.com/skip2/go-qrcode@v0.0.0-20200617195104-da1b6568686e
ls "$(go env GOMODCACHE)/github.com/skip2/go-qrcode@v0.0.0-20200617195104-da1b6568686e/LICENSE"
```

## 2. 本地 fork 与修改声明

两个本地 fork 仅用于修复/规避上游问题，均保留原许可证与版权声明。

### 2.1 github.com/fatedier/frp（Apache-2.0）

- 源码位置：`third_party/frp/`（`go.mod`：`replace github.com/fatedier/frp => ./third_party/frp`），
  基于上游固定版本 v0.71.0。
- 修改位置与内容：`third_party/frp/client/service.go`——
  修复停止/重连时对 `svr.ctl` 的无锁读取竞态：`stop()` 在 `ctlMu` 下置 nil，读侧新增
  `getControl()` 快照（`keepControllerWorking`、登录结果判断、`UpdateConfigSource` 等读取点
  改走快照），并追加只读 `IsConnected()` 供宿主判断连接状态。每处均标注 `frpdesk fork` 注释。
  未改变上游业务逻辑与 API 语义。
- 修改后的源码以本仓库 `third_party/frp/` 为准；许可证不变（Apache-2.0）。

### 2.2 github.com/wailsapp/wails/v3（MIT）

- 源码位置：`third_party/wails/`（`go.mod`：`replace github.com/wailsapp/wails/v3 => ./third_party/wails`），
  基于上游固定版本 v3.0.0-beta.28。
- 修改位置与内容：`third_party/wails/pkg/application/webview_window_darwin.m` 的
  `- (void) startDrag:`——原实现复用可能过期的 `self.leftMouseEvent`，异步 `wails:drag`
  到达时会让 macOS 标题栏拖拽瞬移；修改为始终以当前光标位置合成 mousedown 事件。
  该文件内现有的 `frpdesk local patch` 显著注释标明此本地改动；
  另 `third_party/wails/README.md` 记录补丁原因与升级注意事项。
- 修改后的源码以本仓库 `third_party/wails/` 为准；许可证不变（MIT）。

## 3. Wails 本地副本内置/内联组件

| 组件 | 位置与版权 | 许可证 | 许可全文 |
| --- | --- | --- | --- |
| go-common-file-dialog | `third_party/wails/internal/go-common-file-dialog/`，Copyright (c) 2019 Harry Phillips | MIT | `licenses/MIT-go-common-file-dialog.txt` |
| webviewloader（OpenWebView2Loader 的 Go 移植；见该目录 README.md） | `third_party/wails/internal/webview2/webviewloader/`，Copyright (c) 2020 John Chadwick，Copyright (c) 2022 Wails Project Developers | ISC | `licenses/ISC-webviewloader.txt` |

## 4. 前端内联图标：Lucide 与 Feather

`frontend/dist/main.js`、`frontend/dist/style.css` 内联了 Lucide 图标（纯静态 HTML/JS，
无 npm 依赖）。Lucide 采用 **ISC** 许可；其中部分图标派生自 Feather（**MIT**，
Copyright (c) 2013-present Cole Bemis），本项目用到的 `trash`、`alert-circle`、`info`、
`monitor`、`moon`、`download`、`loader` 等在该派生清单内（完整清单见许可文件）。

- `licenses/ISC-lucide.txt`：ISC 许可与版权声明
- `licenses/MIT-feather.txt`：Feather 派生图标清单 + Feather MIT 许可与版权声明

两份文本取自官方许可页的 Markdown 原文 https://lucide.dev/license.md（2026-10-15 抓取）。

## 5. 已发现的间接依赖

| 组件 | 实际版本 | 许可证 | 说明 |
| --- | --- | --- | --- |
| github.com/hashicorp/yamux | v0.1.1 | MPL-2.0 | frp 使用的流复用库（`go.sum`/模块图间接依赖）。MPL-2.0 为文件级 copyleft，不改变 frpdesk 自有代码的 Apache-2.0 许可 |

许可全文：`licenses/MPL-2.0-yamux.txt`（复制自本地模块缓存对应版本）。固定版本源码获取：

```bash
go mod download github.com/hashicorp/yamux@v0.1.1
ls "$(go env GOMODCACHE)/github.com/hashicorp/yamux@v0.1.1/LICENSE"
```

第 5 节仅记录上述已发现的间接依赖；其余传递依赖未逐一审计（见开头范围说明）。

## 6. 许可全文索引

| 文件 | 适用组件 |
| --- | --- |
| `licenses/Apache-2.0.txt` | fatedier/frp（以及 frpdesk 自有代码，与根 LICENSE 同文） |
| `licenses/Apache-2.0-golib.txt` | fatedier/golib v0.8.2 |
| `licenses/MIT-go-qrcode.txt` | skip2/go-qrcode |
| `licenses/MIT-wails.txt` | wailsapp/wails/v3 本地 fork |
| `licenses/MIT-go-common-file-dialog.txt` | Wails 副本内置 go-common-file-dialog |
| `licenses/ISC-webviewloader.txt` | Wails 副本内置 webviewloader |
| `licenses/ISC-lucide.txt` | 前端内联 Lucide 图标 |
| `licenses/MIT-feather.txt` | 前端内联图标中的 Feather 派生部分 |
| `licenses/MPL-2.0-yamux.txt` | hashicorp/yamux（间接依赖） |