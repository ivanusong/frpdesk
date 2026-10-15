<img src="assets/icon.svg" width="64" height="64" alt="">

# frpdesk

macOS 桌面端 frp 穿透客户端（Wails v3 重构版）。以 Go 库方式嵌入 [frp](https://github.com/fatedier/frp) v0.71.0 客户端，无 sidecar 二进制。

## 功能

- 嵌入式 frpc：HTTP 隧道（subdomain 模式）与 TCP / UDP 端口转发，应用启动自动连接
- 逐服务代理状态：每 2 秒采样，显示注册中/已注册/注册失败（原因直接展示，悬停查看原始错误）；连接状态栏显示失败服务数
- 多服务端配置：每条隧道关联一个 Frps，多台服务器根据隧道启用状态自动连接；配置持久化到 `~/Library/Application Support/frpdesk/config.json`
- 服务管理：HTTP（协议/公网端口/子域名，缺省自动生成 8 字节 hex）与 TCP / UDP（远程端口，同服务器同协议防冲突，TCP/UDP 同数字允许）三类服务
- HTTP 服务默认经内置本地反向代理：自动允许跨域（回显 Origin、允许凭据，预检本地应答不触达后端），Host 改写为本地目标适配 Vite 等开发服务的 allowedHosts，原公网 Host 经 X-Forwarded-Host 透传
- TCP/UDP 服务始终原始字节直连本地目标，不建代理、不解析 HTTP（UDP 报文逐包转发）
- 状态机 stopped/starting/running/stopping/failed + 实时日志（环形缓冲 500 条，frp 日志全量接管，WARN 独立级别）
- 菜单栏常驻：收起窗口后隧道继续运行，菜单支持恢复窗口、查看汇总状态、全部启动/全部停止和退出
- 顶栏显示当前程序版本；自动检查新版，确认后手动更新；服务器通过“配置”Tab 的卡片和弹窗管理

## 许可证与第三方声明

frpdesk 自有代码按 Apache License 2.0 授权（根目录 `LICENSE`，项目自有贡献由 frpdesk contributors 提供）；此许可不覆盖第三方源码。第三方依赖与两个本地 fork 的原许可证、版权声明和修改位置见 [`THIRD_PARTY_NOTICES.md`](THIRD_PARTY_NOTICES.md)，许可证原文位于 [`licenses/`](licenses/)。构建的 Mac `.app` 会在 `Contents/Resources/` 内附上整组文档；Windows EXE 旁附同一文档，CI 同时提供 `frpdesk-<版本>-licenses.zip`，下载 Windows EXE 时请一并下载并保留许可文档包。旧发布流程会把该许可包上传到对应版本目录；manifest 仍只列 3 个安装包。当前 notices 列出全部 4 个 Go 直接依赖，以及已识别的 Wails 内置组件、Lucide/Feather 和 yamux；尚未完成其余间接依赖的全量许可证审计。

## 从 frpcc 升级（名称变更兼容）

项目自 frpcc 更名为 frpdesk，旧数据按最小侵入方式兼容：

- **配置目录**：新配置位于 `os.UserConfigDir()/frpdesk/config.json`（macOS 为 `~/Library/Application Support/frpdesk/config.json`）。首次启动时，仅当新配置不存在且旧 `frpcc/config.json` 存在，才把旧配置原样复制到新路径；旧文件保留不改不删。复制失败会报错退出，不会用空配置冒充成功。
- **界面偏好**：主题与“上次选择服务器”优先读新的 `frpdesk.*` localStorage key，缺失时只读回退旧 `frpcc.*` key（不删除旧值）。bundle ID 变更后网页存储不保证与旧包共享，若读不到就表现为偏好回到默认，不影响功能。
- **旧版共存**：单实例 ID 改为 `cc.frp.frpdesk`（旧版为 `cc.frp.frpcc`），新旧版可能同时运行。升级时请先退出旧版 frpcc，避免两套实例各自管理隧道。
- **更新源与发布路径**：仓库不再内置任何具体发布地址或存储路径；客户端更新源由本地 `.release.env` 的 `UPDATE_MANIFEST_URL` 在构建时注入，发布目标由 `RCLONE_DEST`/`DOWNLOAD_BASE_URL` 提供（三者均只存在本地忽略配置或环境变量中）。

## 多服务端配置

- 配置名称用于区分服务器，地址支持 IP 或主机名，连接端口默认 `7000`，鉴权可留空。同一地址和连接端口不能重复添加。
- 勾选“支持 HTTP 域名”后填写根域名、访问协议和访问端口。访问端口留空表示协议默认端口；这些字段描述已有的 HTTP 入口，不会自动配置 Frps、DNS 或证书。
- 隧道编辑弹窗底部选择目标服务器；HTTP 隧道只能选择支持 HTTP 的服务器。不同服务器可使用相同的远程端口或子域名前缀；同一服务器内远程端口按协议校验冲突（TCP 与 UDP 同数字允许），子域名前缀检查冲突。
- 服务器运行由隧道启用状态决定：第一条隧道启用后自动连接，最后一条隧道关闭、删除或移走后自动释放连接。有已启用隧道时自动重试连接；配置卡片、底部与菜单栏仅展示状态，不提供手动启停入口。
- 配置卡片显示关联隧道总数和已启用数量；底部与菜单栏计数仅显示当前已映射的隧道数（已启用、所属服务器已连接且代理已注册）。
- 配置卡片的“测试”独立验证 Frps 控制连接和鉴权，不注册隧道，也不改变自动连接状态。成功不代表 HTTP 域名、业务端口或本地服务可用。
- 旧全局配置自动迁移为“默认服务器”，现有隧道继续关联它。被隧道引用的配置不能删除；仍有关联 HTTP 隧道时不能关闭 HTTP 支持，包括已停用的隧道。
- 运行中保存配置立即应用，连接地址、端口或鉴权变化只重连对应服务器。更换隧道归属可能改变访问地址并短暂中断；连接失败保留新配置，修正后重试或手动切回。
- HTTP 请求统计随隧道累计，启停或更换服务器不清零，应用重启归零。累计统计显示当前服务器，最近请求显示请求发生时的服务器；统计仅覆盖 HTTP 服务，TCP/UDP 转发不计入。

## 菜单栏与后台运行

- 启动时显示主窗口；点击红色关闭按钮、黄色最小化按钮或按 `⌘W` 收起到菜单栏，保留当前页面和未保存编辑，不停止隧道。
- macOS 主窗口收起时隐藏 Dock 图标，恢复时显示 Dock 并聚焦原窗口。
- 点击菜单栏图标展开原生菜单。再次从 Finder 或 Launchpad 打开应用时恢复现有窗口，不启动第二个客户端。
- 菜单栏采用隧道形图标，异常状态使用带感叹号的版本，菜单显示原因；不自动弹出主窗口或发送系统通知。菜单中的“已连接”数量表示当前已映射的隧道数，不保证本地业务可用。
- 选择“退出 frpdesk（停止所有隧道）”或在主窗口可见时按 `⌘Q`，停止所有隧道并退出，不弹确认框。

## 开发

依赖：Go 1.25+、[wails3 CLI](https://v3.wails.io)（v3.0.0-beta.28）、[Task](https://taskfile.dev)。前端为纯 HTML/原生 JS/CSS，零 npm 零构建链。

frp 使用 `third_party/frp` 中的 v0.71.0 本地副本，修复停止与重连时读取控制连接的竞态，并提供 `IsConnected()` 供应用判断每台服务器的连接状态。升级 frp 时需核对 `client/service.go` 中标注 `frpdesk fork` 的补丁；现有 Wails 本地修复仍独立保留。

```bash
# 构建 / 打包 .app（darwin/arm64，ad-hoc 签名）/ 本地运行
wails3 build
wails3 package
wails3 run

# 重新生成状态栏图标（macOS，需 Xcode CLT）
swift cmd/genicons/svg2png.swift assets/tray-icon.svg assets/tray-icon.png 36
swift cmd/genicons/svg2png.swift assets/tray-icon-failed.svg assets/tray-icon-failed.png 36

# 单元测试
go test ./... -race

# 前端行为验证（需要 Node.js，无需 npm 依赖或浏览器）
node scripts/verify-multiserver-ui.mjs

# 打包 bin/frpdesk.app（ad-hoc 签名，darwin/arm64）
wails3 package

# 开发运行
wails3 run
```

## CI 构建（GitHub Actions）

`.github/workflows/build.yml` 在一台 `macos-14`（ARM64）Runner 上测试并构建三目标产物（darwin arm64/amd64 + windows amd64），复用 `build.sh`，仅上传 Actions 附件，不发布 Release、不上传 MinIO、不修改更新源。

- 触发：push 到 `main` / `develop` / `GitHub`、任意 pull request，或手动 `workflow_dispatch`。
- 下载：进入对应 Actions run 页面，在 Artifacts 区域下载 `frpdesk-<版本>-build`，内含两个 Mac zip、Windows exe 和 `frpdesk-<版本>-licenses.zip`（Windows 许可文档，下载 Windows exe 时一起保存）。Mac app 自带许可文档。
- 说明：这是构建验证产物而非正式发行；macOS `.app` 目前仅 ad-hoc 签名，未做 Apple 开发者签名与公证。

## 发布

本机 macOS 一条脚本完成三目标构建（darwin arm64/amd64 + windows amd64）、updater manifest 生成与校验、rclone 上传 MinIO 并切换 stable 指针。版本目录包含 2 个 Mac zip、1 个 Windows exe、`frpdesk-<版本>-licenses.zip` 和 manifest.json；manifest 仍只描述 3 个可自动更新安装包，stable 指针固定指向最新版本 manifest。

公开仓库不包含任何发布基础设施地址；存储目标、下载根与客户端更新源全部来自本地 `.release.env`（已 gitignore）或环境变量。缺配置时 `release.sh` 在构建或上传前直接报错，不使用任何默认位置，避免误传。`build.sh` 可直接传入 `UPDATE_MANIFEST_URL` 注入客户端更新源；不传入则不注入，应用内更新检查明确禁用。

版本目录发布清单保留原有 3 个 manifest 安装项，另外增加独立的 `frpdesk-<版本>-licenses.zip`（不加到 updater manifest）。skip-build 会要求许可包与两个 Mac app 内的许可文档树全部存在，否则拒绝复用。

前置：本机 rclone 已配置好目标 remote（认证与地址只存在本机配置）；可复制 `release.env.example` 为 `.release.env`（已 gitignore）填写目标、下载根与更新源，环境变量可覆盖文件值。

```bash
# 不传版本号：读取远端 stable.json，第三段 +1 后完整发布
./release.sh

# 0) 预览计划（零副作用，不构建不上传）
./release.sh 0.2.0 --dry-run

# 1) 只出本地产物 bin/releases/<版本>/，不上传不读远端
./release.sh 0.2.0 --prepare-only

# 2) 完整发布：写 VERSION → 构建 → manifest/verify → 上传 → 切 stable
./release.sh 0.2.0 --notes-file NOTES.md

# 已发布版本重跑：跳过构建，复用既有 manifest 核验后补传缺失文件
./release.sh "$(cat VERSION)" --skip-build
```

- 未传版本号时（包括 `--dry-run` / `--prepare-only`），只读远端清单确定版本；例如 `0.2.12 → 0.2.13`、`0.2.234 → 0.2.235`。远端清单缺失或非法时停止，首次发布需显式指定版本。
- 发布会拒绝降级（远端 stable 版本更高时）和改写已存在版本（同版本内容不同直接失败）。
- 签名可选：`wails3 updater genkey -out <key>` 生成密钥对（公钥自动为 `<key>.pub`），在 `.release.env` 配 `UPDATER_SIGNING_KEY` 即对 manifest 签名；不配则无签名，verify 只校验各产物 sha512 摘要。
- 客户端使用 Wails 官方 updater 的 HTTP endpoint provider 读取 stable.json，并按运行平台和架构选择安装包。

检查更新地址由发行构建的 `UPDATE_MANIFEST_URL` 决定（本地配置，不写入仓库）；未注入时应用内更新检查禁用。

## 检查与安装更新

- 启动后检查一次，运行期间每 6 小时检查一次；自动检查只查询清单。
- 右上角主题按钮左侧的图标可手动检查，结果通过提示和“日志”页反馈。日志包含当前版本、清单地址、检查结果和失败原因。
- 发现新版后，当前版本右侧出现“更新”按钮。点击并确认后下载对应安装包、校验摘要，完成准备后重启应用；重启期间隧道短暂断开，启动后自动连接。
- 没有注入版本的 `dev` 开发程序会显示版本并说明更新检查不可用；使用 `./build.sh` 生成的安装包验证。
- 已发布的旧安装包不含本次更新功能，需要先手动安装一次包含更新功能的版本。验证完整流程时，先安装该版本，再通过 `./release.sh <更高版本>` 发布下一版，点击手动检查和“更新”，重启后确认顶栏版本变化。
- 当前版本等于远端 stable 版本时，“已是最新版本”是正常结果。测试无需修改现有业务配置。下载或校验失败会保留当前程序，可查看日志后重试。
- 重启后应用内日志重新开始；若已经退出却未完成替换，可检查系统临时目录中的 `wails-update-<进程号>.log`，这是 Wails 更新助手的替换与恢复日志。
- Windows 启动 5 秒后会清理同目录中当前 EXE 对应的 `.old.<数字>` 更新残留；仍被占用时每 5 秒重试一次，最多尝试 6 次，结果记录到应用日志。退出应用会取消等待和重试。
- 当前发布默认使用 SHA-512 摘要校验；若启用清单签名，还需要在客户端配置匹配的公钥后再发布签名清单。

## 结构

```
main.go            应用入口（application.New + 窗口）
app.go             AppService：暴露给前端的绑定方法 + 事件推送
updater.go         更新检查、下载校验、重启与日志
tray.go            菜单栏图标、原生菜单及窗口收起/恢复
dock_darwin.go     macOS Dock 显隐与应用激活桥接
cmd/genicons       SVG → PNG 生成脚本（macOS 系统 Swift）
assets/tray-icon*.svg  正常与故障状态栏图标源文件
internal/configstore  配置模型 / 持久化 / 校验 / subdomain 生成
internal/tunnel       frp 库嵌入（client.Service 生命周期 + 日志接管 + 状态机）
frontend/dist         静态前端（embed）；bindings 构建时生成到 dist/bindings
build/                darwin 打包资产（Info.plist、icns）与任务
```

行为对齐基线为 Electron 老版 `frpcc/src`（只读参考）；该路径是历史仓库位置，为保持可追溯不随更名调整。

## 已知边界

- HTTP 服务的内置代理适用于明文 HTTP 后端：自动注入 CORS 头并把 Host 改写为本地目标（开发服务友好）；TCP/UDP 服务始终透明转发，非 HTTP 协议按传输层选 TCP 或 UDP 类型。
- “已注册”表示 FRPS 接受了代理，不代表本地业务健康；尚未接入服务端端口清单。
- 顶部 Tab 指示滑块宽度按文字实测定位（tab 宽 - 24px 水平 padding）；窗口 resize 不重算（字号固定，小而美）。
