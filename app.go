package main

import (
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"log/slog"
	"strings"

	qrcode "github.com/skip2/go-qrcode"
	"github.com/wailsapp/wails/v3/pkg/application"
	"github.com/wailsapp/wails/v3/pkg/updater"

	"frpdesk/internal/configstore"
	"frpdesk/internal/i18n"
	"frpdesk/internal/stats"
	"frpdesk/internal/tunnel"
)

// AppService 暴露给前端的服务：服务器/隧道 CRUD + 多实例启停 + 状态/日志事件推送。
// 方法名会被 wails3 generate bindings 生成为前端 JS API。
type AppService struct {
	store    *configstore.Store
	tunnel   *tunnel.Manager
	stats    *stats.Collector // HTTP 请求级流量统计（App 级内存态，随进程重启归零）
	wailsApp *application.App
	tray     *trayController    // 托盘/窗口控制；attachUI 后可用
	up       *updaterController // 应用内更新（updater.go）；attachUI 装配框架 Updater
}

// NewAppService 创建服务实例（应用启动时调用，失败直接退出）。
func NewAppService() *AppService {
	store, err := configstore.New()
	if err != nil {
		slog.Error("初始化配置存储失败", "err", err)
		panic(err)
	}
	// 按持久化偏好解析界面语言（system 时探测系统语言）：必须在托盘/窗口
	// 装配前完成，保证启动时的托盘菜单与状态就是用户语言。
	i18n.SetPreference(store.Language())
	slog.Info("[i18n] 界面语言已解析", "lang", i18n.Current(), "pref", store.Language())
	mgr := tunnel.NewManager()

	// 请求统计：collector 与 App 同生命周期（不随隧道启停清零，重启归零）；
	// tunnel 代理层回调桥接到收集器，serverID/serviceID 按请求发生时捕获
	svc := &AppService{store: store, tunnel: mgr, stats: stats.NewCollector()}
	svc.syncStatsMappings()
	mgr.RequestReporter = func(serverID, serverName, serviceID, method, path, ua string, status int, durationMs int64, bytesIn, bytesOut int64) {
		svc.stats.Record(stats.RecordInput{
			ServiceID: serviceID, ServerID: serverID, ServerName: serverName,
			Method: method, Path: path, UA: ua,
			Status: status, DurationMs: durationMs, BytesIn: bytesIn, BytesOut: bytesOut,
		})
	}
	svc.up = newUpdaterController(svc.logUpdate)

	// 任一实例状态变化 → 重新组装聚合快照推送（多实例下聚合状态需配置数据，
	// 由宿主计算；回调仅为信号，锁外触发）
	mgr.OnStatus = func() {
		if svc.tray != nil {
			svc.tray.updateStatus()
		}
		if svc.wailsApp != nil {
			snap := svc.Snapshot()
			svc.wailsApp.Event.Emit("app:status", snap.Status)
			svc.wailsApp.Event.Emit("app:snapshot", snap)
		}
	}
	mgr.OnLogs = func(entries []configstore.LogEntry) {
		if svc.wailsApp != nil {
			svc.wailsApp.Event.Emit("app:logs", entries)
		}
	}
	mgr.OnProxyStatus = func([]tunnel.ProxyStatus) {
		// 逐服务采样变化（注册成功/失败）会改变托盘「已连接 N」计数，先刷托盘
		if svc.tray != nil {
			svc.tray.updateStatus()
		}
		if svc.wailsApp != nil {
			// 读 Manager 当前最新快照：迟到的旧采样回调不会把过期状态推给前端
			svc.wailsApp.Event.Emit("app:proxies", svc.tunnel.ProxyStatuses())
		}
	}
	return svc
}

// syncStatsMappings 把当前配置的展示映射喂给统计收集器
// （服务名、服务当前归属、服务器名；配置变更后刷新）。
func (a *AppService) syncStatsMappings() {
	cfg := a.store.Config()
	names := make(map[string]string, len(cfg.Services))
	servers := make(map[string]string, len(cfg.Services))
	for i := range cfg.Services {
		names[cfg.Services[i].ID] = cfg.Services[i].Name
		servers[cfg.Services[i].ID] = cfg.Services[i].ServerID
	}
	serverNames := make(map[string]string, len(cfg.Servers))
	for i := range cfg.Servers {
		serverNames[cfg.Servers[i].ID] = cfg.Servers[i].Name
	}
	a.stats.SetServiceNames(names)
	a.stats.SetServiceServers(servers)
	a.stats.SetServerNames(serverNames)
}

