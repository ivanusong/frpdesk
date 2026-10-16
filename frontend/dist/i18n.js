// frpdesk 前端集中双语词典与语言解析（零依赖原生模块）。
//
// 约定：
//   - key 为稳定语义名（区域.语义），{0}..{n} 为参数占位符；
//   - 词典只收录「界面直接可见」的文案；FRP 原始错误、技术日志与历史 changelog
//     保持原文（原始错误仍由后端 rawErr / 环形日志承载，便于排查）；
//   - 用户名字、地址、服务名等技术数据不翻译；
//   - 运行日志（日志面板/更新/清理等后端环形日志）与诊断通道（FrontendLog）
//     固定英文，不随界面语言翻译；
//   - 语言：首启/无偏好由 system 自动解析系统语言（后端权威）；顶栏语言图标
//     打开三选一弹层（自动 system / 简体中文 zh-CN / 英语 en），选择持久化显式
//     偏好，选「自动」可随时回到 system。持久化在 localStorage（旧 key
//     frpcc.language 只读回退），并与后端 config.json 的 language 字段保持
//     一致（后端负责托盘/后端消息语言）。
//
// 新增/修改文案后请运行 `node scripts/verify-i18n.mjs` 校验词典完整性与 key 对齐。

export const PREF_SYSTEM = "system";
export const PREF_ZH = "zh-CN";
export const PREF_EN = "en";
export const LANG_PREFS = [PREF_SYSTEM, PREF_ZH, PREF_EN];

const STORAGE_KEY = "frpdesk.language";
const LEGACY_STORAGE_KEY = "frpcc.language";

