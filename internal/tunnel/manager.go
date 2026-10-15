// Package tunnel 把 frp 客户端以库方式嵌入运行：
// 参考 frp v0.68.1 cmd/frpc/sub 的 RunClient 流程，直接构造
// pkg/config/v1 的配置 struct，经 source.Aggregator 交给 client.Service，
// 在 goroutine 中 Run，日志经 golib logger 输出接管进内存环形缓冲。
//
// 多实例模型：每台配置的 frps 服务器对应一个 serverInstance（独立的
// client.Service 与本地代理集合），可同时连接、独立启停；逐服务状态采样
// 驱动逐代理状态，控制连接状态驱动单实例状态机，不依赖无法归属实例的
// frp 全局日志文本（双 frps 同时登录时日志不可分辨归属）。
package tunnel

import (
	"context"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"github.com/fatedier/frp/client"
	"github.com/fatedier/frp/pkg/config/source"
	v1 "github.com/fatedier/frp/pkg/config/v1"
	"github.com/samber/lo"

	"frpdesk/internal/configstore"
)

// stopWaitTimeout Stop/重启等待 frpc Run 收尾的最长时间。
const stopWaitTimeout = 3 * time.Second

// serverInstance 单台服务器的运行实例（由 Reconcile 的期望态驱动创建/释放）。
type serverInstance struct {
	server     configstore.ServerConfig // 当前生效的连接配置（启动/热更新成功后推进）
	svcs       []configstore.Service    // 当前生效的 enabled 服务列表（启动/热更新成功后推进）
	svc        *client.Service
	cancel     context.CancelFunc
	done       chan struct{}
	ids        []string      // 当前生效的 enabled 服务 ID（frp proxy 名后缀；随热更新推进）
	statusFn   samplingFn    // 逐服务状态采样（测试可桩替）
	connFn     func() bool   // 控制连接状态探测（生产绑定 svc.IsConnected，测试可桩替）
	proxies    *corsProxySet // 本实例独占的本地代理资源（serverName 可随改名刷新）
	lastSample []ProxyStatus // 最近一次逐服务采样（全局状态表重建用）
	// 状态机（仅 Manager.mu 内读写）：
	state     configstore.TunnelState
	message   string
	startedAt time.Time
	// pending starting 期间收到的最新配置：登录完成（running）后锁外补应用，
	// 保证修改不因发生在登录中而丢失
	pending *pendingApply
}

// pendingApply starting 期间暂存的应用请求。
type pendingApply struct {
	server configstore.ServerConfig
	svcs   []configstore.Service
}

// releaseProxies 释放本实例的跨域代理资源（幂等；不触碰其他实例的资源）。
func (inst *serverInstance) releaseProxies() {
	if inst.proxies != nil {
		inst.proxies.Close()
	}
}

// Manager 多服务器隧道生命周期管理器。
//
// 回调契约：OnStatus/OnLogs/OnProxyStatus 一律在 Manager 内部锁（mu、ring.mu）
// 与 frp 日志锁（golib outMu）之外触发。宿主回调里可以安全调用本管理器的
// 加锁方法，不存在持锁重入死锁。
type Manager struct {
	mu sync.Mutex
	// insts 存活实例（serverID → 实例）；statuses 最近已知状态
	//（含 failed 终态与 stopped 记录：删除实例不丢「上次失败」信息）
	insts     map[string]*serverInstance
	statuses  map[string]configstore.ServerStatus
	ring      *LogRing
	proxyStat map[string]ProxyStatus // 逐服务代理状态（全局 serviceID → 状态，serviceID 唯一）

	// 回调由宿主（Wails App）注入；仅在锁外调用。
	// OnStatus 为纯信号（多实例聚合状态需配置数据，由宿主组装后推送）。
	OnStatus      func()
	OnLogs        func(newEntries []configstore.LogEntry)
	OnProxyStatus func(stats []ProxyStatus)

	// RequestReporter 每请求统计上报（enabled HTTP 服务的本地代理挂载点触发）。
	// nil = 不上报。Reconcile 启动时读取并传给本实例的代理集合。
	RequestReporter RequestReporter
}

// notify 锁内收集的一次通知：状态与/或日志/代理变化。
type notify struct {
	logs         []configstore.LogEntry
	status       bool // 实例状态已变化（宿主需重新拉取聚合快照）
	proxyChanged bool // 逐服务代理状态已变化（宿主拉取全量推送）
}

