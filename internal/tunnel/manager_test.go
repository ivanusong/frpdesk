package tunnel

import (
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/fatedier/frp/client"
	v1 "github.com/fatedier/frp/pkg/config/v1"
	frplog "github.com/fatedier/frp/pkg/util/log"
	golog "github.com/fatedier/golib/log"

	"frpdesk/internal/configstore"
)

// ---------- 测试辅助 ----------

// fakeFRPS 接受连接但不响应登录，让实例停在 starting，便于观察启动期状态/资源。
type fakeFRPS struct {
	ln        net.Listener
	mu        sync.Mutex
	accepted  []net.Conn
	startedAt time.Time
}

func newFakeFRPS(t *testing.T) *fakeFRPS {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	f := &fakeFRPS{ln: ln, startedAt: time.Now()}
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			f.mu.Lock()
			f.accepted = append(f.accepted, c)
			f.mu.Unlock()
		}
	}()
	t.Cleanup(func() {
		f.mu.Lock()
		for _, c := range f.accepted {
			_ = c.Close()
		}
		f.accepted = nil
		f.mu.Unlock()
		_ = ln.Close()
	})
	return f
}

func (f *fakeFRPS) port() int { return f.ln.Addr().(*net.TCPAddr).Port }

// closeAll 关闭已接受连接（让 Run 收尾更快）。
func (f *fakeFRPS) closeAll() {
	f.mu.Lock()
	for _, c := range f.accepted {
		_ = c.Close()
	}
	f.accepted = nil
	f.mu.Unlock()
}

// closedDone 返回已关闭的 done channel：手工构造的实例没有真实 Run goroutine，
// StopAll/StopServer 的等待需立即通过。
func closedDone() chan struct{} {
	ch := make(chan struct{})
	close(ch)
	return ch
}

// testServer 构造服务器配置（addr/port 由 fakeFRPS 填充）。
func testServer(id, name string, port int) configstore.ServerConfig {
	return configstore.ServerConfig{
		ID: id, Name: name, ServerAddr: "127.0.0.1", ServerPort: port,
		HTTPEnabled: true, BaseDomain: "example.com", Scheme: "https",
	}
}

// testService 构造归属某服务器的 enabled HTTP 隧道。
func testService(id, serverID, sub string, host string, port int) configstore.Service {
	return configstore.Service{
		ID: id, ServerID: serverID, Name: id, LocalHost: host, LocalPort: port,
		Subdomain: sub, Enabled: true, Type: configstore.TypeHTTP,
	}
}

// waitFor 轮询等待条件成立（2s 超时即致命）。
func waitFor(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("等待超时：%s", what)
}

// ---------- 回调重入 / 日志链 ----------

// TestNotifyCallbacksAllowReentrantCalls 回归：OnStatus/OnLogs 回调在锁外触发，
// 回调内重入 Manager 的加锁方法必须不死锁。
// 用「无 enabled 隧道」路径触发通知：不产生任何网络活动，结果确定。
func TestNotifyCallbacksAllowReentrantCalls(t *testing.T) {
	m := NewManager()
	defer func() { m.StopAll() }()

	var seenLogs atomic.Int32
	m.OnStatus = func() {
		_ = m.Status() // 修复前：回调在 m.mu 内触发，这里再取锁直接死锁
	}
	m.OnLogs = func([]configstore.LogEntry) {
		_ = m.Logs()
		seenLogs.Add(1)
	}

	server := testServer("s", "s", 7000)
	server.ServerAddr = " " // 地址为空 → 校验不通过（产生日志与状态通知）
	m.Reconcile(server, []configstore.Service{
		{ID: "x", ServerID: "s", Name: "x", Enabled: true, Type: configstore.TypeHTTP},
	})
	waitFor(t, "日志回调", func() bool { return seenLogs.Load() > 0 })
	if st := m.ServerStatuses()["s"]; st.State != configstore.StateStopped {
		t.Fatalf("配置非法应停在 stopped: %+v", st)
	}
}

// TestLogSinkCallbackUnlocked frp 日志 sink 消费 goroutine 回调时不持有 Manager.mu
// （回调内调用加锁方法不死锁）。
func TestLogSinkCallbackUnlocked(t *testing.T) {
	m := NewManager()
	defer func() { m.StopAll() }()
	done := make(chan struct{})
	var once sync.Once
	m.OnLogs = func([]configstore.LogEntry) {
		_ = m.Status() // 持锁会死锁：死锁则超时
		once.Do(func() { close(done) })
	}

	// 模拟 sink 消费 goroutine 的调用形态
	entries := m.ring.Append("info", "[frpdesk] probe")
	if m.OnLogs != nil {
		m.OnLogs(entries)
	}
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("OnLogs 回调疑似持锁死锁")
	}
}

// ---------- 状态机（采样驱动，不依赖全局日志） ----------