// 编译期断言：生命周期钩子签名必须匹配 wails v3 的接口。
var (
	_ application.ServiceStartup  = (*AppService)(nil)
	_ application.ServiceShutdown = (*AppService)(nil)
)

// ServiceStartup 是 wails 服务生命周期钩子，应用就绪后回调。
// wailsApp 已由 attachUI 在 Run 前赋值，此处不再重复写入。
func (a *AppService) ServiceStartup(_ context.Context, _ application.ServiceOptions) error {
	// 应用启动即对全部配置服务器做一次对齐：有 enabled 隧道的自动连接，
	// 无隧道的不空连（服务器完全由 Service.Enabled 驱动，无手动启停入口）
	a.reconcileAll()
	// 启动更新检查（异步不阻塞；dev 版内部自动禁用；仅 Check，绝不自动安装）
	go a.up.startupCheck()
	// Windows：异步清理更新替换残留（<exe>.old.<数字>，内部延迟 5s 首扫 + 有界重试）
	a.startUpdateCleanup()
	return nil
}

// ServiceShutdown 停掉全部隧道实例，保证退出时不留连接。
// 先置位托盘 quitting：wails cleanup 顺序是先停服务再销毁窗口/托盘，
// 期间状态回调仍会到达，置位后跳过 UI 更新，避免退出回调卡死或触碰已销毁对象。
func (a *AppService) ServiceShutdown() error {
	slog.Info("[退出] ServiceShutdown：停止所有隧道")
	a.up.shutdown() // 先取消后台更新检查与进行中的下载/安装
	if a.tray != nil {
		a.tray.markQuitting()
	}
	a.tunnel.StopAll()
	return nil
}

// attachUI 在应用对象创建后、Run 之前装配托盘、窗口钩子与更新器（框架就绪期安全初始化）。
func (a *AppService) attachUI(app *application.App, window *application.WebviewWindow) {
	a.wailsApp = app
	a.tray = newTrayController(app, window, a)

	// 更新器：复用框架 app.Updater（官方 pkg/updater），Init 在此完成（含 dev 禁用判定）。
	// dev 版在内部短路，不会真正 Init/建连。
	a.up.attach(app.Updater, currentVersion, func(name string, data any) {
		app.Event.Emit(name, data)
	})

	// 框架更新事件 → 本地状态镜像（进度/校验/安装）。
	// 迟到事件不回退状态的判定在 controller 内（框架状态 + 本地状态双重校验）。
	app.Event.On(updater.EventDownloadProgress, func(e *application.CustomEvent) {
		if p, ok := e.Data.(updater.Progress); ok {
			a.up.onProgress(p)
		}
	})
	app.Event.On(updater.EventVerifying, func(*application.CustomEvent) { a.up.onVerifying() })
	app.Event.On(updater.EventInstalling, func(*application.CustomEvent) { a.up.onInstalling() })
}

// logUpdate 更新日志双写：slog 落终端 + 应用日志环形缓冲推前端（app:logs）。
// 经 Manager.AppendLog 写入，只追加日志，不推进实例状态机。
func (a *AppService) logUpdate(level, format string, args ...any) {
	msg := fmt.Sprintf(format, args...)
	switch level {
	case "error":
		slog.Error(msg)
	case "warn":
		slog.Warn(msg)
	default:
		slog.Info(msg)
	}
	a.tunnel.AppendLog(level, msg)
}

// UpdateInfo 返回当前更新状态（前端绑定拉取；与 app:update 事件同构）。
func (a *AppService) UpdateInfo() UpdateInfo {
	return a.up.snapshot()
}

// CheckForUpdates 手动检查更新（与启动/定时检查互斥，忙时返回明确错误）。
func (a *AppService) CheckForUpdates() (UpdateInfo, error) {
	return a.up.check("手动检查")
}

// InstallUpdate 用户确认后的下载安装重启（无已检查新版时返回明确错误）。
func (a *AppService) InstallUpdate() error {
	return a.up.install()
}

// showMainWindow 统一恢复主窗口：托盘菜单/Dock reopen/第二实例启动共用。
func (a *AppService) showMainWindow() {
	if a.tray != nil {
		a.tray.restoreWindow()
	}
}

// FrontendLog 接收前端侧诊断/错误日志（可观测性通道）。
func (a *AppService) FrontendLog(level, message string) error {
	switch level {
	case "error":
		slog.Error("[前端] " + message)
	default:
		slog.Info("[前端] " + message)
	}
	return nil
}

// LanguageState 语言状态：绑定返回的最小契约 = 持久化偏好 + 后端解析后的生效语言。
// 前端据此对齐 UI 语言（system 时不再自行用 navigator 独立判定）。
type LanguageState struct {
	Preference string `json:"preference"` // system/zh-CN/en
	Resolved   string `json:"resolved"`   // zh-CN/en（后端系统探测结果或显式选择）
}