// NewManager 创建管理器，并把 frp 全局日志接入本实例的环形缓冲。
// frp 日志链是全局单例（installLogSink sync.Once），进程内只支持一个 Manager 实例；
// 全局日志流无法归属单台服务器（不做归属标注），实例状态机也不依赖它。
func NewManager() *Manager {
	m := &Manager{
		insts:     map[string]*serverInstance{},
		statuses:  map[string]configstore.ServerStatus{},
		ring:      NewLogRing(500),
		proxyStat: map[string]ProxyStatus{},
	}
	// frp 日志从 sink 的消费 goroutine 到达，已脱离 golib outMu；
	// ring.Append 自带锁并在返回后释放，因此 OnLogs 回调时无任何内部锁被持有。
	installLogSink(func(level, message string) {
		entries := m.ring.Append(level, message)
		if m.OnLogs != nil {
			m.OnLogs(entries)
		}
	})
	return m
}

// Status 聚合状态：由已知实例状态派生（totalCount 由宿主按配置补齐后推送）。
// 可在回调内安全调用（无锁重入风险）。
func (m *Manager) Status() configstore.TunnelStatus {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.aggregateLocked()
}

// aggregateLocked 由 statuses 派生聚合状态（totalCount 记为已知服务器数，
// 宿主组装 Snapshot 时以配置为准重算）。
func (m *Manager) aggregateLocked() configstore.TunnelStatus {
	list := make([]configstore.ServerStatus, 0, len(m.statuses))
	for _, st := range m.statuses {
		list = append(list, st)
	}
	return configstore.AggregateStatus(list, len(m.statuses))
}

// ServerStatuses 返回全部已知服务器的状态（含 failed 终态/stopped 记录；
// 配置中存在但从未启动过的服务器由宿主补 stopped）。
func (m *Manager) ServerStatuses() map[string]configstore.ServerStatus {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make(map[string]configstore.ServerStatus, len(m.statuses))
	for id, st := range m.statuses {
		out[id] = st
	}
	return out
}

// Logs 返回全部缓冲日志。
func (m *Manager) Logs() []configstore.LogEntry {
	return m.ring.All()
}

// AppendLog 写入应用级日志（与实例状态机无关，如更新流程）。
// 只追加日志，不推进任何实例状态机；统一带 [frpdesk] 前缀，与 frp 库原生日志区分。
func (m *Manager) AppendLog(level, message string) {
	entries := m.ring.Append(level, "[frpdesk] "+message)
	if m.OnLogs != nil {
		m.OnLogs(entries)
	}
}