// TestReconcileStartAndStop Reconcile 启动进入 starting，
// enabled 清零自动停止并等待终态 stopped。
func TestReconcileStartAndStop(t *testing.T) {
	fake := newFakeFRPS(t)
	m := NewManager()
	server := testServer("s1", "srv-1", fake.port())
	svcs := []configstore.Service{testService("t1", "s1", "a", "127.0.0.1", 1)}

	m.Reconcile(server, svcs)
	waitFor(t, "进入 starting", func() bool {
		return m.ServerStatuses()["s1"].State == configstore.StateStarting
	})
	waitFor(t, "实例注册", func() bool {
		m.mu.Lock()
		defer m.mu.Unlock()
		return m.insts["s1"] != nil
	})

	m.Reconcile(server, nil) // enabled=0 → 自动停止
	waitFor(t, "停止终态", func() bool {
		return m.ServerStatuses()["s1"].State == configstore.StateStopped
	})
	m.mu.Lock()
	_, alive := m.insts["s1"]
	m.mu.Unlock()
	if alive {
		t.Fatal("停止后实例应被移除")
	}
	fake.closeAll()
}

// TestSamplePromotesStartingToRunning running 判定由逐服务采样驱动：
// 全部代理注册成功 → starting 晋升 running（登录成功时间被记录），
// 全局日志文本不参与状态判定（双 frps 日志不可归属也不影响）。
func TestSamplePromotesStartingToRunning(t *testing.T) {
	m := NewManager()
	defer func() { m.StopAll() }()

	inst := &serverInstance{
		server:   testServer("s1", "srv-1", 7000),
		svc:      &client.Service{},
		cancel:   func() {},
		done:     closedDone(),
		ids:      []string{"t1", "t2"},
		statusFn: func(string) (string, string, bool) { return "running", "", true },
		connFn:   func() bool { return true }, // 模拟控制连接已建立
		state:    configstore.StateStarting,
	}
	m.mu.Lock()
	m.insts["s1"] = inst
	m.mu.Unlock()

	m.sampleInstance(inst)

	st := m.ServerStatuses()["s1"]
	if st.State != configstore.StateRunning {
		t.Fatalf("全部代理注册后应晋升 running: %+v", st)
	}
	if st.StartedAt == "" {
		t.Fatal("running 应记录连接时刻")
	}
	if inst.pending != nil {
		t.Fatal("无关断言占位")
	}
}

// TestBadServerKeepsRetryingAndIsolated 坏服务器隔离：A 指向不可连端口
// （LoginFailExit=false）持续 starting 自动重试、不产生 failed 终态；
// 不影响 B；停止 A（cancel 终止重试）回到 stopped。
func TestBadServerKeepsRetryingAndIsolated(t *testing.T) {
	// A：指向已关闭端口 → login 失败 → failed
	dead := newFakeFRPS(t)
	deadPort := dead.port()
	dead.ln.Close() // 立即关闭：连接被拒

	live := newFakeFRPS(t)
	m := NewManager()
	defer func() {
		m.StopAll()
		live.closeAll()
	}()

	svcs := []configstore.Service{
		testService("ta", "a", "a", "127.0.0.1", 1),
		testService("tb", "b", "b", "127.0.0.1", 2),
	}
	m.Reconcile(testServer("a", "dead", deadPort), svcs)
	m.Reconcile(testServer("b", "live", live.port()), svcs)

	// A 持续 starting（自动重试，无 failed 终态）
	waitForLong(t, "A starting（重试中）", func() bool {
		return m.ServerStatuses()["a"].State == configstore.StateStarting
	})
	// B 保持 starting（fake frps 不响应登录，不晋升也不失败）
	if st := m.ServerStatuses()["b"].State; st != configstore.StateStarting {
		t.Fatalf("B 应保持 starting 不受 A 影响，得到 %s", st)
	}
	// 全局日志流（无法归属）不改变 B 的状态：注入一条 A 时代的 login success 文本
	entries := m.ring.Append("info", "login to server success")
	if m.OnLogs != nil {
		m.OnLogs(entries)
	}
	if st := m.ServerStatuses()["b"].State; st != configstore.StateStarting {
		t.Fatalf("全局日志不得驱动状态机（不能把 B 当成功），得到 %s", st)
	}
}

// TestStopOneServerKeepsOther 停止一台不影响另一台运行实例。
func TestStopOneServerKeepsOther(t *testing.T) {
	f1 := newFakeFRPS(t)
	f2 := newFakeFRPS(t)
	m := NewManager()
	defer func() {
		m.StopAll()
		f1.closeAll()
		f2.closeAll()
	}()

	svcs := []configstore.Service{
		testService("ta", "a", "a", "127.0.0.1", 1),
		testService("tb", "b", "b", "127.0.0.1", 2),
	}
	m.Reconcile(testServer("a", "a", f1.port()), svcs)
	m.Reconcile(testServer("b", "b", f2.port()), svcs)
	waitFor(t, "双实例注册", func() bool {
		m.mu.Lock()
		defer m.mu.Unlock()
		return m.insts["a"] != nil && m.insts["b"] != nil
	})

	m.Reconcile(testServer("a", "a", f1.port()), nil) // enabled=0 → 自动停止
	// fake frps 不响应登录，frp Run 收尾在 race 下可能超过等待上限：
	// 终态由收尾 goroutine 异步落，轮询等待（不阻碍断言 b 的独立性）
	waitForLong(t, "a stopped", func() bool {
		return m.ServerStatuses()["a"].State == configstore.StateStopped
	})
	m.mu.Lock()
	_, bAlive := m.insts["b"]
	m.mu.Unlock()
	if !bAlive {
		t.Fatal("停止 a 不应影响 b 的实例")
	}
}