// 集中词典：key → { zh, en }。zh 为权威文案，en 为英文回退，两者都必须非空。
export const DICT = {
  // ---- 顶栏：更新 ----
  "app.versionTip": { zh: "当前版本", en: "Current version" },
  "update.check": { zh: "检查更新", en: "Check for updates" },
  "update.checking": { zh: "正在检查更新…", en: "Checking for updates…" },
  "update.busy": { zh: "更新进行中", en: "Update in progress" },
  "update.chip": { zh: "更新", en: "Update" },
  "update.chipReady": { zh: "重启更新", en: "Restart to update" },
  "update.chipRetry": { zh: "重试更新", en: "Retry update" },
  "update.chipDownloading": { zh: "下载中", en: "Downloading" },
  "update.chipVerifying": { zh: "校验中", en: "Verifying" },
  "update.chipInstalling": { zh: "安装中", en: "Installing" },
  "update.chipDownloadingPct": { zh: "下载中 {0}%", en: "Downloading {0}%" },
  "update.tipBusy": { zh: "正在更新，请稍候", en: "Update in progress, please wait" },
  "update.tipTo": { zh: "更新到 v{0}", en: "Update to v{0}" },
  "update.tipRetryTo": { zh: "重试更新到 v{0}", en: "Retry update to v{0}" },
  "update.found": { zh: "发现新版本 v{0}", en: "New version v{0} available" },
  "update.confirmAvailableMsg": {
    zh: "发现新版本 v{0}（当前 v{1}）。更新将重启应用，隧道会短暂断开并在重启后自动重连，是否继续？",
    en: "New version v{0} is available (current v{1}). Updating restarts the app; tunnels will briefly disconnect and reconnect automatically. Continue?",
  },
  "update.confirmReadyTitle": { zh: "v{0} 已就绪", en: "v{0} is ready" },
  "update.confirmReadyMsg": {
    zh: "v{0} 已下载并校验，重启应用完成更新？隧道会短暂断开并在重启后自动重连。",
    en: "v{0} has been downloaded and verified. Restart the app to finish updating? Tunnels will briefly disconnect and reconnect automatically.",
  },
  "update.confirmErrorTitle": { zh: "重试更新到 v{0}", en: "Retry update to v{0}" },
  "update.confirmErrorMsg": {
    zh: "上次更新未完成，重试更新到 v{0}？更新将重启应用，隧道会短暂断开并在重启后自动重连。",
    en: "The last update did not finish. Retry updating to v{0}? Updating restarts the app; tunnels will briefly disconnect and reconnect automatically.",
  },
  "update.metaCurrent": { zh: "当前 v{0} · ", en: "Current v{0} · " },
  "update.metaRestart": {
    zh: "更新将重启应用，隧道会短暂断开并在重启后自动重连",
    en: "Updating restarts the app; tunnels will briefly disconnect and reconnect automatically",
  },
  "update.confirmNow": { zh: "立即更新", en: "Update now" },
  "update.readyToast": { zh: "更新已就绪，可重启应用完成更新", en: "Update ready — restart the app to finish" },
  "update.upToDateToast": { zh: "当前已是最新版本 v{0}", en: "Already on the latest version v{0}" },
  "update.checkFail": { zh: "检查更新失败：{0}", en: "Failed to check for updates: {0}" },
  "update.applyFail": { zh: "更新失败：{0}", en: "Update failed: {0}" },
  "update.notesAria": { zh: "更新日志", en: "Release notes" },
  "update.unknownReason": { zh: "未知错误", en: "unknown error" },

  // ---- 主题与语言 ----
  "theme.title": { zh: "主题：{0}", en: "Theme: {0}" },
  "theme.system": { zh: "跟随系统", en: "System" },
  "theme.light": { zh: "浅色", en: "Light" },
  "theme.dark": { zh: "深色", en: "Dark" },
  "theme.aria": { zh: "切换主题", en: "Switch theme" },
  // 语言入口：图标按钮打开三选一弹层（自动 system / 简体中文 / 英语），
  // 菜单仅三行纯语言名，当前配置由选中态表达；文案/aria 全部走词典
  "lang.title": { zh: "界面语言：{0}", en: "Interface language: {0}" },
  "lang.aria": { zh: "选择界面语言", en: "Choose interface language" },
  "lang.optionSystem": { zh: "自动", en: "Auto" },
  "lang.optionZh": { zh: "简体中文", en: "Simplified Chinese" },
  "lang.optionEn": { zh: "英语", en: "English" },
  "lang.syncFail": { zh: "语言偏好未保存：{0}", en: "Language preference was not saved: {0}" },

  // ---- Tab 与工具行 ----
  "tab.aria": { zh: "功能面板", en: "Panels" },
  "tab.tunnels": { zh: "隧道", en: "Tunnels" },
  "tab.stats": { zh: "统计", en: "Stats" },
  "tab.logs": { zh: "日志", en: "Logs" },
  "tab.servers": { zh: "配置", en: "Servers" },
  "action.addHttp": { zh: "新增 HTTP 服务", en: "Add HTTP Tunnel" },
  "action.addPort": { zh: "新增 TCP / UDP 服务", en: "Add TCP / UDP Tunnel" },
  "action.addServer": { zh: "新增 Frps", en: "Add Frps" },
  "log.autoscroll": { zh: "自动滚动", en: "Auto-scroll" },
  "log.clear": { zh: "清空显示", en: "Clear display" },
  // 日志面板公共归属固定为 System（不随界面语言翻译；不进词典）

  // ---- 空态 ----
  "empty.services": { zh: "还没有穿透服务，点右上角「新增」。", en: "No tunnels yet. Click \"Add\" in the top-right corner." },
  "empty.servers": { zh: "还没有 Frps 配置，点右上角「新增 Frps」。", en: "No Frps configurations yet. Click \"Add Frps\" in the top-right corner." },

  // ---- 统计 ----
  "stats.emptyTitle": { zh: "暂无统计数据", en: "No statistics yet" },
  "stats.emptyHint": {
    zh: "隧道产生访问后，这里会展示 HTTP 服务的请求统计。",
    en: "Once tunnels receive traffic, HTTP request statistics will appear here.",
  },
  "stats.loadFailTitle": { zh: "统计加载失败", en: "Failed to load statistics" },
  "stats.retryHint": { zh: "（每 2 秒自动重试）", en: " (retrying automatically every 2 seconds)" },
  "stats.refreshFail": { zh: "刷新失败：{0}，将自动重试", en: "Refresh failed: {0}; retrying automatically" },
  "stats.fetchFailLog": { zh: "统计拉取失败：{0}", en: "Failed to fetch statistics: {0}" },
  "stats.bindingsMissing": { zh: "Stats 绑定不存在（请升级后端）", en: "Stats binding missing (please upgrade the backend)" },
  "stats.rankServices": { zh: "服务访问排行", en: "Requests by tunnel" },
  "stats.rankTraffic": { zh: "服务流量排行", en: "Traffic by tunnel" },
  "stats.hotPaths": { zh: "热门路径", en: "Top paths" },
  "stats.noteHttpOnly": { zh: "仅统计 HTTP 服务", en: "HTTP tunnels only" },
  "stats.notePathsTop10": { zh: "全部服务合并 Top 10", en: "All tunnels combined, top 10" },
  "stats.timeline": { zh: "分时请求曲线", en: "Requests over time" },
  "stats.devices": { zh: "访问设备分布", en: "Visitor devices" },
  "stats.recent": { zh: "最近请求", en: "Recent requests" },
  "stats.totalRequests": { zh: "总请求", en: "Total requests" },
  "stats.totalTraffic": { zh: "总流量", en: "Total traffic" },
  "stats.clients": { zh: "用户数", en: "Visitors" },
  "stats.uniqueByUA": { zh: "按 UA 去重", en: "Unique by user agent" },
  "stats.since": { zh: "统计起点", en: "Since" },
  "stats.currentServerTip": { zh: "当前服务器：{0}", en: "Current server: {0}" },
  "stats.currentServer": { zh: "当前 {0}", en: "now {0}" },
  "stats.errTip": { zh: "5xx 错误 {0} 次", en: "{0} 5xx errors" },
  "stats.errCount": { zh: "{0} 错", en: "{0} err" },
  "stats.windowRecent": { zh: "近 6 小时", en: "Last 6 hours" },
  "stats.windowSince": { zh: "自 {0} 起", en: "Since {0}" },
  "stats.bucketHours": { zh: "{0} 小时", en: "{0} h" },
  "stats.bucketMinutes": { zh: "{0} 分钟", en: "{0} min" },
  "stats.bucketSeconds": { zh: "{0} 秒", en: "{0} s" },
  "stats.timelineNote": { zh: "{0} · 峰值 {1} · {2}/桶", en: "{0} · peak {1} · {2}/bucket" },
  "stats.chartAria": { zh: "{0}请求曲线，峰值 {1}", en: "{0} request curve, peak {1}" },
  "stats.colTime": { zh: "时间", en: "Time" },
  "stats.colService": { zh: "服务", en: "Service" },
  "stats.colServer": { zh: "服务器", en: "Server" },
  "stats.colMethod": { zh: "方法", en: "Method" },
  "stats.colPath": { zh: "路径", en: "Path" },
  "stats.colStatus": { zh: "状态", en: "Status" },
  "stats.colDuration": { zh: "耗时", en: "Duration" },
  "stats.colTraffic": { zh: "流量", en: "Traffic" },
  "stats.bytesTip": { zh: "入 {0} / 出 {1}", en: "in {0} / out {1}" },
  "stats.unknownService": { zh: "未知服务", en: "Unknown service" },
  "stats.hoverTip": { zh: "{0} · {1} 次", en: "{0} · {1} requests" },
  "stats.relJustNow": { zh: "刚刚", en: "just now" },
  "stats.relMinutes": { zh: "{0} 分钟前", en: "{0} min ago" },
  "stats.relHours": { zh: "{0} 小时前", en: "{0} h ago" },
  "stats.relDays": { zh: "{0} 天前", en: "{0} d ago" },
  "device.desktop": { zh: "桌面", en: "Desktop" },
  "device.mobile": { zh: "移动", en: "Mobile" },
  "device.tablet": { zh: "平板", en: "Tablet" },
  "device.bot": { zh: "爬虫", en: "Bot" },
  "device.other": { zh: "其他", en: "Other" },
  "device.pieAria": { zh: "设备类别分布，共 {0} 个客户", en: "Device mix, {0} visitors in total" },
  "device.clients": { zh: "客户", en: "visitors" },
  "device.segTip": { zh: "{0} {1}（{2}%）", en: "{0} {1} ({2}%)" },

  // ---- 底部状态栏 ----
  "state.unknown": { zh: "未知", en: "Unknown" },
  "state.stopped": { zh: "未启动", en: "Not running" },
  "state.starting": { zh: "正在登录 frps", en: "Logging in to frps" },
  "state.stopping": { zh: "停止中", en: "Stopping" },
  "state.running": { zh: "已连接", en: "Connected" },
  "state.failed": { zh: "失败", en: "Failed" },
  "status.countTip": { zh: "当前已映射隧道数量", en: "Currently mapped tunnels" },
  "status.failedCount": { zh: "{0} 失败", en: "{0} failed" },
  "status.proxyFailed": { zh: "{0} 个服务注册失败", en: "{0} tunnel(s) failed to register" },
  "status.startedFor": { zh: "已启动: {0}", en: "Uptime: {0}" },
  "action.quit": { zh: "退出程序", en: "Quit app" },
  "confirm.quitMsg": { zh: "退出将同时停止 frpc 隧道，确定退出程序？", en: "Quitting also stops frpc tunnels. Quit the app?" },
  "action.quitConfirm": { zh: "退出", en: "Quit" },

  // ---- 服务卡片与操作 ----
  "badge.disabled": { zh: "已停用", en: "Disabled" },
  "badge.waiting": { zh: "等待连接", en: "Waiting for connection" },
  "badge.notRunning": { zh: "未启动", en: "Not running" },
  "badge.registering": { zh: "注册中", en: "Registering" },
  "badge.unregistered": { zh: "未注册", en: "Unregistered" },
  "badge.registered": { zh: "已注册", en: "Registered" },
  "badge.registerFailed": { zh: "注册失败", en: "Registration failed" },
  "badge.registerFailedReason": { zh: "注册失败：{0}", en: "Registration failed: {0}" },
  "card.clickToCopy": { zh: "点击复制", en: "Click to copy" },
  "card.unassignedTip": { zh: "未分配目标 Frps，编辑后选择", en: "No target Frps assigned; choose one when editing" },
  "card.unassigned": { zh: "未分配", en: "Unassigned" },
  "card.serverTip": { zh: "目标 Frps：{0}", en: "Target Frps: {0}" },
  "card.localTargetTip": { zh: "本地映射目标", en: "Local target" },
  "action.copyAddress": { zh: "复制地址", en: "Copy address" },
  "action.showQr": { zh: "访问二维码", en: "Show QR code" },
  "action.edit": { zh: "编辑", en: "Edit" },
  "action.delete": { zh: "删除", en: "Delete" },
  "action.toggleEnabled": { zh: "启用/停用", en: "Enable/disable" },
  "action.reorder": { zh: "拖动调整顺序", en: "Drag to reorder" },

  // ---- Frps 配置卡片 ----
  "srv.noEnabledTunnels": { zh: "无已启用隧道", en: "No enabled tunnels" },
  "srv.connAddrTip": { zh: "连接地址", en: "Connection address" },
  "srv.linked": { zh: "关联 {0} 个隧道 · 已启用 {1} 个", en: "{0} tunnel(s) linked · {1} enabled" },
  "srv.testTip": {
    zh: "测试控制连接与鉴权（不验证 HTTP 入口）",
    en: "Test the control connection and authentication (does not verify the HTTP entry)",
  },
  "srv.testing": { zh: "测试中…", en: "Testing…" },
  "srv.test": { zh: "测试", en: "Test" },
  "srv.editTip": { zh: "编辑配置", en: "Edit configuration" },
  "srv.deleteTip": { zh: "删除配置", en: "Delete configuration" },

  // ---- 通用确认/按钮 ----
  "generic.cancel": { zh: "取消", en: "Cancel" },
  "generic.save": { zh: "保存", en: "Save" },
  "generic.delete": { zh: "删除", en: "Delete" },

  // ---- 操作失败前缀与操作名（toast / 前端日志共用） ----
  "run.fail": { zh: "{0}失败：{1}", en: "{0} failed: {1}" },
  "run.loadConfig": { zh: "加载配置", en: "Load configuration" },
  "run.loadProxyStatus": { zh: "加载代理状态", en: "Load proxy status" },
  "run.loadLogs": { zh: "加载日志", en: "Load logs" },
  "run.genQr": { zh: "生成二维码", en: "Generate QR code" },
  "run.saveConfig": { zh: "保存配置", en: "Save configuration" },
  "run.createConfig": { zh: "新增配置", en: "Add configuration" },
  "run.saveService": { zh: "保存服务", en: "Save tunnel" },
  "run.createService": { zh: "新增服务", en: "Add tunnel" },
  "run.deleteService": { zh: "删除服务", en: "Delete tunnel" },
  "run.toggleService": { zh: "切换状态", en: "Toggle state" },
  "run.deleteConfig": { zh: "删除配置", en: "Delete configuration" },
  "run.testConn": { zh: "测试连接", en: "Test connection" },
  "run.reorderServices": { zh: "保存排序", en: "Save order" },

  // ---- Toast ----
  "toast.copied": { zh: "地址已复制", en: "Address copied" },
  "toast.copyFail": { zh: "复制失败", en: "Copy failed" },
  "toast.configUpdated": { zh: "配置已更新", en: "Configuration updated" },
  "toast.configAdded": { zh: "配置已添加", en: "Configuration added" },
  "toast.configDeleted": { zh: "配置已删除", en: "Configuration deleted" },
  "toast.serviceUpdated": { zh: "服务已更新", en: "Tunnel updated" },
  "toast.serviceAdded": { zh: "服务已添加", en: "Tunnel added" },
  "toast.serviceDeleted": { zh: "服务已删除", en: "Tunnel deleted" },
  "toast.orderSaved": { zh: "排序已保存", en: "Order saved" },
  "toast.logsCleared": { zh: "已清空日志显示", en: "Log display cleared" },
  "toast.probeOk": { zh: "FRPS 登录成功 · {0} ms", en: "FRPS login succeeded · {0} ms" },
  "toast.probeFail": { zh: "连接失败：{0}", en: "Connection failed: {0}" },
  "toast.unknownReason": { zh: "未知原因", en: "unknown reason" },

  // ---- 模态：服务 ----
  "modal.addService": { zh: "新增服务", en: "Add tunnel" },
  "modal.editService": { zh: "编辑 {0} 服务", en: "Edit {0} tunnel" },
  "modal.editServer": { zh: "编辑 Frps 配置", en: "Edit Frps configuration" },
  "modal.addServer": { zh: "新增 Frps 配置", en: "Add Frps configuration" },
  "icon.pick": { zh: "选择图标", en: "Choose icon" },
  "icon.pickAria": { zh: "选择服务图标", en: "Choose service icon" },
  "icon.random": { zh: "🎲 随机", en: "🎲 Random" },
  "icon.gridAria": { zh: "精选图标", en: "Curated icons" },
  "form.proto": { zh: "协议", en: "Protocol" },
  "form.svcName": { zh: "服务名称", en: "Service name" },
  "form.svcNamePh": { zh: "例如：本地博客", en: "e.g. My local blog" },
  "form.localIP": { zh: "本地 IP", en: "Local IP" },
  "form.localPort": { zh: "本地端口", en: "Local port" },
  "form.subdomain": { zh: "子域名前缀", en: "Subdomain prefix" },
  "form.subdomainHelp": { zh: "留空自动生成", en: "Generated automatically if left empty" },
  "form.subdomainHelpAria": { zh: "子域名前缀说明", en: "Subdomain prefix help" },
  "form.subdomainPh": { zh: "例如：my-blog", en: "e.g. my-blog" },
  "form.remotePort": { zh: "远程端口", en: "Remote port" },
  "form.remotePortHelp": { zh: "frps 侧监听端口，必填", en: "Listening port on the frps side; required" },
  "form.remotePortHelpAria": { zh: "远程端口说明", en: "Remote port help" },
  "form.remotePortPh": { zh: "例如：36000", en: "e.g. 36000" },
  "form.targetServer": { zh: "目标 Frps", en: "Target Frps" },

  // ---- 模态：Frps 配置 ----
  "srvForm.configName": { zh: "配置名称", en: "Configuration name" },
  "srvForm.configNamePh": { zh: "例如：主服务器", en: "e.g. Primary server" },
  "srvForm.addr": { zh: "服务器地址", en: "Server address" },
  "srvForm.port": { zh: "端口", en: "Port" },
  // 长解释从小问号帮助入口（data-help）延迟展示，标题保持短文案
  "srvForm.authToken": { zh: "authToken", en: "authToken" },
  "srvForm.authTokenHelp": { zh: "可留空", en: "Optional" },
  "srvForm.authTokenHelpAria": { zh: "authToken 说明", en: "authToken help" },
  "srvForm.authTokenPh": { zh: "留空表示无鉴权", en: "Leave empty for no authentication" },
  "srvForm.httpToggle": { zh: "支持 HTTP 域名", en: "HTTP domains" },
  "srvForm.httpToggleHelp": {
    zh: "使用该 Frps 已配置的域名和端口作为 HTTP 隧道入口",
    en: "Use this Frps's configured domain and port as the HTTP tunnel entry",
  },
  "srvForm.httpToggleHelpAria": { zh: "支持 HTTP 域名说明", en: "HTTP domains help" },
  "srvForm.baseDomain": { zh: "根域名", en: "Base domain" },
  "srvForm.baseDomainHelp": { zh: "必填，HTTP 服务入口", en: "Required; the entry for HTTP tunnels" },
  "srvForm.baseDomainHelpAria": { zh: "根域名说明", en: "Base domain help" },
  "srvForm.scheme": { zh: "访问协议", en: "Access scheme" },
  "srvForm.publicPort": { zh: "访问端口", en: "Access port" },
  "srvForm.publicPortHelp": { zh: "留空使用协议默认端口", en: "Leave empty to use the protocol default port" },
  "srvForm.publicPortHelpAria": { zh: "访问端口说明", en: "Access port help" },
  "srvForm.publicPortPh": { zh: "例如：8443", en: "e.g. 8443" },

  // ---- 目标服务器选择与迁移提示 ----
  "pick.noHttpServer": { zh: "暂无支持 HTTP 的 Frps 配置", en: "No HTTP-capable Frps configuration" },
  "pick.noServer": { zh: "暂无 Frps 配置", en: "No Frps configuration" },
  "pick.currentSuffix": { zh: "（当前）", en: " (current)" },
  "hint.addr": { zh: "访问地址：{0}", en: "Access address: {0}" },
  "hint.fillRemotePort": { zh: "填写远程端口后生成访问地址", en: "Enter the remote port to generate the access address" },
  "hint.httpDepends": { zh: "HTTP 入口取决于该配置的根域名设置", en: "The HTTP entry depends on this configuration's base domain" },
  "hint.autoSubdomain": {
    zh: "保存时自动生成子域名，入口形如 {0}://{1}.{2}{3}",
    en: "A subdomain is generated on save; the entry will look like {0}://{1}.{2}{3}",
  },
  "hint.baseDomainFallback": { zh: "该配置根域名", en: "this configuration's base domain" },
  "hint.subdomainPlaceholder": { zh: "<子域名>", en: "<subdomain>" },
  "hint.moveInterrupt": { zh: "切换目标会使连接短暂中断", en: "Switching the target will briefly interrupt the connection" },
  "hint.join": { zh: "；", en: "; " },
  "pick.noTargetToast": { zh: "请选择目标 Frps", en: "Choose a target Frps" },
  "pick.noServerToast": { zh: "暂无 Frps 配置，请先到「配置」页添加", en: "No Frps configuration yet — add one on the \"Servers\" tab first" },
  "pick.noHttpServerToast": {
    zh: "暂无支持 HTTP 的 Frps 配置，请先到「配置」页添加并开启 HTTP",
    en: "No HTTP-capable Frps configuration yet — add one on the \"Servers\" tab and enable HTTP first",
  },

  // ---- 二维码 ----
  "qr.aria": { zh: "访问二维码", en: "Access QR code" },
  "qr.imgAlt": { zh: "访问地址二维码", en: "QR code for the access address" },
  "qr.loading": { zh: "生成中…", en: "Generating…" },
  "qr.fail": { zh: "生成失败", en: "Generation failed" },

  // ---- 删除确认 ----
  "confirm.deleteService": { zh: "确定删除服务「{0}」？", en: "Delete tunnel \"{0}\"?" },
  "confirm.deleteServer": {
    zh: "确定删除 Frps 配置「{0}」？仍有关联隧道（含已停用隧道）时不能删除。",
    en: "Delete Frps configuration \"{0}\"? It cannot be deleted while tunnels (including disabled ones) still reference it.",
  },
};