// Reconcile 把单台服务器对齐到期望态（唯一生命周期入口，期望完全由
// Service.Enabled 驱动，服务器无独立开关）：
//   - enabledCount > 0：必须连接/维持连接——无实例启动；连接参数（地址/端口/token）
//     变化仅重连自身；参数不变则热更新代理（保留 client.Service，防 FRPS 异步
//     cleanup 触发的 proxy already exists 竞态）；停止中则按最新配置替换启动
//     （期望是连接，不得落在停止 no-op 上）；
//   - enabledCount == 0：停止并释放实例（无实例则 no-op；disabled 隧道不触发启动）。
//
// 登录失败不产生终态：buildCommon 显式 LoginFailExit=false，frp 内部自动重试
// （keepControllerWorking backoff），实例停在 starting「正在连接」直到连上或被
// 停止——失败/等待连接不影响其它实例；停止/退出经 cancel 终止重试。
func (m *Manager) Reconcile(server configstore.ServerConfig, svcs []configstore.Service) {
	enabled := enabledServices(svcs, server.ID)

	m.mu.Lock()
	inst := m.insts[server.ID]

	// 期望无连接：停止并释放实例（finishRun 落 stopped 终态）
	if len(enabled) == 0 {
		if inst == nil {
			// 无实例可停，但启动失败/运行异常留下的 failed 终态记录要随期望
			// 归零一并清理，否则聚合出现「失败 1/0」（totalCount=0）
			if st := m.statuses[server.ID]; st.State == configstore.StateFailed {
				m.statuses[server.ID] = configstore.ServerStatus{ServerID: server.ID, State: configstore.StateStopped}
				var notes []notify
				m.logLocked(&notes, "info", "该服务器已无启用的隧道，失败状态已清理", server.ID)
				m.mu.Unlock()
				m.flush(notes)
				return
			}
			m.mu.Unlock()
			return // 无实例且无隧道：no-op
		}
		var notes []notify
		m.logLocked(&notes, "info", "该服务器已无启用的隧道，停止连接", server.ID)
		m.stopInstanceLocked(&notes, inst)
		m.mu.Unlock()
		m.flush(notes)
		m.waitInstanceDone(inst)
		return
	}

	// 期望连接：配置校验（非法则记录原因不启动；修正配置后再次 reconcile 即可启动）
	if msg := configstore.ValidateServerStart(server, svcs); msg != "" {
		m.statuses[server.ID] = configstore.ServerStatus{ServerID: server.ID, State: configstore.StateStopped, Message: msg}
		var notes []notify
		m.logLocked(&notes, "info", msg, "")
		m.mu.Unlock()
		m.flush(notes)
		return
	}

	// 连接参数变化 → 专用重连路径（停止中同样适用：cancel 幂等；launch 覆盖
	// insts 引用后旧实例收尾成陈旧，不会覆盖新实例状态）
	if inst != nil && connectionChanged(inst.server, server) {
		var notes []notify
		m.logLocked(&notes, "info",
			fmt.Sprintf("服务器连接参数变化，重连 frps %s:%d", server.ServerAddr, server.ServerPort), server.ID)
		st := m.restartLocked(&notes, server, enabled)
		m.mu.Unlock()
		m.flush(notes)
		_ = st
		return
	}

	// 停止中：期望仍是连接 → 直接按最新配置覆盖启动，不等待收尾、不落 no-op
	//（旧实现「等待后 if insts==nil 才启动」在收尾超时时 ref 仍占位 → 完全 no-op，
	// 期望态卡死）。cancel 已在停止时发出（幂等）；launch 覆盖 insts 引用后，
	// 旧实例收尾成陈旧（代次校验兜底），与连接参数变化的重连同一路径。
	// 随后若又来一次 enabled=0 的对齐，会对新实例再次停止——每次对齐用的
	// 都是当次期望，不存在复活旧配置。
	if inst != nil && inst.state == configstore.StateStopping {
		var notes []notify
		m.logLocked(&notes, "info", "服务器停止中，直接按最新配置重新连接", server.ID)
		st := m.restartLocked(&notes, server, enabled)
		m.mu.Unlock()
		m.flush(notes)
		_ = st
		return
	}

	// starting：登录未完成，代理变化暂存 pending，连接成功后补应用
	//（不依赖 frp 重连后是否自动 reload 配置源；连接参数变化已在上方立即重连）
	if inst != nil && inst.state == configstore.StateStarting {
		inst.pending = &pendingApply{server: server, svcs: enabled}
		var notes []notify
		m.logLocked(&notes, "info", "服务器登录中，配置变更将在连接后应用", server.ID)
		m.mu.Unlock()
		m.flush(notes)
		return
	}

	// running 且参数一致 → 热更新对齐到最新代理列表（幂等；保留 client.Service）
	if inst != nil {
		m.mu.Unlock()
		m.applyProxies(inst, server, enabled)
		return
	}

	// 无实例 → 自动启动
	var notes []notify
	m.logLocked(&notes, "info",
		fmt.Sprintf("正在连接 frps %s:%d（%s，启用 %d 个代理）",
			server.ServerAddr, server.ServerPort, server.Name, len(enabled)), server.ID)
	st := m.restartLocked(&notes, server, enabled)
	m.mu.Unlock()
	m.flush(notes)
	_ = st
}

// waitInstanceDone 等待实例 Run 收尾（最长 stopWaitTimeout；超时只记日志，
// 终态由收尾 goroutine 异步落账）。实例字段在锁内快照后再等待：
// inst.server 会被 applyProxies 的快照推进在锁内整体赋值，禁止锁外读。
func (m *Manager) waitInstanceDone(inst *serverInstance) {
	m.mu.Lock()
	name, id := inst.server.Name, inst.server.ID
	m.mu.Unlock()
	select {
	case <-inst.done:
	case <-time.After(stopWaitTimeout):
		slog.Warn("[Stop] 等待实例收尾超时", "server", name, "id", id)
	}
}

// ReconcileAll 对全部配置服务器做一次对齐（应用启动初始化用）；
// 无 enabled 隧道的服务器不空连（Reconcile 内 no-op）。
func (m *Manager) ReconcileAll(servers []configstore.ServerConfig, services []configstore.Service) {
	byID := servicesByServer(services)
	for _, server := range servers {
		m.Reconcile(server, byID[server.ID])
	}
}

// restartLocked 停旧实例（若有）并立即用给定配置启动新实例。
// 必须持有 m.mu，通知暂存 notes；旧实例 cancel 由其收尾清理
// （launch 覆盖 insts 引用后旧收尾成陈旧，代次校验防覆盖新实例状态）。
func (m *Manager) restartLocked(notes *[]notify, server configstore.ServerConfig, enabled []configstore.Service) configstore.ServerStatus {
	if old := m.insts[server.ID]; old != nil {
		m.stopInstanceLocked(notes, old)
	}
	st := configstore.ServerStatus{ServerID: server.ID, State: configstore.StateStarting, Message: "正在登录 frps"}
	m.statuses[server.ID] = st
	if err := m.launchLocked(notes, server, enabled); err != nil {
		st = configstore.ServerStatus{ServerID: server.ID, State: configstore.StateFailed,
			Message: configstore.BriefMessage(err.Error(), 120)}
		m.statuses[server.ID] = st
		m.logLocked(notes, "error", err.Error(), server.ID)
	}
	return st
}