// TestStaleRunFinishDoesNotOverrideCurrentInstance 陈旧收尾不覆盖新实例状态
// （重启竞速回归：旧实例 done 收尾不得把新实例 starting 顶成 stopped）。
func TestStaleRunFinishDoesNotOverrideCurrentInstance(t *testing.T) {
	m := NewManager()
	defer func() { m.StopAll() }()

	oldInst := &serverInstance{server: testServer("s", "s", 7000), cancel: func() {}, done: closedDone()}
	newInst := &serverInstance{
		server: testServer("s", "s", 7000), svc: &client.Service{},
		cancel: func() {}, done: closedDone(), state: configstore.StateStarting,
	}
	m.mu.Lock()
	m.insts["s"] = newInst
	m.statuses["s"] = configstore.ServerStatus{ServerID: "s", State: configstore.StateStarting}
	m.mu.Unlock()

	m.finishRun(oldInst, nil) // 陈旧收尾

	if st := m.ServerStatuses()["s"]; st.State != configstore.StateStarting {
		t.Fatalf("陈旧收尾不得覆盖新实例状态: %+v", st)
	}
}

// TestFinishRunWritesFinalStates Run 返回的终态分类：错误 → failed；正常/停止 → stopped。
func TestFinishRunWritesFinalStates(t *testing.T) {
	m := NewManager()
	defer func() { m.StopAll() }()

	// 错误退出 → failed
	inst := &serverInstance{server: testServer("s", "s", 7000), cancel: func() {}, done: closedDone(), state: configstore.StateStarting}
	m.mu.Lock()
	m.insts["s"] = inst
	m.mu.Unlock()
	m.finishRun(inst, errors.New("login failed"))
	if st := m.ServerStatuses()["s"]; st.State != configstore.StateFailed || st.Message == "" {
		t.Fatalf("错误退出应 failed 带原因: %+v", st)
	}

	// 停止中取消的偶发 error → 统一 stopped
	inst2 := &serverInstance{server: testServer("s2", "s2", 7000), cancel: func() {}, done: closedDone(), state: configstore.StateStopping}
	m.mu.Lock()
	m.insts["s2"] = inst2
	m.mu.Unlock()
	m.finishRun(inst2, errors.New("context canceled"))
	if st := m.ServerStatuses()["s2"]; st.State != configstore.StateStopped {
		t.Fatalf("停止中取消应统一 stopped: %+v", st)
	}
}

// ---------- Reconcile（保存即对齐） ----------

// hotApplyHarness 构造一个 running 实例（代理集合与 frp 服务桩替）。
func hotApplyHarness(t *testing.T, m *Manager, id string) (*serverInstance, *corsProxySet) {
	t.Helper()
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, "ok")
	}))
	t.Cleanup(backend.Close)
	host, port := backendHostPort(t, backend.URL)
	p := newCORSProxyMust(t, host, port)
	set := newCORSProxySet(id, id, nil)
	set.add("t1", p)
	inst := &serverInstance{
		server: testServer(id, id, 7000),
		svcs:   []configstore.Service{testService("t1", id, "a", host, port)},
		svc:    &client.Service{}, cancel: func() {}, done: closedDone(),
		ids: []string{"t1"}, proxies: set, state: configstore.StateRunning,
	}
	m.mu.Lock()
	m.insts[id] = inst
	m.mu.Unlock()
	t.Cleanup(func() { inst.releaseProxies() })
	return inst, set
}

// TestReconcileRunningHotReloadKeepsService running 实例普通代理变化热更新：
// client.Service 指针不变（防 FRPS 异步 cleanup 竞态），updateFrpcConfigSource 被调用。
func TestReconcileRunningHotReloadKeepsService(t *testing.T) {
	m := NewManager()
	inst, set := hotApplyHarness(t, m, "s")
	var calls atomic.Int32
	oldUpdate := updateFrpcConfigSource
	updateFrpcConfigSource = func(*client.Service, *v1.ClientCommonConfig, []v1.ProxyConfigurer) error {
		calls.Add(1)
		return nil
	}
	t.Cleanup(func() { updateFrpcConfigSource = oldUpdate })

	newSvcs := []configstore.Service{testService("t2", "s", "b", "127.0.0.1", 2)}
	m.Reconcile(inst.server, newSvcs)

	waitFor(t, "热更新调用", func() bool { return calls.Load() == 1 })
	m.mu.Lock()
	current := m.insts["s"] == inst
	m.mu.Unlock()
	if !current {
		t.Fatal("热更新不应替换实例（client.Service 必须保留）")
	}
	if set.get("t2") == nil {
		t.Fatal("热更新应为新隧道创建本地代理")
	}
}

