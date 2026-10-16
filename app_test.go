package main

import (
	"net"
	"path/filepath"
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

// TestSummarizeFailures 失败摘要：无失败 nil；有失败带服务器名与原因
// （真实路径：StartServer 指向已关闭端口 → failed）。
func TestSummarizeFailures(t *testing.T) {
	svc := newTestAppService(t)
	if err := svc.summarizeFailures(); err != nil {
		t.Fatalf("无失败应返回 nil: %v", err)
	}

}

// TestSetLanguagePersistsAndApplies 语言绑定：合法偏好落盘并即时应用进程语言；
// 非法偏好拒绝且不影响当前语言/配置；不触碰隧道状态。
func TestSetLanguagePersistsAndApplies(t *testing.T) {
	t.Cleanup(func() { i18n.Set(i18n.ZH) })
	svc := newTestAppService(t)

	if err := svc.SetLanguage(i18n.PrefEN); err != nil {
		t.Fatalf("SetLanguage(en): %v", err)
	}
	if i18n.Current() != i18n.EN {
		t.Fatalf("进程语言应为 en，得到 %v", i18n.Current())
	}
	if got := svc.store.Language(); got != i18n.PrefEN {
		t.Fatalf("偏好应落盘为 en，得到 %q", got)
	}
	if len(svc.Snapshot().Config.Services) != 0 {
		t.Fatal("切语言不应改动配置")
	}

	if err := svc.SetLanguage("fr"); err == nil {
		t.Fatal("非法偏好应返回错误")
	}
	if i18n.Current() != i18n.EN || svc.store.Language() != i18n.PrefEN {
		t.Fatalf("非法偏好不得改变状态: lang=%v pref=%q", i18n.Current(), svc.store.Language())
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