// forceRestart 无条件强制重连（绕过任何期望态判断）：热更新下发失败回退用——
// 连接配置未变但 client 侧必须重建，且必须用本次最新服务列表。
func (m *Manager) forceRestart(server configstore.ServerConfig, svcs []configstore.Service) configstore.ServerStatus {
	enabled := enabledServices(svcs, server.ID)
	m.mu.Lock()
	var notes []notify
	m.logLocked(&notes, "info", "热更新失败，强制重连该服务器", server.ID)
	st := m.restartLocked(&notes, server, enabled)
	m.mu.Unlock()
	m.flush(notes)
	return st
}

// StopAll 停止全部实例并等待收尾（应用退出/服务关闭用；终止 frp 自动重试）。
func (m *Manager) StopAll() {
	m.mu.Lock()
	var notes []notify
	insts := make([]*serverInstance, 0, len(m.insts))
	for _, inst := range m.insts {
		m.stopInstanceLocked(&notes, inst)
		insts = append(insts, inst)
	}
	m.mu.Unlock()
	m.flush(notes)

	for _, inst := range insts {
		m.waitInstanceDone(inst)
	}
}

// servicesByServer 按 serverID 分组服务列表。
func servicesByServer(services []configstore.Service) map[string][]configstore.Service {
	out := map[string][]configstore.Service{}
	for _, svc := range services {
		out[svc.ServerID] = append(out[svc.ServerID], svc)
	}
	return out
}

// enabledServices 过滤某服务器的 enabled 隧道（归属悬空的服务不属于任何服务器）。
func enabledServices(svcs []configstore.Service, serverID string) []configstore.Service {
	out := make([]configstore.Service, 0, len(svcs))
	for _, svc := range svcs {
		if svc.ServerID == serverID && svc.Enabled {
			out = append(out, svc)
		}
	}
	return out
}

// connectionChanged 连接参数（地址/端口/token）是否变化：变化必须重连。
func connectionChanged(a, b configstore.ServerConfig) bool {
	return a.ServerAddr != b.ServerAddr || a.ServerPort != b.ServerPort || a.AuthToken != b.AuthToken
}

// launchLocked 构造并启动单台实例（锁内；通知暂存 notes）。
func (m *Manager) launchLocked(notes *[]notify, server configstore.ServerConfig, enabled []configstore.Service) error {
	svc, proxies, err := buildService(server, enabled, m.RequestReporter)
	if err != nil {
		return err
	}
	ctx, cancel := context.WithCancel(context.Background())
	inst := &serverInstance{
		server:   server,
		svcs:     enabled,
		svc:      svc,
		cancel:   cancel,
		done:     make(chan struct{}),
		ids:      serviceIDs(enabled),
		statusFn: frpStatusFunc(svc),
		connFn:   svc.IsConnected, // 真实控制连接状态（fork 新增；测试可桩替）
		proxies:  proxies,
		state:    configstore.StateStarting,
	}
	m.insts[server.ID] = inst
	if m.clearInstanceProxiesLocked(inst.ids) {
		*notes = append(*notes, notify{proxyChanged: true}) // 新实例接管：旧代次逐服务状态移除
	}

	// 逐服务状态采样：仅在实例存活期间轮询（2s 周期）；running 判定由采样驱动
	go func() {
		ticker := time.NewTicker(2 * time.Second)
		defer ticker.Stop()
		for {
			select {
			case <-inst.done:
				return
			case <-ticker.C:
				m.sampleInstance(inst)
			}
		}
	}()

	// Run 是阻塞调用：LoginFailExit=false（buildCommon 显式设置）时 login 失败
	// 由 frp 内部自动重试、Run 不返回，直到 cancel 取消或运行错误。
	// panic 必须拦截，不允许带崩 GUI。done 在收尾完成后关闭，供停止路径等待终态。
	go func() {
		defer close(inst.done)
		defer func() {
			if r := recover(); r != nil {
				m.finishRun(inst, fmt.Errorf("frpc 内部 panic：%v", r))
			}
		}()
		m.finishRun(inst, svc.Run(ctx))
	}()
	return nil
}