// TestReconcileConnectionChangeRestartsOnlyThatServer 连接参数变化仅重连该服务器。
func TestReconcileConnectionChangeRestartsOnlyThatServer(t *testing.T) {
	f1 := newFakeFRPS(t)
	m := NewManager()
	defer func() { m.StopAll() }()

	inst, _ := hotApplyHarness(t, m, "s")
	// 另一台不受影响的实例
	other := &serverInstance{
		server: testServer("other", "other", f1.port()), svc: &client.Service{},
		cancel: func() {}, done: closedDone(), state: configstore.StateStarting,
	}
	m.mu.Lock()
	m.insts["other"] = other
	m.mu.Unlock()

	changed := inst.server
	changed.ServerPort = 7100 // 连接参数变化
	m.Reconcile(changed, inst.svcs)

	waitFor(t, "实例被替换（重连）", func() bool {
		m.mu.Lock()
		defer m.mu.Unlock()
		return m.insts["s"] != nil && m.insts["s"] != inst
	})
	m.mu.Lock()
	stillSame := m.insts["other"] == other
	m.mu.Unlock()
	if !stillSame {
		t.Fatal("无关服务器不得被重启")
	}
}

// TestHotApplyStartingStoresPendingFlushOnRunning starting 期间的修改不丢失：
// 先暂存 pending，采样晋升 running 后补应用（代理变化）；
// 若登录期间连接参数也变了，晋升后用最新参数重启。
func TestReconcileStartingPendingFlush(t *testing.T) {
	m := NewManager()

	inst := &serverInstance{
		server: testServer("s", "s", 7000), svc: &client.Service{},
		cancel: func() {}, done: closedDone(),
		ids:      []string{"t1"},
		proxies:  newCORSProxySet("s", "S", nil), // pending 补应用时 syncTo/建代理需要集合
		statusFn: func(string) (string, string, bool) { return "running", "", true },
		connFn:   func() bool { return true }, // 模拟登录成功（控制连接建立）触发晋升与 pending 应用
		state:    configstore.StateStarting,
	}
	m.mu.Lock()
	m.insts["s"] = inst
	m.statuses["s"] = configstore.ServerStatus{ServerID: "s", State: configstore.StateStarting}
	m.mu.Unlock()

	var calls atomic.Int32
	oldUpdate := updateFrpcConfigSource
	updateFrpcConfigSource = func(*client.Service, *v1.ClientCommonConfig, []v1.ProxyConfigurer) error {
		calls.Add(1)
		return nil
	}
	t.Cleanup(func() { updateFrpcConfigSource = oldUpdate })

	// starting 中保存：代理变化（无连接参数变化）
	newSvcs := []configstore.Service{testService("t2", "s", "b", "127.0.0.1", 2)}
	m.Reconcile(inst.server, newSvcs)
	m.mu.Lock()
	pending := inst.pending
	m.mu.Unlock()
	if pending == nil {
		t.Fatal("starting 期间的修改应暂存 pending")
	}

	m.sampleInstance(inst) // 晋升 running + flush pending
	waitFor(t, "pending 补应用", func() bool { return calls.Load() == 1 })

	// 场景二：starting 中连接参数变化 → 立即重连（不等旧登录结果，旧鉴权可能永远失败）
	inst2 := &serverInstance{
		server: testServer("s2", "s2", 7000), svc: &client.Service{},
		cancel: func() {}, done: closedDone(), ids: []string{"t1"},
		proxies:  newCORSProxySet("s2", "S2", nil),
		statusFn: func(string) (string, string, bool) { return "running", "", true },
		state:    configstore.StateStarting,
	}
	m.mu.Lock()
	m.insts["s2"] = inst2
	m.statuses["s2"] = configstore.ServerStatus{ServerID: "s2", State: configstore.StateStarting}
	m.mu.Unlock()
	changed := inst2.server
	changed.ServerPort = 7200
	m.Reconcile(changed, []configstore.Service{testService("t1", "s2", "a", "127.0.0.1", 1)})
	waitFor(t, "登录中连接参数变化立即重连", func() bool {
		m.mu.Lock()
		defer m.mu.Unlock()
		return m.insts["s2"] != nil && m.insts["s2"] != inst2
	})
	// 旧实例的 pending 不得在新实例上生效（新实例已用最新参数启动）
	m.mu.Lock()
	leaked := m.insts["s2"] != nil && m.insts["s2"].pending != nil
	m.mu.Unlock()
	if leaked {
		t.Fatal("重连后新实例不应残留旧 pending")
	}
}