// 当前偏好与解析后的语言（模块级状态；main.js 通过 applyPref/currentPref/currentLang 使用）
let pref = PREF_SYSTEM;
let lang = PREF_EN;

// systemLang：系统语言解析。按用户首选顺序取第一项：中文（zh 开头）→ 简体中文，
// 其他语言 → 英文回退（不以“列表里出现中文”覆盖首选，避免忽略首选语言）。
export function systemLang() {
  let tags = [];
  try {
    if (Array.isArray(navigator?.languages) && navigator.languages.length) tags = navigator.languages;
    else if (navigator?.language) tags = [navigator.language];
  } catch {
    /* 无 navigator 的环境走回退 */
  }
  return langFromTags(tags);
}

// langFromTags：纯函数版本（供单测/复用）：取首个非空语言标签判定，空列表回退英文。
export function langFromTags(tags) {
  const list = Array.isArray(tags) ? tags : [tags];
  const first = list.find((x) => typeof x === "string" && x.trim() !== "");
  return /^zh([-_]|$)/i.test(String(first ?? "")) ? PREF_ZH : PREF_EN;
}

// normalizePref：偏好值归一：只允许 system/zh-CN/en，缺失/未知一律 system。
export function normalizePref(p) {
  return LANG_PREFS.includes(p) ? p : PREF_SYSTEM;
}