// serviceIDs 提取 enabled 服务 ID 列表（保序）。
func serviceIDs(svcs []configstore.Service) []string {
	ids := make([]string, 0, len(svcs))
	for _, svc := range svcs {
		ids = append(ids, svc.ID)
	}
	return ids
}

// stopInstanceLocked 停止单实例：置过渡态并取消 Run（收尾 goroutine 写终态）。
// 必须持有 m.mu。
func (m *Manager) stopInstanceLocked(notes *[]notify, inst *serverInstance) {
	m.transitionLocked(notes, inst, configstore.TunnelStatus{State: configstore.StateStopping, Message: "停止中"})
	m.logLocked(notes, "info", "正在停止 frpc", inst.server.ID)
	inst.cancel()
	inst.releaseProxies() // 立即释放本实例代理；Run 收尾时幂等再释放一次
}

// sampleInstance 对单实例做一次逐服务状态采样（锁内判定，通知锁外 flush）。
// 代次校验：实例已被替换/停止则丢弃本次结果。
//
// 连接状态判定走真实控制连接（frp Service.IsConnected，ctlMu 安全；不依赖全局
// 日志，也不从代理注册记录推断）：
//   - starting：控制连接建立（登录成功）→ 晋升 running；单代理注册失败
//     （start error）不影响连接主态，失败独立展示在逐服务状态，pending 照常应用；
//   - running：控制连接断开（frp 自动重连中）→ 回退 starting「正在重连」，
//     重连成功后再次晋升。
func (m *Manager) sampleInstance(inst *serverInstance) {
	m.mu.Lock()
	if m.insts[inst.server.ID] != inst {
		m.mu.Unlock()
		return
	}
	stats := sampleAll(inst.svc, append([]string(nil), inst.ids...), inst.statusFn)
	var notes []notify
	promoted := false
	connected := inst.connFn != nil && inst.connFn()
	switch inst.state {
	case configstore.StateStarting:
		if connected {
			m.transitionLocked(&notes, inst, configstore.TunnelStatus{
				State:     configstore.StateRunning,
				Message:   "已连接",
				StartedAt: time.Now().Format(time.RFC3339),
			})
			inst.startedAt = time.Now()
			promoted = true
		}
	case configstore.StateRunning:
		if !connected {
			m.transitionLocked(&notes, inst, configstore.TunnelStatus{
				State:   configstore.StateStarting,
				Message: "连接断开，正在重连",
			})
		}
	}
	m.mergeInstanceProxiesLocked(inst, stats, &notes)
	m.mu.Unlock()

	// 采样变化（含未晋升的普通轮询）也要通知；晋升 running 后再处理 pending
	m.flush(notes)
	if !promoted {
		return
	}
	// 连接成功后补应用登录期间的代理修改（锁外，代次校验在 applyProxies 内）。
	// 连接参数变化不走 pending：Reconcile 已立即重连（不依赖旧登录结果）
	m.mu.Lock()
	pending := inst.pending
	inst.pending = nil
	m.mu.Unlock()
	if pending != nil {
		slog.Info("[frpdesk] 登录期间有配置变更，连接后补应用", "server", pending.server.Name)
		m.applyProxies(inst, pending.server, pending.svcs)
	}
}

// mergeInstanceProxiesLocked 把单实例采样合并进全局逐服务状态表；
// 实例已消失（陈旧采样）则以当前实例表为准重建，变化时通知。
// 必须持有 m.mu。
func (m *Manager) mergeInstanceProxiesLocked(inst *serverInstance, stats []ProxyStatus, notes *[]notify) {
	if m.insts[inst.server.ID] != inst {
		return // 实例已被替换/终止：陈旧采样丢弃（终态清理在 finishRun）
	}
	merged := map[string]ProxyStatus{}
	for id, inst2 := range m.insts {
		if id == inst.server.ID {
			continue
		}
		for _, st := range inst2.lastSample {
			merged[st.ServiceID] = st
		}
	}
	for _, st := range stats {
		merged[st.ServiceID] = st
	}
	inst.lastSample = stats
	if proxyStatusEqual(proxyStatuses(m.proxyStat), proxyStatuses(merged)) {
		return
	}
	m.proxyStat = merged
	*notes = append(*notes, notify{proxyChanged: true})
}

// clearInstanceProxiesLocked 从全局逐服务状态表移除指定服务（实例重启/收尾时代次残留清理）。
// 返回是否发生变化（通知判定用）。
func (m *Manager) clearInstanceProxiesLocked(ids []string) bool {
	if len(ids) == 0 || len(m.proxyStat) == 0 {
		return false
	}
	drop := make(map[string]bool, len(ids))
	for _, id := range ids {
		drop[id] = true
	}
	merged := map[string]ProxyStatus{}
	for sid, st := range m.proxyStat {
		if !drop[sid] {
			merged[sid] = st
		}
	}
	if len(merged) == len(m.proxyStat) {
		return false
	}
	m.proxyStat = merged
	return true
}