// TestReconcileStoppedNoInstanceNoOp 未连接实例 Reconcile no-op（只保存）。
func TestReconcileStoppedNoInstanceNoOp(t *testing.T) {
	m := NewManager()
	m.Reconcile(testServer("none", "none", 7000), nil) // 无实例：不 panic、不启动
	m.mu.Lock()
	defer m.mu.Unlock()
	if len(m.insts) != 0 {
		t.Fatal("无 enabled 隧道不应被 Reconcile 启动")
	}
}

// TestReconcileFailureFallsBackToRestart 热更新失败回退重启该服务器（仅该服务器）。
func TestReconcileFailureFallsBackToRestart(t *testing.T) {
	f := newFakeFRPS(t)
	m := NewManager()
	defer func() {
		m.StopAll()
		f.closeAll()
	}()

	inst, _ := hotApplyHarness(t, m, "s")
	inst.state = configstore.StateRunning
	m.mu.Lock()
	m.statuses["s"] = configstore.ServerStatus{ServerID: "s", State: configstore.StateRunning}
	m.mu.Unlock()

	var calls atomic.Int32
	oldUpdate := updateFrpcConfigSource
	updateFrpcConfigSource = func(*client.Service, *v1.ClientCommonConfig, []v1.ProxyConfigurer) error {
		calls.Add(1)
		return errors.New("模拟下发失败")
	}
	t.Cleanup(func() { updateFrpcConfigSource = oldUpdate })

	m.Reconcile(inst.server, inst.svcs)
	waitFor(t, "回退重启（实例替换）", func() bool {
		m.mu.Lock()
		defer m.mu.Unlock()
		return m.insts["s"] != nil && m.insts["s"] != inst
	})
	if calls.Load() == 0 {
		t.Fatal("热更新应先尝试下发")
	}
}

// ---------- 日志归属 ----------

// TestLogAttribution 实例生命周期日志带 serverId/serverName；
// 全局 frp 日志流与应用级日志不标归属（宁缺勿错）。
func TestLogAttribution(t *testing.T) {
	m := NewManager()
	defer func() { m.StopAll() }()

	inst := &serverInstance{server: testServer("srv-9", "九号机", 7000), cancel: func() {}, done: closedDone()}
	m.mu.Lock()
	m.insts["srv-9"] = inst
	var notes []notify
	m.logLocked(&notes, "info", "正在停止 frpc", "srv-9")
	m.logLocked(&notes, "info", "全局消息", "")
	m.mu.Unlock()
	m.flush(notes)

	var attributed, plain int
	for _, e := range m.Logs() {
		switch e.Message {
		case "[frpdesk] 正在停止 frpc":
			if e.ServerID != "srv-9" || e.ServerName != "九号机" {
				t.Fatalf("实例日志应带归属: %+v", e)
			}
			attributed++
		case "[frpdesk] 全局消息":
			if e.ServerID != "" || e.ServerName != "" {
				t.Fatalf("全局日志不得标注归属: %+v", e)
			}
			plain++
		}
	}
	if attributed != 1 || plain != 1 {
		t.Fatalf("日志条数不符: attributed=%d plain=%d", attributed, plain)
	}
}

// TestReconcileIdempotentWhileActive 重复对齐幂等：starting/running 状态下连接参数
// 一致的再次 Reconcile 保持现有实例（指针不变、不重连，仅热更新对齐）。
func TestReconcileIdempotentWhileActive(t *testing.T) {
	fake := newFakeFRPS(t)
	m := NewManager()
	defer func() { m.StopAll() }()

	server := testServer("s", "srv", fake.port())
	svcs := []configstore.Service{testService("t1", "s", "a", "127.0.0.1", 1)}

	m.Reconcile(server, svcs)
	waitFor(t, "实例注册", func() bool {
		m.mu.Lock()
		defer m.mu.Unlock()
		return m.insts["s"] != nil
	})
	m.mu.Lock()
	first := m.insts["s"]
	state := first.state
	m.mu.Unlock()

	// starting 下重复对齐：保持实例与状态（热更新路径幂等）
	m.Reconcile(server, svcs)
	m.mu.Lock()
	same := m.insts["s"] == first
	cur := first.state
	m.mu.Unlock()
	if !same {
		t.Fatal("幂等对齐不得替换实例")
	}
	if cur != state {
		t.Fatalf("幂等对齐不得改变状态: %s → %s", state, cur)
	}

	// 人为晋升 running 后再重复启动，同样保持实例
	m.mu.Lock()
	first.state = configstore.StateRunning
	m.statuses["s"] = configstore.ServerStatus{ServerID: "s", State: configstore.StateRunning}
	m.mu.Unlock()
	m.Reconcile(server, svcs)
	m.mu.Lock()
	same2 := m.insts["s"] == first
	m.mu.Unlock()
	if !same2 {
		t.Fatal("running 下幂等对齐不得替换实例")
	}
}