// SetLanguage 持久化语言偏好（system/zh-CN/en）并立即应用：更新进程 i18n、
// 刷新托盘菜单/状态文案，并记录日志。返回解析后的生效语言（LanguageState）
// 供前端对齐（system 时前端采用后端探测结果，不再自行判定）。
// 不触碰隧道/服务器状态（不重启隧道）。
func (a *AppService) SetLanguage(pref string) (LanguageState, error) {
	if !i18n.IsValidPref(pref) {
		return a.languageState(), errors.New(i18n.T("err.invalidLanguage"))
	}
	if err := a.store.SetLanguage(pref); err != nil {
		return a.languageState(), err
	}
	lang := i18n.SetPreference(pref)
	slog.Info("[i18n] 语言已切换", "pref", pref, "lang", lang)
	if a.tray != nil {
		a.tray.applyLanguage()
	}
	// 状态说明类消息按新语言重建并重推快照：前端状态栏/托盘原因行即时跟随
	// （错误详情保留发生时的原文；不触碰隧道，不重连）。
	a.tunnel.RefreshLocalizedMessages()
	a.emitSnapshot()
	return a.languageState(), nil
}

// languageState 当前语言状态（偏好 + 解析后的生效语言）。
func (a *AppService) languageState() LanguageState {
	return LanguageState{Preference: a.store.Language(), Resolved: string(i18n.Current())}
}

// Snapshot 返回配置 + 聚合状态 + 逐服务器状态快照（前端绑定拉取）。
func (a *AppService) Snapshot() configstore.Snapshot {
	return a.buildSnapshot()
}

// reconcileAll 对全部配置服务器做一次期望态对齐（启动初始化与托盘等内部入口）。
func (a *AppService) reconcileAll() {
	cfg := a.store.Config()
	a.tunnel.ReconcileAll(cfg.Servers, cfg.Services)
	a.syncStatsMappings()
	a.emitSnapshot()
}

// buildSnapshot 组装快照：serverStatuses 按配置顺序输出（含未运行的 stopped 项），
// 聚合 status 的 totalCount 按「有 enabled 隧道的服务器数」计算。
func (a *AppService) buildSnapshot() configstore.Snapshot {
	cfg := a.store.Config()
	known := a.tunnel.ServerStatuses()
	list := make([]configstore.ServerStatus, 0, len(cfg.Servers))
	total := 0
	for i := range cfg.Servers {
		st, ok := known[cfg.Servers[i].ID]
		if !ok {
			st = configstore.ServerStatus{ServerID: cfg.Servers[i].ID, State: configstore.StateStopped}
		}
		list = append(list, st)
		if cfg.EnabledCount(cfg.Servers[i].ID) > 0 {
			total++
		}
	}
	return configstore.Snapshot{
		Config:         cfg,
		Status:         configstore.AggregateStatus(list, total),
		ServerStatuses: list,
		Language:       string(i18n.Current()),
	}
}

// emitSnapshot 主动推送最新聚合快照：所有配置/启停写操作成功后调用，
// 包括不触发实例状态变化的 stopped no-op——前端状态权威是 app:snapshot 事件
// （RPC 返回值不携带权威状态），漏推会导致「已连接 N/M」的分母不更新。
// 托盘同样在此刷新（分母/服务器名）：给已停止服务器新增 enabled 隧道等
// 纯配置变更不会触发 OnStatus，托盘只靠这里是最新分母。
// 与 OnStatus 回调的推送可能重复，数据幂等无害。
func (a *AppService) emitSnapshot() {
	if a.tray != nil {
		a.tray.updateStatus() // 内部有 quitting 检查；wailsApp 未就绪也要刷新托盘
	}
	if a.wailsApp == nil {
		return
	}
	snap := a.Snapshot()
	a.wailsApp.Event.Emit("app:status", snap.Status)
	a.wailsApp.Event.Emit("app:snapshot", snap)
}

// Logs 返回当前缓冲的全部日志。
func (a *AppService) Logs() []configstore.LogEntry {
	logs := a.tunnel.Logs()
	slog.Info("[前端] 拉取日志", "count", len(logs), "state", a.tunnel.Status().State)
	return logs
}

// ProxyStatuses 返回逐服务代理运行状态快照（采样自 frp StatusExporter）。
func (a *AppService) ProxyStatuses() []tunnel.ProxyStatus {
	return a.tunnel.ProxyStatuses()
}