// ProxyStatuses 返回当前逐服务代理状态快照（供绑定拉取）。
func (m *Manager) ProxyStatuses() []ProxyStatus {
	m.mu.Lock()
	defer m.mu.Unlock()
	return proxyStatuses(m.proxyStat)
}

// applyProxies 锁外执行单实例代理热更新：先把本地代理集合同步到目标态
// （先移除后补建，同时刷新集合的服务器展示名），再生成新代理配置经
// frp UpdateConfigSource 下发；成功且代次校验通过后把实例快照（ids/svcs/server）
// 推进到本次最新配置——采样盯最新 ID、后续重连与失败回退都以最新为准。
// 代次校验：期间被 Stop/重启抢先则丢弃结果只记日志，不覆盖新实例状态。
func (m *Manager) applyProxies(inst *serverInstance, server configstore.ServerConfig, svcs []configstore.Service) {
	enabled := enabledServices(svcs, server.ID)
	inst.proxies.setServerName(server.Name) // 服务器改名后新请求上报最新名（在途请求已捕获开始时名字）
	if err := inst.proxies.syncTo(enabled); err != nil {
		m.hotApplyFailed(inst, server, enabled, err)
		return
	}
	proxyCfgs, err := buildProxyCfgs(enabled, inst.proxies)
	if err != nil {
		m.hotApplyFailed(inst, server, enabled, err)
		return
	}
	if err := updateFrpcConfigSource(inst.svc, buildCommon(server), proxyCfgs); err != nil {
		m.hotApplyFailed(inst, server, enabled, err)
		return
	}
	m.mu.Lock()
	// inst.server 在本函数锁外阶段不可读（推进时会整体赋值）：代次与日志
	// 一律用参数 server（值拷贝）
	current := m.insts[server.ID] == inst
	var notes []notify
	if current {
		// 快照推进：采样盯最新 ID 列表、后续回退/重连用最新配置与名字。
		// 必须持锁写（与 sampleInstance/Reconcile 的读互斥）。
		inst.ids = serviceIDs(enabled)
		inst.svcs = enabled
		inst.server = server
		m.logLocked(&notes, "info", fmt.Sprintf("配置已热更新（%d 个代理）", len(enabled)), server.ID)
	}
	m.mu.Unlock()
	if current {
		m.flush(notes)
	} else {
		slog.Info("[frpdesk] 配置热更新完成，但实例已被替换，丢弃本次结果", "server", server.Name)
	}
}

// hotApplyFailed 热更新失败的兜底：仅当实例仍是当前且状态 running 才强制重连
// 该服务器，并且必须用本次下发的最新服务列表（不是实例启动时的旧快照，
// 否则本次新增的隧道丢失）；实例已被抢先则只记日志。
func (m *Manager) hotApplyFailed(inst *serverInstance, server configstore.ServerConfig, enabled []configstore.Service, err error) {
	m.mu.Lock()
	current := m.insts[inst.server.ID] == inst && inst.state == configstore.StateRunning
	var notes []notify
	if current {
		m.logLocked(&notes, "error", fmt.Sprintf("配置热更新失败，回退重启该服务器：%v", err), inst.server.ID)
	}
	m.mu.Unlock()
	if !current {
		slog.Error("[frpdesk] 配置热更新失败（实例已变更，跳过回退）", "server", server.Name, "err", err)
		return
	}
	m.flush(notes)
	slog.Error("[frpdesk] 配置热更新失败，回退重启该服务器", "server", server.Name, "err", err)
	m.forceRestart(server, enabled)
}