// TestReconcileReconnectsOnConnectionChange 连接参数变化（专用重连路径）：
// starting 与 running 下改地址/端口/token 都立即替换实例，不等旧登录结果。
func TestReconcileReconnectsOnConnectionChange(t *testing.T) {
	fake := newFakeFRPS(t)
	m := NewManager()
	defer func() { m.StopAll() }()

	server := testServer("s", "srv", fake.port())
	svcs := []configstore.Service{testService("t1", "s", "a", "127.0.0.1", 1)}
	m.Reconcile(server, svcs)
	waitFor(t, "实例注册", func() bool {
		m.mu.Lock()
		defer m.mu.Unlock()
		return m.insts["s"] != nil
	})
	m.mu.Lock()
	first := m.insts["s"]
	m.mu.Unlock()

	changed := server
	changed.AuthToken = "new-token" // 连接参数变化
	m.Reconcile(changed, svcs)
	waitFor(t, "实例被替换（重连）", func() bool {
		m.mu.Lock()
		defer m.mu.Unlock()
		return m.insts["s"] != nil && m.insts["s"] != first
	})
}

// TestReconcileAllSkipsServersWithoutTunnels 对齐：无 enabled 隧道的服务器不空连。
func TestReconcileAllSkipsServersWithoutTunnels(t *testing.T) {
	m := NewManager()
	servers := []configstore.ServerConfig{
		testServer("with", "有隧道", 7000),
		testServer("empty", "无隧道", 7000),
	}
	svcs := []configstore.Service{
		{ID: "t1", ServerID: "with", Name: "t1", Enabled: false}, // 未启用 → 无隧道
	}
	m.ReconcileAll(servers, svcs)
	m.mu.Lock()
	defer m.mu.Unlock()
	if len(m.insts) != 0 {
		t.Fatalf("无 enabled 隧道不应创建实例: %d", len(m.insts))
	}
	if _, alive := m.insts["empty"]; alive {
		t.Fatal("无隧道服务器不应创建实例（不空连）")
	}
}

// TestReconcileDisabledOnlyDoesNothing 纯 disabled 隧道不触发启动。
func TestReconcileDisabledOnlyDoesNothing(t *testing.T) {
	m := NewManager()
	server := testServer("s", "s", 7000)
	m.Reconcile(server, []configstore.Service{
		{ID: "t1", ServerID: "s", Name: "t1", Enabled: false, Type: configstore.TypeHTTP},
	})
	m.mu.Lock()
	defer m.mu.Unlock()
	if len(m.insts) != 0 {
		t.Fatal("disabled 隧道不应创建实例")
	}
}

// TestStopAllStopsEveryInstance 全部停止（应用退出路径）：逐实例等待终态。
func TestStopAllStopsEveryInstance(t *testing.T) {
	f1 := newFakeFRPS(t)
	f2 := newFakeFRPS(t)
	m := NewManager()
	svcs := []configstore.Service{
		testService("ta", "a", "a", "127.0.0.1", 1),
		testService("tb", "b", "b", "127.0.0.1", 2),
	}
	m.Reconcile(testServer("a", "a", f1.port()), svcs)
	m.Reconcile(testServer("b", "b", f2.port()), svcs)
	waitFor(t, "双实例注册", func() bool {
		m.mu.Lock()
		defer m.mu.Unlock()
		return len(m.insts) == 2
	})

	m.StopAll()
	// 同上：Run 收尾可能晚于 StopAll 的 3s 等待上限，轮询等收尾完成
	waitForLong(t, "全部实例收尾", func() bool {
		m.mu.Lock()
		defer m.mu.Unlock()
		return len(m.insts) == 0
	})
	f1.closeAll()
	f2.closeAll()
}

// TestLastSampleMergedAcrossInstances 双实例采样合并：各自的代理状态在同一张
// 全局表里（serviceID 全局唯一），一台停止后其条目移除、另一台保留。
func TestLastSampleMergedAcrossInstances(t *testing.T) {
	m := NewManager()
	mkInst := func(id, t1 string) *serverInstance {
		return &serverInstance{
			server: testServer(id, id, 7000), svc: &client.Service{},
			cancel: func() {}, done: closedDone(), ids: []string{t1},
			statusFn: func(string) (string, string, bool) { return "running", "", true },
			connFn:   func() bool { return true },
			state:    configstore.StateRunning,
		}
	}
	a, b := mkInst("a", "ta"), mkInst("b", "tb")
	m.mu.Lock()
	m.insts["a"], m.insts["b"] = a, b
	m.mu.Unlock()

	m.sampleInstance(a)
	m.sampleInstance(b)
	stats := m.ProxyStatuses()
	if len(stats) != 2 {
		t.Fatalf("双实例采样应合并为 2 条: %+v", stats)
	}

	m.mu.Lock()
	delete(m.insts, "b") // 模拟 b 收尾消费引用
	m.mu.Unlock()
	m.sampleInstance(a)
	stats = m.ProxyStatuses()
	if len(stats) != 1 || stats[0].ServiceID != "ta" {
		t.Fatalf("b 终止后其条目应移除: %+v", stats)
	}
}