// normalizeLang：生效语言归一：只允许 zh-CN/en，其他返回 null（调用方自行兜底）。
export function normalizeLang(l) {
  return l === PREF_ZH || l === PREF_EN ? l : null;
}

// resolveLang：偏好 → 生效语言；system/未知偏好按系统语言解析（其他语言回退英文）。
export function resolveLang(p) {
  const pref = normalizePref(p);
  if (pref === PREF_ZH || pref === PREF_EN) return pref;
  return systemLang();
}

// readStoredPref：读取持久化偏好（旧 key 只读回退；非法值按 system）。
export function readStoredPref() {
  try {
    const v = localStorage.getItem(STORAGE_KEY) ?? localStorage.getItem(LEGACY_STORAGE_KEY);
    return LANG_PREFS.includes(v) ? v : PREF_SYSTEM;
  } catch {
    return PREF_SYSTEM;
  }
}

// currentPref / currentLang：当前偏好 / 生效语言。
export function currentPref() {
  return pref;
}

export function currentLang() {
  return lang;
}

// t(key, ...args)：取当前语言文案并做 {0}..{n} 参数插值。
// 查找顺序：当前语言 → 英文 → key 本身（缺失 key 可见且可排查）。
// 插值单次扫描模板：参数内容不再被后续参数递归替换（如服务名含 {1} 时原样保留），
// 参数中的 $ 等字符也不参与替换串特殊处理。
export function t(key, ...args) {
  const entry = DICT[key];
  if (!entry) return String(key);
  // 词典字段名固定为 zh/en（比语言标签紧凑）；此处做一次映射
  const field = lang === PREF_ZH ? "zh" : "en";
  return interpolate(entry[field] ?? entry.en ?? entry.zh ?? String(key), args);
}