// finishRun 处理单实例 Run goroutine 的返回（独立加锁，锁外回调）。
// 按归属分类（与单实例版一致，多实例下按 serverID 比对）：
//   - m.insts[id] 是别的实例：期间发生过重启，本次是陈旧收尾，只记日志，
//     绝不能覆盖新实例状态
//   - m.insts[id] == inst：当前实例正常收尾，消费引用并写终态
func (m *Manager) finishRun(inst *serverInstance, err error) {
	inst.releaseProxies()

	m.mu.Lock()
	var notes []notify
	if cur := m.insts[inst.server.ID]; cur != nil && cur != inst {
		m.logLocked(&notes, "info", "旧 frpc 实例已退出（已被新实例替换）", inst.server.ID)
		m.mu.Unlock()
		m.flush(notes)
		return
	}
	delete(m.insts, inst.server.ID) // 消费引用
	if m.clearInstanceProxiesLocked(inst.ids) {
		notes = append(notes, notify{proxyChanged: true}) // 被停实例的逐服务状态过期
	}
	switch {
	case err == nil:
		// 正常退出：无条件写终态（不可加 state!=stopped 守卫——过渡态也是 stopped）
		m.transitionLocked(&notes, inst, configstore.TunnelStatus{State: configstore.StateStopped, Message: "frpc 已停止"})
	case inst.state == configstore.StateStopping || inst.state == configstore.StateStopped:
		// 主动停止/取消过程中 cancel 后 Run 返回 error 的偶发路径：终态统一「frpc 已停止」
		m.transitionLocked(&notes, inst, configstore.TunnelStatus{State: configstore.StateStopped, Message: "frpc 已停止"})
	default:
		m.transitionLocked(&notes, inst, configstore.TunnelStatus{
			State:   configstore.StateFailed,
			Message: configstore.BriefMessage(fmt.Sprintf("frpc 运行异常：%v", err), 120),
		})
	}
	m.mu.Unlock()
	m.flush(notes)
}

// transitionLocked 更新实例状态与服务器状态记录并登记通知（必须持有 m.mu）。
func (m *Manager) transitionLocked(notes *[]notify, inst *serverInstance, ts configstore.TunnelStatus) configstore.TunnelStatus {
	slog.Info("[状态机]", "server", inst.server.Name, "id", inst.server.ID, "state", ts.State, "message", ts.Message, "prev", inst.state)
	inst.state = ts.State
	inst.message = ts.Message
	st := configstore.ServerStatus{ServerID: inst.server.ID, State: ts.State, Message: ts.Message, StartedAt: ts.StartedAt}
	m.statuses[inst.server.ID] = st
	*notes = append(*notes, notify{status: true})
	return ts
}

// logLocked 写入环形缓冲并登记日志通知。
// serverID 非空时条目标注归属（实例生命周期日志）；空 = 全局/无法归属（frp 原生日志流）。
// 自身日志统一带 [frpdesk] 前缀。
func (m *Manager) logLocked(notes *[]notify, level, message, serverID string) {
	entry := "[frpdesk] " + message
	var entries []configstore.LogEntry
	if serverID != "" {
		name := ""
		if inst, ok := m.insts[serverID]; ok {
			name = inst.server.Name
		}
		// 实例已不存在时（收尾后补记）仍标 ID 不带名：宁缺名不错归属
		entries = m.ring.AppendServer(level, entry, serverID, name)
	} else {
		entries = m.ring.Append(level, entry)
	}
	*notes = append(*notes, notify{logs: entries})
}

// flush 在无锁状态下依序回放通知。仅允许在释放 mu/ring.mu 后调用。
func (m *Manager) flush(notes []notify) {
	var hasStatus, hasProxy bool
	for _, n := range notes {
		if len(n.logs) > 0 && m.OnLogs != nil {
			m.OnLogs(n.logs)
		}
		if n.status {
			hasStatus = true
		}
		if n.proxyChanged {
			hasProxy = true
		}
	}
	if hasStatus && m.OnStatus != nil {
		m.OnStatus()
	}
	if hasProxy && m.OnProxyStatus != nil {
		m.OnProxyStatus(m.ProxyStatuses())
	}
}

// buildService 构造单台服务器的 frp client.Service 与本地代理集合。
// 组合 buildProxyCfgs（代理配置 + 代理创建）与 buildFrpcService（frp 服务装配）。
// 构造任一步失败都会释放本次已创建的全部代理资源（返回的 proxies 为 nil）。
func buildService(server configstore.ServerConfig, enabled []configstore.Service, reporter RequestReporter) (*client.Service, *corsProxySet, error) {
	proxies := newCORSProxySet(server.ID, server.Name, reporter)
	svc, err := buildFrpcService(server, enabled, proxies)
	if err != nil {
		proxies.Close() // 回滚本次已创建的代理，不泄漏监听端口
		return nil, nil, err
	}
	return svc, proxies, nil
}

// buildCommon 构造 frp 客户端公共配置（从服务器配置转换；token 空串等价缺省）。
// LoginFailExit 显式 false：登录失败由 frp 内部自动重试（keepControllerWorking
// backoff），实例停留在 starting「正在连接」而不产生 failed 终态——坏服务器
// 不影响其它实例，停止/退出经 cancel 终止重试。
func buildCommon(server configstore.ServerConfig) *v1.ClientCommonConfig {
	common := &v1.ClientCommonConfig{
		ServerAddr: server.ServerAddr,
		ServerPort: server.ServerPort,
	}
	common.Auth.Method = v1.AuthMethodToken
	common.Auth.Token = server.AuthToken
	common.LoginFailExit = lo.ToPtr(false)
	return common
}