// Stats 返回 HTTP 请求级流量统计快照。
// 生命周期语义：App 进程内存态——不随隧道启停清零、不持久化、重启归零。
// 仅统计经内置代理的 enabled HTTP 服务；TCP/UDP 直连无统计，前端标注「仅 HTTP 服务」。
func (a *AppService) Stats() stats.Snapshot {
	return a.stats.Snapshot()
}

// ---------- 服务器 CRUD 与启停 ----------

// CreateServer 新增服务器：若其下已有 enabled 隧道（导入/恢复场景）会自动连接。
func (a *AppService) CreateServer(input configstore.ServerInput) (configstore.Snapshot, error) {
	slog.Info("[前端] 新增服务器", "name", input.Name, "addr", input.ServerAddr, "port", input.ServerPort)
	cfg, err := a.store.CreateServer(input)
	if err != nil {
		return a.Snapshot(), err
	}
	if len(cfg.Servers) > 0 {
		newest := cfg.Servers[len(cfg.Servers)-1]
		a.reconcileServers(cfg, newest.ID)
	}
	a.emitSnapshot()
	return a.Snapshot(), nil
}

// UpdateServer 编辑服务器：立即应用——连接参数变化仅重连该服务器，
// 元数据变化不影响连接；未连接的服务器只保存。
func (a *AppService) UpdateServer(id string, input configstore.ServerInput) (configstore.Snapshot, error) {
	slog.Info("[前端] 编辑服务器", "id", id, "name", input.Name)
	cfg, err := a.store.UpdateServer(id, input)
	if err != nil {
		return a.Snapshot(), err
	}
	a.syncStatsMappings()
	// 按期望态对齐：连接参数变化仅重连自身；元数据/代理变化热更新；有 enabled
	// 隧道未连接则自动连接；enabled 清零自动停止
	server, ok := cfg.ServerByID(id)
	if ok {
		a.tunnel.Reconcile(server, cfg.Services)
	}
	a.emitSnapshot() // 无状态变化的对齐也推最新快照（分母/元数据更新）
	return a.Snapshot(), nil
}

// DeleteServer 删除服务器（被任何隧道引用——含停用——则阻止）。
func (a *AppService) DeleteServer(id string) (configstore.Snapshot, error) {
	slog.Info("[前端] 删除服务器", "id", id)
	if _, err := a.store.DeleteServer(id); err != nil {
		return a.Snapshot(), err
	}
	a.syncStatsMappings()
	a.emitSnapshot()
	return a.Snapshot(), nil
}

// summarizeFailures 汇总当前失败服务器的可读原因（无失败返回 nil）。
func (a *AppService) summarizeFailures() error {
	snap := a.Snapshot()
	var failed []string
	for _, st := range snap.ServerStatuses {
		if st.State == configstore.StateFailed {
			name := st.ServerID
			for i := range snap.Config.Servers {
				if snap.Config.Servers[i].ID == st.ServerID {
					name = snap.Config.Servers[i].Name
					break
				}
			}
			failed = append(failed, i18n.T("msg.failureItem", name, st.Message))
		}
	}
	if len(failed) == 0 {
		return nil
	}
	return errors.New(i18n.T("msg.startFailed", len(failed), strings.Join(failed, i18n.T("msg.failureJoin"))))
}

// ---------- 隧道 CRUD ----------

// reconcileServers 隧道/服务器变更后对受影响服务器做期望态对齐
// （服务器完全由 Service.Enabled 驱动）：
//   - enabledCount > 0：自动连接/维持——无实例自动启动（含新增/启用首个）；
//     连接参数变化仅重连自身；否则热更新（保留 client.Service）；
//   - enabledCount == 0：自动停止并释放实例（禁用/删除/移走最后一个）；
//   - 跨服务器移动：调用方按 先旧后新 顺序传入，A 先移除/停空、B 再自动连接。
func (a *AppService) reconcileServers(cfg configstore.Config, serverIDs ...string) {
	a.syncStatsMappings()
	seen := map[string]bool{}
	for _, id := range serverIDs {
		if id == "" || seen[id] {
			continue
		}
		seen[id] = true
		server, ok := cfg.ServerByID(id)
		if !ok {
			continue // 服务器已被删除（服务随之无归属场景）
		}
		a.tunnel.Reconcile(server, cfg.Services)
	}
}

// CreateService 新增穿透隧道；受影响服务器按期望态自动对齐（enabled>0 即连接）。
func (a *AppService) CreateService(input configstore.ServiceInput) (configstore.Snapshot, error) {
	slog.Info("[前端] 新增服务", "name", input.Name, "serverId", input.ServerID, "type", input.Type)
	cfg, err := a.store.CreateService(input)
	if err != nil {
		return a.Snapshot(), err
	}
	a.reconcileServers(cfg, input.ServerID)
	a.emitSnapshot()
	return a.Snapshot(), nil
}