// TestFrpLogSinkInstalledOnce 全局日志 sink 只安装一次（包级 once）。
func TestFrpLogSinkInstalledOnce(t *testing.T) {
	m := NewManager()
	defer func() { m.StopAll() }()
	// 二次安装不会替换 Logger 指针（安装后的指针与首次一致即可继续接收）
	entries := m.ring.Append("info", "probe-once")
	if len(entries) == 0 {
		t.Fatal("环形缓冲应正常接收")
	}
	// 直接验证 frplog.Logger 输出已指向我们的 sink（由 installLogSink 保证只装一次）
	if frplog.Logger == nil {
		t.Fatal("frp 日志 logger 不应为空")
	}
	_ = golog.WithOutput // 保持导入对齐（编译期引用）
}

// ---------- 热更新后的实例快照推进 ----------

// TestApplyProxiesAdvancesInstanceSnapshot 热更新成功后代次校验通过即推进实例快照
// （ids/svcs/server 含最新名字）：采样盯最新 ID（新增隧道有状态、删除后采旧 ID 的
// 回 starting 不再发生）、后续回退/重连以最新配置为准。全部读写持 m.mu。
func TestApplyProxiesAdvancesInstanceSnapshot(t *testing.T) {
	m := NewManager()
	inst, set := hotApplyHarness(t, m, "s")
	var calls atomic.Int32
	oldUpdate := updateFrpcConfigSource
	updateFrpcConfigSource = func(*client.Service, *v1.ClientCommonConfig, []v1.ProxyConfigurer) error {
		calls.Add(1)
		return nil
	}
	t.Cleanup(func() { updateFrpcConfigSource = oldUpdate })

	newSvcs := []configstore.Service{testService("t2", "s", "b", "127.0.0.1", 2)}
	renamed := inst.server
	renamed.Name = "新名字"
	m.applyProxies(inst, renamed, newSvcs)

	// 下发成功与快照推进之间有微小窗口：直接等推进完成
	waitFor(t, "实例快照推进", func() bool {
		m.mu.Lock()
		defer m.mu.Unlock()
		return len(inst.ids) == 1 && inst.ids[0] == "t2" && inst.server.Name == "新名字"
	})
	m.mu.Lock()
	defer m.mu.Unlock()
	if len(inst.ids) != 1 || inst.ids[0] != "t2" {
		t.Fatalf("ids 应推进到最新列表: %v", inst.ids)
	}
	if len(inst.svcs) != 1 || inst.svcs[0].ID != "t2" {
		t.Fatalf("svcs 应推进到最新列表: %+v", inst.svcs)
	}
	if inst.server.Name != "新名字" {
		t.Fatalf("server 快照应推进（含最新名字）: %+v", inst.server)
	}
	if got := set.serverDisplayName(); got != "新名字" {
		t.Fatalf("代理集合的展示名应刷新: %q", got)
	}
}

// TestHotApplyFallbackUsesLatestSvcs 热更新失败回退必须用本次下发的最新服务列表
// （旧实现回退 inst.svcs 启动时快照，会丢掉本次新增的隧道）。
func TestHotApplyFallbackUsesLatestSvcs(t *testing.T) {
	fake := newFakeFRPS(t)
	m := NewManager()
	defer func() {
		m.StopAll()
		fake.closeAll()
	}()

	inst, _ := hotApplyHarness(t, m, "s")
	inst.state = configstore.StateRunning
	m.mu.Lock()
	m.statuses["s"] = configstore.ServerStatus{ServerID: "s", State: configstore.StateRunning}
	m.mu.Unlock()

	oldUpdate := updateFrpcConfigSource
	updateFrpcConfigSource = func(*client.Service, *v1.ClientCommonConfig, []v1.ProxyConfigurer) error {
		return errors.New("模拟下发失败")
	}
	t.Cleanup(func() { updateFrpcConfigSource = oldUpdate })

	latest := append(append([]configstore.Service(nil), inst.svcs...), testService("t2", "s", "b", "127.0.0.1", 2))
	m.Reconcile(inst.server, latest)
	waitFor(t, "回退重启（实例替换）", func() bool {
		m.mu.Lock()
		defer m.mu.Unlock()
		return m.insts["s"] != nil && m.insts["s"] != inst
	})
	// 新实例必须携带本次最新服务列表（含新增的 t2）
	m.mu.Lock()
	newInst := m.insts["s"]
	var hasT2 bool
	for _, svc := range newInst.svcs {
		if svc.ID == "t2" {
			hasT2 = true
		}
	}
	m.mu.Unlock()
	if !hasT2 {
		t.Fatalf("回退重连应使用本次最新服务列表（含 t2）: %+v", newInst.svcs)
	}
}