// buildProxyCfgs 生成 enabled 隧道的 frp 代理配置：服务顺序与字段均确定性赋值，
// 未变化服务两次生成的配置 DeepEqual 相等，frp 热更新按名 diff 才不会误杀重注册。
// enabled 的 HTTP 隧道（含旧数据缺省类型）：内置跨域+本地 Host 代理，集合中已有同 ID
// 代理则不重复创建，本地目标指向 loopback 代理；TCP/UDP 无条件直连原目标。
func buildProxyCfgs(svcs []configstore.Service, proxies *corsProxySet) ([]v1.ProxyConfigurer, error) {
	proxyCfgs := make([]v1.ProxyConfigurer, 0, len(svcs))
	for i := range svcs {
		svc := svcs[i]
		switch svc.Type {
		case configstore.TypeTCP:
			// tcp：frps 侧监听 RemotePort，流量原始字节直连本地目标（不经 HTTP 代理）
			proxyCfg := &v1.TCPProxyConfig{}
			proxyCfg.Name = "frpdesk-" + svc.ID
			proxyCfg.Type = "tcp"
			proxyCfg.LocalIP = svc.LocalHost
			proxyCfg.LocalPort = svc.LocalPort
			proxyCfg.RemotePort = svc.RemotePort
			proxyCfgs = append(proxyCfgs, proxyCfg)
		case configstore.TypeUDP:
			// udp：frps 侧监听 RemotePort，UDP 报文原始转发直连本地目标（不经 HTTP 代理）
			proxyCfg := &v1.UDPProxyConfig{}
			proxyCfg.Name = "frpdesk-" + svc.ID
			proxyCfg.Type = "udp"
			proxyCfg.LocalIP = svc.LocalHost
			proxyCfg.LocalPort = svc.LocalPort
			proxyCfg.RemotePort = svc.RemotePort
			proxyCfgs = append(proxyCfgs, proxyCfg)
		default:
			// http（含旧数据缺省类型）：默认经内置本地代理（跨域+本地 Host）
			if proxies.get(svc.ID) == nil {
				p, err := newCORSProxy(proxies, svc.LocalHost, svc.LocalPort, svc.ID)
				if err != nil {
					return nil, fmt.Errorf("服务 %s 创建本地代理失败: %w", svc.Name, err)
				}
				proxies.add(svc.ID, p)
				slog.Info("[代理] HTTP 服务已接入本地代理（内置跨域+本地 Host）",
					"server", proxies.serverID, "service", svc.Name, "id", svc.ID, "listen", p.addr(), "target", p.target)
			}
			localHost, localPort := proxies.localAddr(svc)
			proxyCfg := &v1.HTTPProxyConfig{}
			proxyCfg.Name = "frpdesk-" + svc.ID
			proxyCfg.Type = "http"
			proxyCfg.LocalIP = localHost
			proxyCfg.LocalPort = localPort
			proxyCfg.SubDomain = svc.Subdomain
			proxyCfgs = append(proxyCfgs, proxyCfg)
		}
	}
	return proxyCfgs, nil
}

// buildFrpcService 组装单台服务器的 frp 配置并创建 client.Service。
func buildFrpcService(server configstore.ServerConfig, enabled []configstore.Service, proxies *corsProxySet) (*client.Service, error) {
	proxyCfgs, err := buildProxyCfgs(enabled, proxies)
	if err != nil {
		return nil, err
	}

	configSource := source.NewConfigSource()
	if err := configSource.ReplaceAll(proxyCfgs, nil); err != nil {
		return nil, fmt.Errorf("注册代理配置失败: %w", err)
	}
	aggregator := source.NewAggregator(configSource)

	svc, err := client.NewService(client.ServiceOptions{
		Common:                 buildCommon(server),
		ConfigSourceAggregator: aggregator,
	})
	if err != nil {
		return nil, fmt.Errorf("创建 frpc 服务失败: %w", err)
	}
	return svc, nil
}

// updateFrpcConfigSource 经 frp 热更新通道下发新配置（UpdateConfigSource 不断线，
// 按代理名 diff：同名且配置相等不动、变化/删除优雅注销、新增注册）。
// 抽成包级变量便于测试桩替模拟失败回退（对齐 listenCORSProxy 的做法）。
var updateFrpcConfigSource = func(svc *client.Service, common *v1.ClientCommonConfig, proxyCfgs []v1.ProxyConfigurer) error {
	return svc.UpdateConfigSource(common, proxyCfgs, nil)
}