// UpdateService 编辑穿透隧道：跨服务器移动时先旧服务器移除代理、新服务器再应用；
// 全部本地校验通过才保存（store 层保证）。
func (a *AppService) UpdateService(id string, input configstore.ServiceInput) (configstore.Snapshot, error) {
	slog.Info("[前端] 编辑服务", "id", id, "name", input.Name, "serverId", input.ServerID)
	old := a.store.Config()
	var oldServerID string
	for i := range old.Services {
		if old.Services[i].ID == id {
			oldServerID = old.Services[i].ServerID
			break
		}
	}
	cfg, err := a.store.UpdateService(id, input)
	if err != nil {
		return a.Snapshot(), err
	}
	a.reconcileServers(cfg, oldServerID, input.ServerID)
	a.emitSnapshot()
	return a.Snapshot(), nil
}

// SetServiceEnabled 切换隧道 enabled 开关；撤销最后一个 enabled 隧道会停止该服务器连接。
func (a *AppService) SetServiceEnabled(id string, enabled bool) (configstore.Snapshot, error) {
	slog.Info("[前端] 切换服务开关", "id", id, "enabled", enabled)
	prev := a.store.Config()
	var serverID string
	for i := range prev.Services {
		if prev.Services[i].ID == id {
			serverID = prev.Services[i].ServerID
			break
		}
	}
	cfg, err := a.store.SetServiceEnabled(id, enabled)
	if err != nil {
		return a.Snapshot(), err
	}
	a.reconcileServers(cfg, serverID)
	a.emitSnapshot()
	return a.Snapshot(), nil
}

// DeleteService 删除穿透隧道；该服务器最后一个 enabled 隧道被删则停止连接。
func (a *AppService) DeleteService(id string) (configstore.Snapshot, error) {
	slog.Info("[前端] 删除服务", "id", id)
	prev := a.store.Config()
	var serverID string
	for i := range prev.Services {
		if prev.Services[i].ID == id {
			serverID = prev.Services[i].ServerID
			break
		}
	}
	cfg, err := a.store.DeleteService(id)
	if err != nil {
		return a.Snapshot(), err
	}
	a.reconcileServers(cfg, serverID)
	a.emitSnapshot()
	return a.Snapshot(), nil
}

// QuitApp 前端「退出程序」入口（二次确认后调用）：复用托盘退出路径——
// 置 quitting 后 app.Quit，ServiceShutdown 负责停止全部隧道等清理。
func (a *AppService) QuitApp() {
	slog.Info("[前端] 点击退出程序")
	if a.tray != nil {
		a.tray.quit()
	}
}

// PublicURL 返回 HTTP 隧道的公网访问地址（按归属服务器入口元数据拼接）。
func (a *AppService) PublicURL(id string) (string, error) {
	cfg := a.store.Config()
	for i := range cfg.Services {
		svc := cfg.Services[i]
		if svc.ID == id {
			server, ok := cfg.ServerByID(svc.ServerID)
			if !ok {
				return "", errors.New(i18n.T("err.tunnelServerMissing"))
			}
			return configstore.PublicURL(server, svc), nil
		}
	}
	return "", errors.New(i18n.T("err.serviceNotFound"))
}

// TestServerConnection 独立测试已保存服务器的 FRPS 控制连通性。
// 与运行实例/期望态完全隔离：不启停隧道、不改配置、不影响服务器状态与统计；
// Success 仅代表 FRPS 控制连接与鉴权通过（不代表 HTTP 域名或业务端口可用）。
func (a *AppService) TestServerConnection(serverId string) (tunnel.ServerConnectionTestResult, error) {
	cfg := a.store.Config()
	server, ok := cfg.ServerByID(serverId)
	if !ok {
		return tunnel.ServerConnectionTestResult{}, errors.New(i18n.T("err.serverNotFound"))
	}
	res := tunnel.TestServerConnection(server)
	slog.Info("[前端] 服务器连通性探测完成",
		"id", serverId, "name", server.Name, "success", res.Success, "latencyMs", res.LatencyMs)
	return res, nil
}

// QRCode 生成访问地址的二维码 PNG（base64 data URL），供手机扫码直接访问。
func (a *AppService) QRCode(url string) (string, error) {
	png, err := qrcode.Encode(url, qrcode.Medium, 512)
	if err != nil {
		return "", err
	}
	return "data:image/png;base64," + base64.StdEncoding.EncodeToString(png), nil
}