// tEn(key, ...args)：固定英文取词（诊断日志通道专用）。与 t() 同插值规则，
// 但恒用 en 文案：进入后端日志的文本不随界面语言变化（toast 等可见 UI 仍走 t()）。
export function tEn(key, ...args) {
  const entry = DICT[key];
  if (!entry) return t(key, ...args);
  return interpolate(entry.en ?? entry.zh ?? String(key), args);
}

// interpolate {0}..{n} 插值：单次扫描，参数内容不递归替换。
function interpolate(text, args) {
  if (!args.length) return text;
  return text.replace(/\{([0-9])\}/g, (m, d) => (Number(d) < args.length ? String(args[Number(d)]) : m));
}

// applyPref：应用偏好并同步 <html lang>。opts.persist=true 时写入 localStorage；
// opts.resolved 为后端确认的生效语言（system 偏好的权威解析），传入时优先使用
// （保证 WebView 与原生托盘语言一致），缺失/非法时回退本地系统语言解析。
// 返回生效语言。调用方（main.js）负责触发界面重渲染与后端同步。
export function applyPref(p, opts = {}) {
  pref = normalizePref(p);
  lang = normalizeLang(opts.resolved) ?? resolveLang(pref);
  try {
    if (opts.persist) localStorage.setItem(STORAGE_KEY, pref);
  } catch {
    /* 隐私模式等场景允许失败 */
  }
  try {
    document.documentElement.lang = lang;
  } catch {
    /* 无 document 的环境（词典自检脚本）允许跳过 */
  }
  return lang;
}