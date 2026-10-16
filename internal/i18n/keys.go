package i18n

// 文案 key 常量：仅收录「直接可见」的用户文案（状态、托盘菜单、表单校验、
// 探测/更新提示等）；技术日志（slog / 环形日志 LogEntry）保持原文不经过这里。
// 与前端 frontend/dist/i18n.js 的词典相互独立（后端消息不走前端词典）。
const (
	// 托盘菜单与状态
	KeyTrayShow       = "tray.show"
	KeyTrayQuit       = "tray.quit"
	KeyTrayTooltip    = "tray.tooltip"
	KeyStateStopped   = "state.stopped"
	KeyStateStarting  = "state.starting"
	KeyStateStopping  = "state.stopping"
	KeyStateRunningN  = "state.runningN"
	KeyStateFailedNM  = "state.failedNM"
	KeyAppDescription = "app.description"
)

// entry 双语文案条目：zh 简体中文（权威），en 英文回退。
type entry struct{ zh, en string }

// dict 集中词典。新增文案时 zh/en 都必须非空（由 i18n_test 兜底校验）。
var dict = map[string]entry{
	// ---- 应用 ----
	KeyAppDescription: {"frp 穿透客户端", "frp tunnel client"},

	// ---- 托盘 ----
	KeyTrayShow:    {"显示主窗口", "Show Main Window"},
	KeyTrayQuit:    {"退出 frpdesk", "Quit frpdesk"},
	KeyTrayTooltip: {"frpdesk 托盘", "frpdesk tray"},

	// ---- 聚合/托盘状态 ----
	KeyStateStopped:  {"已停止", "Stopped"},
	KeyStateStarting: {"启动中", "Starting"},
	KeyStateStopping: {"停止中", "Stopping"},
	KeyStateRunningN: {"已连接 {0}", "Connected {0}"},
	KeyStateFailedNM: {"失败 {0}/{1}", "Failed {0}/{1}"},

	// ---- 实例状态消息（TunnelStatus.Message，状态栏/托盘直达） ----
	"msg.loggingIn":           {"正在登录 frps", "Logging in to frps"},
	"msg.connected":           {"已连接", "Connected"},
	"msg.reconnecting":        {"连接断开，正在重连", "Connection lost, reconnecting"},
	"msg.stopping":            {"停止中", "Stopping"},
	"msg.stopped":             {"frpc 已停止", "frpc stopped"},
	"msg.frpcPanic":           {"frpc 运行异常：{0}", "frpc runtime error: {0}"},
	"msg.frpcPanicHit":        {"frpc 内部 panic：{0}", "frpc internal panic: {0}"},
	"msg.serversFailed":       {"{0} 台服务器失败", "{0} server(s) failed"},
	"msg.serversFailedReason": {"{0} 台服务器失败：{1}", "{0} server(s) failed: {1}"},
	"msg.startFailed":         {"{0} 台服务器启动失败（{1}）", "{0} server(s) failed to start ({1})"},
	"msg.failureItem":         {"{0}：{1}", "{0}: {1}"},
	"msg.failureJoin":         {"；", "; "},
	"msg.noAddr":              {"请先配置 frps 服务器地址", "Configure the frps server address first"},
	"msg.httpDisabledSvc":     {"服务器「{0}」未开启 HTTP 支持，但隧道「{1}」为 HTTP 类型", "Server \"{0}\" has HTTP disabled but tunnel \"{1}\" is HTTP"},
	"msg.noEnabled":           {"服务器「{0}」没有启用的隧道", "Server \"{0}\" has no enabled tunnels"},

	// ---- configstore 校验错误 ----
	"err.nameRequired":         {"服务器名称不能为空", "Server name is required"},
	"err.nameExists":           {"服务器名称已存在：{0}", "Server name already exists: {0}"},
	"err.addrRequired":         {"服务器地址不能为空", "Server address is required"},
	"err.portRange":            {"服务器端口必须在 1-65535 之间", "Server port must be between 1-65535"},
	"err.endpointExists":       {"已存在相同地址的服务器配置：{0}:{1}（{2}）", "A server with the same address already exists: {0}:{1} ({2})"},
	"err.baseDomainRequired":   {"启用 HTTP 隧道必须填写根域名", "A base domain is required when HTTP tunnels are enabled"},
	"err.publicPortRange":      {"协议端口必须在 0-65535 之间（0 表示协议默认端口）", "Public port must be between 0-65535 (0 = protocol default port)"},
	"err.serverNotFound":       {"服务器不存在", "Server not found"},
	"err.httpRefCount":         {"仍有 {0} 条 HTTP 隧道引用此服务器，不能关闭 HTTP 支持", "{0} HTTP tunnel(s) still reference this server; HTTP support cannot be disabled"},
	"err.tunnelRefCount":       {"仍有 {0} 条隧道引用此服务器（含停用），请先删除或移动隧道", "{0} tunnel(s) still reference this server (including disabled ones); delete or move them first"},
	"err.svcNameRequired":      {"服务名称不能为空", "Tunnel name is required"},
	"err.localPortRange":       {"本地端口必须在 1-65535 之间", "Local port must be between 1-65535"},
	"err.serverRequired":       {"隧道必须归属一台已配置的服务器", "A tunnel must belong to a configured server"},
	"err.iconTooLong":          {"图标最多 8 个字符", "Icon may be at most 8 characters"},
	"err.typeInvalid":          {"服务类型仅支持 http、tcp 或 udp", "Tunnel type must be http, tcp, or udp"},
	"err.httpNotSupported":     {"服务器「{0}」未开启 HTTP 隧道支持，HTTP 隧道必须归属支持 HTTP 的服务器", "Server \"{0}\" does not have HTTP enabled; HTTP tunnels require an HTTP-capable server"},
	"err.remotePortRange":      {"远程端口必须在 1-65535 之间", "Remote port must be between 1-65535"},
	"err.remotePortTaken":      {"远程端口已被此服务器上其他 {0} 服务占用：{1}", "Remote port {1} is already used by another {0} service on this server"},
	"err.subdomainLen":         {"域名前缀长度需在 3-63 位之间", "Subdomain must be 3-63 characters long"},
	"err.subdomainChars":       {"域名前缀只能包含小写字母、数字和短横线", "Subdomain may only contain lowercase letters, digits, and hyphens"},
	"err.subdomainExists":      {"域名前缀已存在：{0}", "Subdomain already exists: {0}"},
	"err.serviceNotFound":      {"服务不存在", "Tunnel not found"},
	"err.reorderCount":         {"排序请求与隧道数量不一致（当前 {0} 条，收到 {1} 条）", "Reorder request does not match the tunnel count ({0} configured, {1} received)"},
	"err.reorderDup":           {"排序请求包含重复的隧道 ID：{0}", "Reorder request contains a duplicate tunnel ID: {0}"},
	"err.reorderUnknown":       {"排序请求包含未知隧道：{0}", "Reorder request contains an unknown tunnel: {0}"},
	"err.tunnelServerMissing":  {"隧道归属的服务器不存在", "The server this tunnel belongs to no longer exists"},
	"err.serverNoBaseDomain":   {"<服务器未配置根域名>", "<server has no base domain>"},
	"err.configDir":            {"创建配置目录失败", "Failed to create the config directory"},
	"err.userConfigDir":        {"获取用户配置目录失败", "Failed to resolve the user config directory"},
	"err.statConfig":           {"检查新配置失败", "Failed to inspect the new config file"},
	"err.statLegacy":           {"检查旧配置失败", "Failed to inspect the legacy config file"},
	"err.readLegacy":           {"读取旧配置失败", "Failed to read the legacy config file"},
	"err.createLegacyCopy":     {"复制旧配置失败", "Failed to copy the legacy config file"},
	"err.writeConfig":          {"写入新配置失败", "Failed to write the new config file"},
	"err.marshalConfig":        {"序列化配置失败", "Failed to serialize the config"},
	"legacy.defaultServerName": {"默认服务器", "Default Server"},
	"err.invalidLanguage":      {"语言偏好仅支持 system、zh-CN 或 en", "Language preference must be system, zh-CN, or en"},

	// ---- 连接/代理运行错误（buildService 路径，failed 终态消息） ----
	"err.listenProxyFail":   {"监听本地代理端口失败", "Failed to listen on the local proxy port"},
	"err.svcProxyFail":      {"服务 {0} 创建本地代理失败", "Failed to create the local proxy for service {0}"},
	"err.registerProxyFail": {"注册代理配置失败", "Failed to register proxy configuration"},
	"err.createFrpcFail":    {"创建 frpc 服务失败", "Failed to create the frpc service"},
	"err.proxySetClosed":    {"跨域代理集合已关闭", "The local proxy set is closed"},

	// ---- 连通性探测（probe） ----
	"probe.queueTimeout": {"探测排队超时，请稍后重试", "Probe queue timed out; please retry"},
	"probe.addrEmpty":    {"服务器地址为空", "Server address is empty"},
	"probe.initFail":     {"初始化探测客户端失败", "Failed to initialize the probe client"},
	"probe.createFail":   {"创建探测客户端失败", "Failed to create the probe client"},
	"probe.timeout":      {"连接超时（8 秒内未完成登录，请检查地址与端口）", "Connection timed out (login did not complete within 8 seconds; check the address and port)"},
	"probe.ok":           {"FRPS 控制连接与鉴权通过（不代表 HTTP 域名或业务端口可用）", "FRPS control connection and authentication passed (does not verify the HTTP domain or service port)"},
	"probe.connClosed":   {"连接已关闭", "Connection closed"},
	"probe.refused":      {"无法连接 frps：地址或端口不可达（连接被拒绝）", "Cannot reach frps: address or port unreachable (connection refused)"},
	"probe.ioTimeout":    {"无法连接 frps：连接超时", "Cannot reach frps: connection timed out"},
	"probe.authFail":     {"frps 拒绝登录：鉴权失败（请检查 authToken）", "frps rejected the login: authentication failed (check authToken)"},
	"probe.loginFail":    {"无法连接 frps：登录被拒绝或连接中断", "Cannot reach frps: login rejected or connection interrupted"},

	// ---- 逐服务注册失败友好原因（proxystatus） ----
	"proxy.portUsed":        {"远程端口已被其他服务占用", "Remote port already used by another service"},
	"proxy.addrInUse":       {"端口已被占用", "Port already in use"},
	"proxy.portUnavailable": {"远程端口不可用", "Remote port unavailable"},
	"proxy.portNotAllowed":  {"远程端口不在 frps 允许范围内", "Remote port outside the range allowed by frps"},
	"proxy.exists":          {"代理名称或子域名已被其他客户端使用", "Proxy name or subdomain already used by another client"},
	"proxy.conflict":        {"与 frps 上已有代理配置冲突", "Conflicts with an existing proxy on frps"},

	// ---- 应用内更新（updater） ----
	"err.updaterBusy":         {"已有更新操作在进行，请稍后再试", "Another update operation is in progress; try again later"},
	"err.updaterDisabled":     {"更新检查已禁用", "Update checks are disabled"},
	"err.noPendingUpdate":     {"当前没有已检查的新版本，请先检查更新", "No checked update is pending; check for updates first"},
	"err.updaterNotInit":      {"更新器尚未初始化", "The updater is not initialized"},
	"err.updaterShuttingDown": {"应用正在退出，更新操作已取消", "The app is quitting; the update was canceled"},
	"upd.devDisabled":         {"开发版（版本号未注入），更新检查已禁用", "Development build (version not injected); update checks are disabled"},
	"upd.noManifest":          {"未配置更新源，更新检查已禁用", "No update source configured; update checks are disabled"},
	"upd.manifestInitFail":    {"更新源初始化失败：{0}", "Failed to initialize the update source: {0}"},
	"upd.initFail":            {"更新器初始化失败：{0}", "Failed to initialize the updater: {0}"},
	"upd.checkFail":           {"检查更新失败：{0}", "Failed to check for updates: {0}"},
	"upd.upToDate":            {"已是最新版本", "Already up to date"},
	"upd.available":           {"发现新版本 {0}", "New version {0} available"},
	"upd.installFail":         {"更新失败：{0}", "Update failed: {0}"},
	"upd.restarting":          {"正在重启以应用更新", "Restarting to apply the update"},
	"upd.downloading":         {"开始下载更新", "Downloading update"},
	"upd.downloadingProgress": {"正在下载更新 {0}/{1}", "Downloading update {0}/{1}"},
	"upd.downloaded":          {"正在下载更新（已下载 {0}）", "Downloading update ({0} downloaded)"},
	"upd.verifying":           {"正在校验更新包", "Verifying the update package"},
	"upd.installing":          {"正在安装更新", "Installing the update"},
	"upd.ready":               {"更新已就绪，即将重启", "Update ready; restarting"},
	"upd.restartFail":         {"重启以应用更新失败：{0}", "Failed to restart to apply the update: {0}"},
}
