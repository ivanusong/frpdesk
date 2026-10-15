package tunnel

import (
	"strings"
	"testing"
	"time"

	"github.com/fatedier/frp/client"
	golog "github.com/fatedier/golib/log"

	"frpdesk/internal/configstore"
)

// stubStatusFn 构造可变的逐服务状态桩。
type stubPhase struct {
	phase string
	err   string
}

func stubStatusFn(states map[string]stubPhase) samplingFn {
	return func(id string) (string, string, bool) {
		s, ok := states[id]
		if !ok {
			return "", "", false
		}
		return s.phase, s.err, true
	}
}

// mkSamplingInstance 构造挂到指定服务器 ID 下的最小采样实例。
func mkSamplingInstance(m *Manager, serverID, svcID string) *serverInstance {
	inst := &serverInstance{
		server: configstore.ServerConfig{ID: serverID, Name: serverID},
		svc:    &client.Service{},
		cancel: func() {},
		done:   closedDone(),
		ids:    []string{svcID},
		connFn: func() bool { return true },
		state:  configstore.StateRunning,
	}
	m.mu.Lock()
	m.insts[serverID] = inst
	m.mu.Unlock()
	return inst
}

// TestSampleProxyStatusFailureAndRecovery 失败→恢复：
// 端口占用注册失败（friendly 中文 + 原始错误）→ frp 恢复 running 后回到 registered。
func TestSampleProxyStatusFailureAndRecovery(t *testing.T) {
	m := NewManager()
	t.Cleanup(func() { m.StopAll() })

	inst := mkSamplingInstance(m, "s", "svc1")
	var last [][]ProxyStatus
	m.OnProxyStatus = func(stats []ProxyStatus) { last = append(last, stats) }

	// 采样1：注册失败（端口占用）
	inst.statusFn = stubStatusFn(map[string]stubPhase{
		"frpdesk-svc1": {phase: "start error", err: "listen tcp: bind: address already in use"},
	})
	m.sampleInstance(inst)

	if len(last) == 0 {
		t.Fatal("采样1未推送")
	}
	st := last[len(last)-1]
	if len(st) != 1 || st[0].State != proxyStateFailed {
		t.Fatalf("采样1应为 failed: %+v", st)
	}
	if st[0].Friendly != "端口已被占用" {
		t.Fatalf("失败原因应为友好中文: %+v", st[0])
	}
	if !strings.Contains(st[0].RawErr, "address already in use") {
		t.Fatalf("原始错误应保留: %+v", st[0])
	}

	// 采样2：frp 恢复 running
	inst.statusFn = stubStatusFn(map[string]stubPhase{"frpdesk-svc1": {phase: "running"}})
	m.sampleInstance(inst)

	if len(last) < 2 {
		t.Fatal("采样2未推送")
	}
	st2 := last[len(last)-1]
	if len(st2) != 1 || st2[0].State != proxyStateRegistered || st2[0].RawErr != "" || st2[0].Friendly != "" {
		t.Fatalf("恢复后应为 registered 且清空错误: %+v", st2[0])
	}
}

// TestFinishRunClearsProxyStatus 实例收尾后旧采样过期：
// 该实例的逐服务状态被移除并推送（前端恢复「未启动」徽章）；其他实例不受影响。
func TestFinishRunClearsProxyStatus(t *testing.T) {
	m := NewManager()
	inst := mkSamplingInstance(m, "gone", "svc-gone")
	inst.lastSample = []ProxyStatus{{ServiceID: "svc-gone", State: proxyStateRegistered}}
	other := mkSamplingInstance(m, "keep", "svc-keep")
	other.lastSample = []ProxyStatus{{ServiceID: "svc-keep", State: proxyStateRegistered}}
	m.mu.Lock()
	m.proxyStat = map[string]ProxyStatus{
		"svc-gone": {ServiceID: "svc-gone", State: proxyStateRegistered},
		"svc-keep": {ServiceID: "svc-keep", State: proxyStateRegistered},
	}
	m.mu.Unlock()

	var cleared atomicBoolFlag
	m.OnProxyStatus = func(stats []ProxyStatus) {
		if len(stats) == 1 && stats[0].ServiceID == "svc-keep" {
			cleared.set()
		}
	}

	m.finishRun(inst, nil)

	if got := m.ProxyStatuses(); len(got) != 1 || got[0].ServiceID != "svc-keep" {
		t.Fatalf("收尾后应只剩其他实例的状态: %+v", got)
	}
	if !cleared.get() {
		t.Fatal("收尾应推送剩余状态（被停实例条目已清）")
	}
}

// atomicBoolFlag 并发安全的布尔标志（测试用）。
type atomicBoolFlag struct {
	mu    chan struct{}
	value bool
}

func (f *atomicBoolFlag) set() {
	if f.mu == nil {
		f.mu = make(chan struct{}, 1)
	}
	f.mu <- struct{}{}
	f.value = true
	<-f.mu
}

func (f *atomicBoolFlag) get() bool {
	if f.mu == nil {
		f.mu = make(chan struct{}, 1)
	}
	f.mu <- struct{}{}
	v := f.value
	<-f.mu
	return v
}

