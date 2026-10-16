package main

import (
	"net"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"frpdesk/internal/configstore"
	"frpdesk/internal/i18n"
	"frpdesk/internal/stats"
	"frpdesk/internal/tunnel"
)

// newTestAppService 构造使用临时配置路径的 AppService（绝不触碰真实用户配置）。
func newTestAppService(t *testing.T) *AppService {
	t.Helper()
	svc := &AppService{
		store:  configstore.NewAtPath(filepath.Join(t.TempDir(), "config.json")),
		tunnel: tunnel.NewManager(),
		stats:  stats.NewCollector(),
	}
	svc.syncStatsMappings()
	t.Cleanup(func() { svc.tunnel.StopAll() })
	return svc
}

// waitForApp app 层轮询等待（5s 超时即致命）。
func waitForApp(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("等待超时：%s", what)
}

// TestBuildSnapshotMergesServerStatuses 快照组装：serverStatuses 按配置顺序输出、
// 从未启动过的服务器补 stopped、totalCount = 有 enabled 隧道的服务器数。
func TestBuildSnapshotMergesServerStatuses(t *testing.T) {
	svc := newTestAppService(t)

	sa, err := svc.store.CreateServer(configstore.ServerInput{
		Name: "甲", ServerAddr: "a.com", ServerPort: 7000, HTTPEnabled: true, BaseDomain: "a.com", Scheme: "https",
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := svc.store.CreateServer(configstore.ServerInput{
		Name: "乙", ServerAddr: "b.com", ServerPort: 7000, HTTPEnabled: true, BaseDomain: "b.com", Scheme: "https",
	}); err != nil {
		t.Fatal(err)
	}
	// 甲上有 enabled 隧道；乙无（不计数）
	if _, err := svc.store.CreateService(configstore.ServiceInput{
		ServerID: sa.Servers[0].ID, Name: "web", LocalPort: 8001, Subdomain: "web", Enabled: true, Type: "http",
	}); err != nil {
		t.Fatal(err)
	}

	snap := svc.buildSnapshot()
	if len(snap.ServerStatuses) != 2 {
		t.Fatalf("应含全部配置服务器: %+v", snap.ServerStatuses)
	}
	for _, st := range snap.ServerStatuses {
		if st.State != configstore.StateStopped {
			t.Fatalf("未启动过的服务器应补 stopped: %+v", st)
		}
	}
	if snap.Status.TotalCount != 1 {
		t.Fatalf("totalCount 应为有 enabled 隧道的服务器数 1: %+v", snap.Status)
	}
	if snap.Status.State != configstore.StateStopped {
		t.Fatalf("无实例应 stopped: %+v", snap.Status)
	}
	if snap.Config.Servers[0].Name != "甲" || snap.ServerStatuses[0].ServerID != snap.Config.Servers[0].ID {
		t.Fatal("serverStatuses 应与配置同序")
	}
}

// TestSummarizeFailures 失败摘要：无失败 nil；有失败时为双语错误——UI 走当前
// 语言（含服务器名与原因），日志通道（ErrText）固定英文且无本地化残留；
// 纯函数 summarizeFailedServers 直接用手工快照驱动（无需真实运行实例）。
func TestSummarizeFailures(t *testing.T) {
	svc := newTestAppService(t)
	if err := svc.summarizeFailures(); err != nil {
		t.Fatalf("无失败应返回 nil: %v", err)
	}

	t.Cleanup(func() { i18n.Set(i18n.ZH) })
	i18n.SetPreference(i18n.PrefZH)
	snap := configstore.Snapshot{
		Config: configstore.Config{Servers: []configstore.ServerConfig{{ID: "a", Name: "prod-1"}}},
		ServerStatuses: []configstore.ServerStatus{
			{ServerID: "a", State: configstore.StateFailed, Message: "注册代理配置失败", MessageEN: "Failed to register proxy configuration"},
			{ServerID: "b", State: configstore.StateRunning, Message: "已连接", MessageEN: "Connected"},
		},
	}
	err := summarizeFailedServers(snap)
	if err == nil {
		t.Fatal("有 failed 状态应返回错误")
	}
	if got := err.Error(); !strings.Contains(got, "1 台服务器启动失败") || !strings.Contains(got, "prod-1：注册代理配置失败") {
		t.Fatalf("UI 错误应为当前语言（含服务器名与原因）: %q", got)
	}
	if en := i18n.ErrText(err); en != "1 server(s) failed to start (prod-1: Failed to register proxy configuration)" {
		t.Fatalf("日志通道应为固定英文: %q", en)
	}
	// 服务器已删除（无归属名）时回退 ID；英文通道不得带入本地化状态文案
	orphan := summarizeFailedServers(configstore.Snapshot{
		ServerStatuses: []configstore.ServerStatus{{ServerID: "gone", State: configstore.StateFailed, Message: "启动失败"}},
	})
	if got := i18n.ErrText(orphan); got != "1 server(s) failed to start (gone: )" {
		t.Fatalf("无归属名应回退 ID 且英文通道不含中文: %q", got)
	}
}

// TestSetLanguagePersistsAndApplies 语言绑定：合法偏好落盘并即时应用进程语言；
// 返回值携带偏好 + 解析后生效语言；快照携带同一 resolved 语言（前端对齐契约）；
// 非法偏好拒绝且不影响当前语言/配置。
func TestSetLanguagePersistsAndApplies(t *testing.T) {
	t.Cleanup(func() { i18n.Set(i18n.ZH) })
	svc := newTestAppService(t)

	state, err := svc.SetLanguage(i18n.PrefEN)
	if err != nil {
		t.Fatalf("SetLanguage(en): %v", err)
	}
	if state.Preference != i18n.PrefEN || state.Resolved != string(i18n.EN) {
		t.Fatalf("SetLanguage(en) 返回契约不对: %+v", state)
	}
	if i18n.Current() != i18n.EN {
		t.Fatalf("进程语言应为 en，得到 %v", i18n.Current())
	}
	if got := svc.store.Language(); got != i18n.PrefEN {
		t.Fatalf("偏好应落盘为 en，得到 %q", got)
	}
	if snap := svc.Snapshot(); snap.Language != string(i18n.EN) || snap.Config.Language != i18n.PrefEN {
		t.Fatalf("快照应携带 resolved=后端生效语言: language=%q config.language=%q", snap.Language, snap.Config.Language)
	}
	if len(svc.Snapshot().Config.Services) != 0 {
		t.Fatal("切语言不应改动配置")
	}

	// system：偏好持久化为 system，resolved 为探测结果（合法语言）
	state, err = svc.SetLanguage(i18n.PrefSystem)
	if err != nil {
		t.Fatalf("SetLanguage(system): %v", err)
	}
	if state.Preference != i18n.PrefSystem || (state.Resolved != string(i18n.ZH) && state.Resolved != string(i18n.EN)) {
		t.Fatalf("system 应持久化偏好并返回合法 resolved: %+v", state)
	}
	if snap := svc.Snapshot(); snap.Language != state.Resolved {
		t.Fatalf("system 下快照 resolved 应与绑定返回一致: snap=%q state=%q", snap.Language, state.Resolved)
	}

	if _, err := svc.SetLanguage("fr"); err == nil {
		t.Fatal("非法偏好应返回错误")
	}
	if i18n.Current() != i18n.Lang(state.Resolved) || svc.store.Language() != i18n.PrefSystem {
		t.Fatalf("非法偏好不得改变状态: lang=%v pref=%q", i18n.Current(), svc.store.Language())
	}
}

// TestReorderServicesRPC 排序绑定：按给定顺序重排并落盘；缺 ID/重复/未知 ID 拒绝；
// 排序仅动配置数组顺序——不 reconcile，不触发任何实例对齐（状态保持 stopped）。
func TestReorderServicesRPC(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.json")
	svc := &AppService{
		store:  configstore.NewAtPath(path),
		tunnel: tunnel.NewManager(),
		stats:  stats.NewCollector(),
	}
	svc.syncStatsMappings()
	t.Cleanup(func() { svc.tunnel.StopAll() })

	sa, err := svc.store.CreateServer(configstore.ServerInput{
		Name: "甲", ServerAddr: "a.com", ServerPort: 7000, HTTPEnabled: true, BaseDomain: "a.com", Scheme: "https",
	})
	if err != nil {
		t.Fatal(err)
	}
	var ids []string
	for i, name := range []string{"web1", "web2", "web3"} {
		cfg, err := svc.store.CreateService(configstore.ServiceInput{
			ServerID: sa.Servers[0].ID, Name: name, LocalPort: 8001 + i, Subdomain: name, Enabled: true, Type: "http",
		})
		if err != nil {
			t.Fatal(err)
		}
		ids = append(ids, cfg.Services[len(cfg.Services)-1].ID)
	}

	// 成功：按给定顺序重排，返回值与落盘均为新序
	want := []string{ids[2], ids[0], ids[1]}
	snap, err := svc.ReorderServices(want)
	if err != nil {
		t.Fatalf("合法重排应成功: %v", err)
	}
	for i, id := range want {
		if snap.Config.Services[i].ID != id {
			t.Fatalf("快照顺序应为给定次序: %+v", snap.Config.Services)
		}
	}
	reloaded := configstore.NewAtPath(path)
	for i, id := range want {
		if reloaded.Config().Services[i].ID != id {
			t.Fatalf("重排应落盘（刷新/重启后保持）: %+v", reloaded.Config().Services)
		}
	}

	// 严格校验：缺 ID / 重复 / 未知 ID 拒绝，顺序不变
	for name, bad := range map[string][]string{
		"缺 ID":  {want[0], want[1]},
		"重复 ID": {want[0], want[0], want[1]},
		"未知 ID": {want[0], want[1], "ghost"},
	} {
		if _, err := svc.ReorderServices(bad); err == nil {
			t.Fatalf("%s 应拒绝", name)
		}
		for i, id := range want {
			if got := svc.Snapshot().Config.Services[i].ID; got != id {
				t.Fatalf("%s 拒绝后顺序不得变化: %v", name, got)
			}
		}
	}

	// 不 reconcile：无实例被拉起，聚合状态仍 stopped
	if got := svc.tunnel.ServerStatuses(); len(got) != 0 {
		t.Fatalf("排序不得触发实例对齐: %+v", got)
	}
	if st := svc.Snapshot().Status; st.State != configstore.StateStopped || st.ActiveCount != 0 {
		t.Fatalf("排序后聚合状态应保持 stopped: %+v", st)
	}
}

// TestDisableLastServiceAutoStops 禁用最后一个 enabled 隧道自动停止：
// 聚合回到 stopped 0/0（LoginFailExit=false 下坏服务器为 starting 重试中，
// 停止经 cancel 终止重试）。
func TestDisableLastServiceAutoStops(t *testing.T) {
	svc := newTestAppService(t)

	// 占住端口再关掉：得到确定连接失败的端口
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	port := ln.Addr().(*net.TCPAddr).Port
	_ = ln.Close()

	sv, err := svc.store.CreateServer(configstore.ServerInput{
		Name: "甲", ServerAddr: "127.0.0.1", ServerPort: port, HTTPEnabled: true, BaseDomain: "a.com",
	})
	if err != nil {
		t.Fatal(err)
	}
	serverID := sv.Servers[0].ID
	if _, err := svc.store.CreateService(configstore.ServiceInput{
		ServerID: serverID, Name: "web", LocalPort: 8001, Subdomain: "web", Enabled: true, Type: "http",
	}); err != nil {
		t.Fatal(err)
	}

	svc.reconcileAll()
	waitForApp(t, "进入 starting", func() bool {
		st := svc.Snapshot().Status
		return st.State == configstore.StateStarting && st.TotalCount == 1
	})

	// 禁用最后一个 enabled 隧道：reconcile 自动停止（cancel 终止 frp 重试）
	svc.SetServiceEnabled(svc.Snapshot().Config.Services[0].ID, false)

	waitForApp(t, "自动停止回 stopped 0/0", func() bool {
		st := svc.Snapshot().Status
		return st.State == configstore.StateStopped && st.FailedCount == 0 &&
			st.TotalCount == 0 && st.ActiveCount == 0 && st.RunningCount == 0
	})
	for _, st := range svc.Snapshot().ServerStatuses {
		if st.State != configstore.StateStopped {
			t.Fatalf("服务器状态应清理为 stopped: %+v", st)
		}
	}
}