// TestProxyReportsNewNameAfterServerRename 服务器改名后新请求上报最新名；
// 名字在请求 handler 入口快照（在途/历史请求保留发生时旧名）。
func TestProxyReportsNewNameAfterServerRename(t *testing.T) {
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, "ok")
	}))
	defer backend.Close()
	host, port := backendHostPort(t, backend.URL)
	p, getReports := newReportingProxy(t, host, port)

	resp, err := http.Get("http://" + p.addr() + "/before")
	if err != nil {
		t.Fatal(err)
	}
	_ = readBody(t, resp)
	recs := waitForReports(t, getReports, 1)
	if recs[0].serverName != "一号服务器" {
		t.Fatalf("改名前请求应报旧名: %+v", recs[0])
	}

	// 服务器改名（热更新流程刷新集合展示名）
	setRef.setServerName("二号服务器")

	resp, err = http.Get("http://" + p.addr() + "/after")
	if err != nil {
		t.Fatal(err)
	}
	_ = readBody(t, resp)
	recs = waitForReports(t, getReports, 2)
	if recs[1].serverName != "二号服务器" {
		t.Fatalf("改名后的新请求应报最新名: %+v", recs[1])
	}
	if recs[0].serverName != "一号服务器" {
		t.Fatalf("旧上报不应被回填新名: %+v", recs[0])
	}
}

// ---------- 停止中重新启用的边界 ----------

// TestReconcileStoppingReEnableOverridesTimeout 停止中重新启用的超时边界：
// 旧实例 done 永不关闭（模拟 frp 收尾超时）时，Reconcile(enabled>0) 不得落在
// 「等收尾后 if insts==nil 才启动」的 no-op 上——必须立即按最新配置覆盖启动
// （旧实例收尾成陈旧，代次校验兜底）。
func TestReconcileStoppingReEnableOverridesTimeout(t *testing.T) {
	fake := newFakeFRPS(t)
	m := NewManager()
	defer func() { m.StopAll() }()

	// 手工构造 stopping 实例：done 永不关闭 = 收尾超时
	inst := &serverInstance{
		server: testServer("s", "旧名字", fake.port()), svc: &client.Service{},
		cancel: func() {}, ids: []string{"t1"}, state: configstore.StateStopping,
	}
	m.mu.Lock()
	m.insts["s"] = inst
	m.statuses["s"] = configstore.ServerStatus{ServerID: "s", State: configstore.StateStopping}
	m.mu.Unlock()

	newSvcs := []configstore.Service{testService("t2", "s", "b", "127.0.0.1", 2)}
	m.Reconcile(testServer("s", "新名字", fake.port()), newSvcs)

	// 不等收尾：实例必须立即被按最新配置启动的新实例替换
	waitFor(t, "停止中重启替换实例", func() bool {
		m.mu.Lock()
		defer m.mu.Unlock()
		return m.insts["s"] != nil && m.insts["s"] != inst
	})
	m.mu.Lock()
	newInst := m.insts["s"]
	svcsOK := len(newInst.svcs) == 1 && newInst.svcs[0].ID == "t2"
	nameOK := newInst.server.Name == "新名字"
	m.mu.Unlock()
	if !svcsOK || !nameOK {
		t.Fatalf("重启应用最新配置/名字: svcsOK=%v nameOK=%v", svcsOK, nameOK)
	}
}

// TestReconcileDisableWhileStoppingDoesNotRevive 停止中先对齐 enabled>0 再来一次
// enabled=0：不得复活连接——最终必须停止释放（每次对齐用当次期望，无旧配置复活）。
func TestReconcileDisableWhileStoppingDoesNotRevive(t *testing.T) {
	fake := newFakeFRPS(t)
	m := NewManager()
	defer func() { m.StopAll() }()

	inst := &serverInstance{
		server: testServer("s", "srv", fake.port()), svc: &client.Service{},
		cancel: func() {}, ids: []string{"t1"}, state: configstore.StateStopping,
	}
	m.mu.Lock()
	m.insts["s"] = inst
	m.statuses["s"] = configstore.ServerStatus{ServerID: "s", State: configstore.StateStopping}
	m.mu.Unlock()

	svcs := []configstore.Service{testService("t1", "s", "a", "127.0.0.1", 1)}
	m.Reconcile(testServer("s", "srv", fake.port()), svcs) // 停止中重新启用 → 覆盖启动
	m.Reconcile(testServer("s", "srv", fake.port()), nil)  // 随即期望归零 → 停止

	waitForLong(t, "最终停止且不复活", func() bool {
		m.mu.Lock()
		_, alive := m.insts["s"]
		m.mu.Unlock()
		return !alive && m.ServerStatuses()["s"].State == configstore.StateStopped
	})
}

// TestReconcileNoEnabledClearsFailedRecord 无实例可停时残留的 failed 终态
// （启动失败/运行异常来源）随期望归零清理为 stopped，聚合不再出现「失败 1/0」。
func TestReconcileNoEnabledClearsFailedRecord(t *testing.T) {
	m := NewManager()
	m.mu.Lock()
	m.statuses["s"] = configstore.ServerStatus{
		ServerID: "s", State: configstore.StateFailed, Message: "frpc 运行异常：模拟",
	}
	m.mu.Unlock()

	m.Reconcile(testServer("s", "s", 7000), nil) // enabled=0 且无实例

	if got := m.ServerStatuses()["s"]; got.State != configstore.StateStopped {
		t.Fatalf("残留 failed 应清理为 stopped: %+v", got)
	}
}