// TestStaleSampleIgnored 旧实例采样不得覆盖新实例状态。
func TestStaleSampleIgnored(t *testing.T) {
	m := NewManager()
	t.Cleanup(func() { m.StopAll() })

	oldInst := &serverInstance{
		server: configstore.ServerConfig{ID: "s", Name: "s"}, svc: &client.Service{},
		cancel: func() {}, done: closedDone(), ids: []string{"old"}, connFn: func() bool { return true }, state: configstore.StateRunning,
	}
	newInst := &serverInstance{
		server: configstore.ServerConfig{ID: "s", Name: "s"}, svc: &client.Service{},
		cancel: func() {}, done: closedDone(), ids: []string{"new"}, connFn: func() bool { return true }, state: configstore.StateRunning,
	}

	m.mu.Lock()
	m.insts["s"] = newInst // 发生重启：当前实例变为 newInst
	m.proxyStat = map[string]ProxyStatus{"new": {ServiceID: "new", State: proxyStateRegistered}}
	newInst.lastSample = []ProxyStatus{{ServiceID: "new", State: proxyStateRegistered}}
	m.mu.Unlock()

	// 旧实例采样迟到：应被代次守卫丢弃（保留 new 的状态，不回退 old）
	oldInst.lastSample = []ProxyStatus{{ServiceID: "old", State: proxyStateRegistered}}
	m.sampleInstance(oldInst)

	st := m.ProxyStatuses()
	if len(st) != 1 || st[0].ServiceID != "new" || st[0].State != proxyStateRegistered {
		t.Fatalf("旧实例采样迟到：应保留 new 状态: %+v", st)
	}
}

// TestProxyCallbackRunsOutsideLock 回调锁外安全：回调内再取 Manager 锁不死锁。
func TestProxyCallbackRunsOutsideLock(t *testing.T) {
	m := NewManager()
	inst := mkSamplingInstance(m, "s", "svc1")

	deadlock := make(chan struct{})
	inst.statusFn = func(string) (string, string, bool) { return "running", "", true }
	m.OnProxyStatus = func([]ProxyStatus) {
		_ = m.ProxyStatuses() // 回调内重入加锁：持锁调用会死锁
		close(deadlock)
	}

	go m.sampleInstance(inst)

	select {
	case <-deadlock:
	case <-time.After(2 * time.Second):
		t.Fatal("代理状态回调死锁（未在锁外调用）")
	}
}

// TestWarnLevelMapped WARN 级别修复回归：frp WARN 日志不再并入 info。
func TestWarnLevelMapped(t *testing.T) {
	got := make(chan string, 4)
	sink := newFrpLogSink(func(level, message string) { got <- level })

	sink.WriteLog([]byte("warn line"), golog.WarnLevel, time.Now())
	select {
	case lv := <-got:
		if lv != "warn" {
			t.Fatalf("WARN 应映射为 warn，得到 %q", lv)
		}
	case <-time.After(time.Second):
		t.Fatal("WARN 未到达（采样通道阻塞）")
	}
}

// TestSameMultiServiceNoRepeatPush 相同多服务采样不重复推送
// （map 遍历顺序随机，输出按 ServiceID 稳定排序后应判定无变化）。
func TestSameMultiServiceNoRepeatPush(t *testing.T) {
	m := NewManager()
	inst := &serverInstance{
		server: configstore.ServerConfig{ID: "s", Name: "s"}, svc: &client.Service{},
		done: make(chan struct{}), ids: []string{"a", "b"},
		statusFn: func(string) (string, string, bool) { return "running", "", true },
		connFn:   func() bool { return true },
		state:    configstore.StateRunning,
	}
	m.mu.Lock()
	m.insts["s"] = inst
	m.mu.Unlock()

	var pushes int
	m.OnProxyStatus = func(stats []ProxyStatus) { pushes++ }

	m.sampleInstance(inst) // 首次：推送
	first := m.ProxyStatuses()
	m.sampleInstance(inst) // 二次：内容相同 → 不推送
	if pushes != 1 {
		t.Fatalf("相同采样不应重复推送（实际 %d 次）", pushes)
	}
	if len(first) != 2 {
		t.Fatalf("应含 2 个服务: %+v", first)
	}
}

// TestSampleAllUnknownService 未注册服务（frps 无记录）映射为 unregistered。
func TestSampleAllUnknownService(t *testing.T) {
	svc := &client.Service{}
	out := sampleAll(svc, []string{"ghost"}, func(string) (string, string, bool) {
		return "", "", false
	})
	if len(out) != 1 || out[0].State != proxyStateUnregistered {
		t.Fatalf("未注册应映射 unregistered: %+v", out)
	}
}

// TestFriendlyProxyErrPrecise 友好映射精准性：permission denied 等不得误归为端口占用。
func TestFriendlyProxyErrPrecise(t *testing.T) {
	cases := []struct {
		raw  string
		want string
	}{
		{"port already used", "远程端口已被其他服务占用"},
		{"listen tcp: bind: address already in use", "端口已被占用"},
		{"port unavailable", "远程端口不可用"},
		{"port not allowed", "远程端口不在 frps 允许范围内"},
		{"permission denied", ""}, // 不得误归为占用
		{"totally unknown error", ""},
	}
	for _, c := range cases {
		if got := friendlyProxyErr(c.raw); got != c.want {
			t.Errorf("friendlyProxyErr(%q) = %q, want %q", c.raw, got, c.want)
		}
	}
}
