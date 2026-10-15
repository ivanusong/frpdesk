package tunnel

import (
	"context"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/fatedier/frp/client"
	v1 "github.com/fatedier/frp/pkg/config/v1"
	frpserver "github.com/fatedier/frp/server"

	"frpdesk/internal/configstore"
)

// ---------- 真实 FRPS 集成测试 ----------
// 本地起真实 frps（随机端口、临时配置，绝不触碰用户远程服务器/真实配置），
// 验证多实例真实连接：同时 running、独立启停、端到端转发。

// freePort 取一个空闲端口（存在竞速，frps 启动失败即致命，实际冲突概率极低）。
func freePort(t *testing.T) int {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	return ln.Addr().(*net.TCPAddr).Port
}

// startTestFRPS 起一个真实 frps：随机 bind/vhost 端口，subDomainHost 固定，
// 返回 bindPort/vhostPort。生命周期挂 t.Cleanup。
func startTestFRPS(t *testing.T) (bindPort, vhostPort int) {
	t.Helper()
	bindPort = freePort(t)
	vhostPort = freePort(t)
	cfg := &v1.ServerConfig{
		BindAddr:      "127.0.0.1",
		BindPort:      bindPort,
		VhostHTTPPort: vhostPort,
		SubDomainHost: "svc.test",
	}
	if err := cfg.Complete(); err != nil { // 对齐 frps 官方入口：默认值补全在 NewService 之前
		t.Fatalf("frps 配置补全失败: %v", err)
	}
	svr, err := frpserver.NewService(cfg)
	if err != nil {
		t.Fatalf("启动 frps 失败: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	go svr.Run(ctx)
	t.Cleanup(func() {
		cancel()
		_ = svr.Close()
	})

	// 等 bind 端口就绪（frps 内部异步监听）
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		conn, err := net.DialTimeout("tcp", fmt.Sprintf("127.0.0.1:%d", bindPort), 200*time.Millisecond)
		if err == nil {
			_ = conn.Close()
			return bindPort, vhostPort
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatal("frps bind 端口未就绪")
	return 0, 0
}

// waitForLong 集成测试轮询等待（10s：覆盖 login + 代理注册 + 2s 采样周期）。
func waitForLong(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(100 * time.Millisecond)
	}
	t.Fatalf("等待超时：%s", what)
}

// TestTwoRealFRPSInstancesRunIndependently 双真实 frps 并发连接：
// 各实例独立 starting→running（不依赖日志判定），停一台另一台保持 running，
// HTTP 隧道端到端转发真实可用。
func TestTwoRealFRPSInstancesRunIndependently(t *testing.T) {
	bindA, vhostA := startTestFRPS(t)
	bindB, vhostB := startTestFRPS(t)

	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, "from-backend")
	}))
	defer backend.Close()
	host, port := backendHostPort(t, backend.URL)

	m := NewManager()
	defer func() { m.StopAll() }()

	servers := []configstore.ServerConfig{
		{ID: "a", Name: "frps-A", ServerAddr: "127.0.0.1", ServerPort: bindA, HTTPEnabled: true, BaseDomain: "a.test", Scheme: "http"},
		{ID: "b", Name: "frps-B", ServerAddr: "127.0.0.1", ServerPort: bindB, HTTPEnabled: true, BaseDomain: "b.test", Scheme: "http"},
	}
	svcs := []configstore.Service{
		{ID: "ta", ServerID: "a", Name: "web-a", LocalHost: host, LocalPort: port, Subdomain: "weba", Enabled: true, Type: configstore.TypeHTTP},
		{ID: "tb", ServerID: "b", Name: "web-b", LocalHost: host, LocalPort: port, Subdomain: "webb", Enabled: true, Type: configstore.TypeHTTP},
	}

	m.Reconcile(servers[0], svcs)
	m.Reconcile(servers[1], svcs)

	// 双实例都真实连接成功（running 判定 = 全部代理注册，采样驱动）
	waitForLong(t, "双实例 running", func() bool {
		st := m.ServerStatuses()
		return st["a"].State == configstore.StateRunning && st["b"].State == configstore.StateRunning
	})
	for _, st := range m.ServerStatuses() {
		if st.StartedAt == "" {
			t.Fatalf("running 实例应有连接时刻: %+v", st)
		}
	}

	// 端到端：A 的 HTTP 隧道真实转发（frps vhost 按 Host 分流）
	req, err := http.NewRequest(http.MethodGet, fmt.Sprintf("http://127.0.0.1:%d/", vhostA), nil)
	if err != nil {
		t.Fatal(err)
	}
	req.Host = "weba.svc.test" // frps subDomainHost=svc.test：路由键 = {subdomain}.svc.test
	resp, err := (&http.Client{Timeout: 5 * time.Second}).Do(req)
	if err != nil {
		t.Fatalf("经 frps-A 访问隧道失败: %v", err)
	}
	body, _ := io.ReadAll(resp.Body)
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusOK || string(body) != "from-backend" {
		t.Fatalf("端到端转发错误: status=%d body=%q", resp.StatusCode, body)
	}

	// 停 A（enabled 清零 → reconcile 自动停止）：B 保持 running（部分停止互不影响）
	m.Reconcile(servers[0], nil)
	waitForLong(t, "A stopped", func() bool {
		return m.ServerStatuses()["a"].State == configstore.StateStopped
	})
	if st := m.ServerStatuses()["b"].State; st != configstore.StateRunning {
		t.Fatalf("停止 A 后 B 应保持 running: %+v", st)
	}

	// B 的隧道仍可用
	req2, err := http.NewRequest(http.MethodGet, fmt.Sprintf("http://127.0.0.1:%d/", vhostB), nil)
	if err != nil {
		t.Fatal(err)
	}
	req2.Host = "webb.svc.test"
	resp2, err := (&http.Client{Timeout: 5 * time.Second}).Do(req2)
	if err != nil {
		t.Fatalf("经 frps-B 访问隧道失败: %v", err)
	}
	body2, _ := io.ReadAll(resp2.Body)
	_ = resp2.Body.Close()
	if resp2.StatusCode != http.StatusOK || string(body2) != "from-backend" {
		t.Fatalf("B 端到端转发错误: status=%d body=%q", resp2.StatusCode, body2)
	}
}

// TestRealFRPSLoginFailureIsolated 真实场景失败隔离：A 指向不存在的 frps → failed；
// B 真实连接 running；全局 frp 日志流（双实例混流）不把 A 的成功误判给 B 或反之。
func TestRealFRPSLoginFailureIsolated(t *testing.T) {
	deadBind := freePort(t) // 无人监听
	bindB, _ := startTestFRPS(t)

	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, "ok")
	}))
	defer backend.Close()
	host, port := backendHostPort(t, backend.URL)

	m := NewManager()
	defer func() { m.StopAll() }()

	servers := []configstore.ServerConfig{
		{ID: "dead", Name: "死服务器", ServerAddr: "127.0.0.1", ServerPort: deadBind, HTTPEnabled: true, BaseDomain: "x.test", Scheme: "http"},
		{ID: "live", Name: "活服务器", ServerAddr: "127.0.0.1", ServerPort: bindB, HTTPEnabled: true, BaseDomain: "x.test", Scheme: "http"},
	}
	svcs := []configstore.Service{
		{ID: "td", ServerID: "dead", Name: "d", LocalHost: host, LocalPort: port, Subdomain: "d1", Enabled: true, Type: configstore.TypeHTTP},
		{ID: "tl", ServerID: "live", Name: "l", LocalHost: host, LocalPort: port, Subdomain: "l1", Enabled: true, Type: configstore.TypeHTTP},
	}
	m.Reconcile(servers[0], svcs)
	m.Reconcile(servers[1], svcs)

	waitForLong(t, "dead 重试中 / live running", func() bool {
		st := m.ServerStatuses()
		return st["dead"].State == configstore.StateStarting && st["live"].State == configstore.StateRunning
	})
	// 停止 dead：cancel 终止 frp 自动重试，回 stopped（坏服务器仍可控）
	m.Reconcile(testServer("dead", "死服务器", deadBind), nil)
	waitForLong(t, "dead stopped", func() bool {
		return m.ServerStatuses()["dead"].State == configstore.StateStopped
	})
	if st := m.ServerStatuses()["live"].State; st != configstore.StateRunning {
		t.Fatalf("停止 dead 后 live 应保持 running: %+v", st)
	}
}

// TestRealFRPSProxyRegistrationFailureIndependent 真实注册失败场景：
// 同一 frps 上两条隧道抢同一 subdomain → 后注册的 start error（frps 拒绝），
// 实例保持 running（控制连接健康），失败代理独立展示为 failed——
// 连接状态与代理健康分离，不出现「永久 starting」。
func TestRealFRPSProxyRegistrationFailureIndependent(t *testing.T) {
	bind, _ := startTestFRPS(t)

	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, "ok")
	}))
	defer backend.Close()
	host, port := backendHostPort(t, backend.URL)

	m := NewManager()
	defer func() { m.StopAll() }()

	server := configstore.ServerConfig{ID: "s", Name: "frps", ServerAddr: "127.0.0.1", ServerPort: bind, HTTPEnabled: true, BaseDomain: "x.test", Scheme: "http"}
	svcs := []configstore.Service{
		{ID: "first", ServerID: "s", Name: "first", LocalHost: host, LocalPort: port, Subdomain: "clash", Enabled: true, Type: configstore.TypeHTTP},
		{ID: "second", ServerID: "s", Name: "second", LocalHost: host, LocalPort: port, Subdomain: "clash", Enabled: true, Type: configstore.TypeHTTP},
	}
	// store 层查重会拦同 subdomain；这里直接构造隧道级注册冲突（模拟 frps 侧已有同名代理，
	// 如另一个客户端占用）——隧道配置本身合法
	m.Reconcile(server, svcs)

	waitForLong(t, "实例 running（注册失败不影响连接主态）", func() bool {
		return m.ServerStatuses()["s"].State == configstore.StateRunning
	})
	// 两条隧道最终一 registered 一 failed（frps 拒绝重复 subdomain）
	waitForLong(t, "出现注册失败代理", func() bool {
		states := map[string]string{}
		for _, ps := range m.ProxyStatuses() {
			states[ps.ServiceID] = ps.State
		}
		return states["first"] == proxyStateRegistered && states["second"] == proxyStateFailed ||
			states["second"] == proxyStateRegistered && states["first"] == proxyStateFailed
	})
	// running 状态稳定（不被失败代理拖回 starting）
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if m.ServerStatuses()["s"].State != configstore.StateRunning {
			t.Fatalf("注册失败不应影响连接主态: %+v", m.ServerStatuses()["s"])
		}
		time.Sleep(200 * time.Millisecond)
	}
}

// TestRealFRPSHotReloadUpdatesInstanceState 真实热更新行为：加/删隧道后
// 逐服务状态跟随最新列表（新增代理真实注册、删除代理状态消失），
// 全程保持 client.Service 指针不变（不整体重启）。
func TestRealFRPSHotReloadUpdatesInstanceState(t *testing.T) {
	bind, _ := startTestFRPS(t)

	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, "ok")
	}))
	defer backend.Close()
	host, port := backendHostPort(t, backend.URL)

	m := NewManager()
	defer func() { m.StopAll() }()

	server := configstore.ServerConfig{ID: "s", Name: "frps", ServerAddr: "127.0.0.1", ServerPort: bind, HTTPEnabled: true, BaseDomain: "x.test", Scheme: "http"}
	svcs := []configstore.Service{
		{ID: "t1", ServerID: "s", Name: "one", LocalHost: host, LocalPort: port, Subdomain: "one", Enabled: true, Type: configstore.TypeHTTP},
	}
	m.Reconcile(server, svcs)
	waitForLong(t, "初始 running", func() bool {
		return m.ServerStatuses()["s"].State == configstore.StateRunning
	})
	m.mu.Lock()
	svcPtr := m.insts["s"].svc
	m.mu.Unlock()

	proxyState := func(id string) string {
		for _, ps := range m.ProxyStatuses() {
			if ps.ServiceID == id {
				return ps.State
			}
		}
		return "absent"
	}

	// 加：t2 注册成功（采样盯到新 ID）且实例指针不变
	added := append(append([]configstore.Service(nil), svcs...),
		configstore.Service{ID: "t2", ServerID: "s", Name: "two", LocalHost: host, LocalPort: port, Subdomain: "two", Enabled: true, Type: configstore.TypeHTTP})
	m.Reconcile(server, added)
	waitForLong(t, "t2 注册成功", func() bool {
		return proxyState("t2") == proxyStateRegistered && proxyState("t1") == proxyStateRegistered
	})
	m.mu.Lock()
	samePtr := m.insts["s"].svc == svcPtr
	m.mu.Unlock()
	if !samePtr {
		t.Fatal("普通热更新不得替换 client.Service")
	}

	// 删：t2 从逐服务状态消失，t1 保持
	m.Reconcile(server, svcs)
	waitForLong(t, "t2 状态消失", func() bool {
		return proxyState("t2") == "absent" && proxyState("t1") == proxyStateRegistered
	})
	m.mu.Lock()
	samePtr = m.insts["s"].svc == svcPtr
	m.mu.Unlock()
	if !samePtr {
		t.Fatal("删除隧道的热更新不得替换 client.Service")
	}
}

// TestRealFRPSHotReloadFallbackUsesNewConfig 真实回退回归：热更新下发失败强制重连后，
// 新实例用本次最新服务列表真实注册（本次新增的隧道不丢失）。
func TestRealFRPSHotReloadFallbackUsesNewConfig(t *testing.T) {
	bind, _ := startTestFRPS(t)

	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, "ok")
	}))
	defer backend.Close()
	host, port := backendHostPort(t, backend.URL)

	m := NewManager()
	defer func() { m.StopAll() }()

	server := configstore.ServerConfig{ID: "s", Name: "frps", ServerAddr: "127.0.0.1", ServerPort: bind, HTTPEnabled: true, BaseDomain: "x.test", Scheme: "http"}
	svcs := []configstore.Service{
		{ID: "t1", ServerID: "s", Name: "one", LocalHost: host, LocalPort: port, Subdomain: "one", Enabled: true, Type: configstore.TypeHTTP},
	}
	m.Reconcile(server, svcs)
	waitForLong(t, "初始 running", func() bool {
		return m.ServerStatuses()["s"].State == configstore.StateRunning
	})
	m.mu.Lock()
	oldPtr := m.insts["s"].svc
	m.mu.Unlock()

	// 注入下发失败：回退强制重连，且必须用含 t2 的最新列表
	oldUpdate := updateFrpcConfigSource
	updateFrpcConfigSource = func(*client.Service, *v1.ClientCommonConfig, []v1.ProxyConfigurer) error {
		return fmt.Errorf("模拟下发失败")
	}
	added := append(append([]configstore.Service(nil), svcs...),
		configstore.Service{ID: "t2", ServerID: "s", Name: "two", LocalHost: host, LocalPort: port, Subdomain: "two", Enabled: true, Type: configstore.TypeHTTP})
	m.Reconcile(server, added)
	updateFrpcConfigSource = oldUpdate // 回退重启后的真实下发放行

	// 新实例 + t2 真实注册成功（旧实例指针被替换）
	waitForLong(t, "回退重启且 t2 注册成功", func() bool {
		m.mu.Lock()
		cur := m.insts["s"]
		replaced := cur != nil && cur.svc != oldPtr
		m.mu.Unlock()
		if !replaced {
			return false
		}
		for _, ps := range m.ProxyStatuses() {
			if ps.ServiceID == "t2" && ps.State == proxyStateRegistered {
				return true
			}
		}
		return false
	})
}

// TestRealFRPSUDPRoundTrip 真实 UDP 端到端：本地 UDP echo 后端 + 真实 frps，
// UDP 隧道注册后客户端包经 frps 远程端口往返达本地后端；TCP 同端口共存
// （同服务器同数字远程端口跨协议允许，各自转发互不干扰）。
func TestRealFRPSUDPRoundTrip(t *testing.T) {
	bind, _ := startTestFRPS(t)
	udpRemote := freeUDPPort(t)

	// UDP echo 后端：收到的报文原样回发
	backendAddr := *startUDPEchoBackend(t)
	// TCP echo 后端：同数字远程端口走 TCP 也能往返（跨协议互不干扰）
	tcpAddr := startTCPEchoBackend(t)

	m := NewManager()
	defer func() { m.StopAll() }()

	server := configstore.ServerConfig{ID: "s", Name: "frps", ServerAddr: "127.0.0.1", ServerPort: bind, HTTPEnabled: false}
	svcs := []configstore.Service{
		{ID: "udp1", ServerID: "s", Name: "udp-svc", LocalHost: backendAddr.IP.String(), LocalPort: backendAddr.Port, Enabled: true, Type: configstore.TypeUDP, RemotePort: udpRemote},
		{ID: "tcp1", ServerID: "s", Name: "tcp-svc", LocalHost: tcpAddr.IP.String(), LocalPort: tcpAddr.Port, Enabled: true, Type: configstore.TypeTCP, RemotePort: udpRemote}, // 同数字跨协议
	}
	m.Reconcile(server, svcs)

	waitForLong(t, "实例 running", func() bool {
		return m.ServerStatuses()["s"].State == configstore.StateRunning
	})
	waitForLong(t, "UDP/TCP 代理均注册", func() bool {
		states := map[string]string{}
		for _, ps := range m.ProxyStatuses() {
			states[ps.ServiceID] = ps.State
		}
		return states["udp1"] == proxyStateRegistered && states["tcp1"] == proxyStateRegistered
	})

	// UDP 往返：发到 frps 的远程端口，应收到 echo 后端的原样回包
	conn, err := net.DialTimeout("udp", fmt.Sprintf("127.0.0.1:%d", udpRemote), 3*time.Second)
	if err != nil {
		t.Fatalf("连接 frps UDP 远程端口失败: %v", err)
	}
	defer conn.Close()
	if err := conn.SetDeadline(time.Now().Add(5 * time.Second)); err != nil {
		t.Fatal(err)
	}
	payload := []byte("ping-frpdesk-udp")
	if _, err := conn.Write(payload); err != nil {
		t.Fatalf("发送 UDP 包失败: %v", err)
	}
	buf := make([]byte, 256)
	n, err := conn.Read(buf)
	if err != nil {
		t.Fatalf("等待 UDP 回包失败: %v", err)
	}
	if string(buf[:n]) != string(payload) {
		t.Fatalf("UDP 往返内容错误: got %q want %q", buf[:n], payload)
	}

	// 同数字 TCP 往返：frps 的 TCP 同端口转发到 TCP echo，与 UDP 互不干扰
	tcpConn, err := net.DialTimeout("tcp", fmt.Sprintf("127.0.0.1:%d", udpRemote), 3*time.Second)
	if err != nil {
		t.Fatalf("连接 frps TCP 远程端口失败: %v", err)
	}
	defer tcpConn.Close()
	if err := tcpConn.SetDeadline(time.Now().Add(5 * time.Second)); err != nil {
		t.Fatal(err)
	}
	if _, err := tcpConn.Write([]byte("ping-frpdesk-tcp")); err != nil {
		t.Fatalf("发送 TCP 数据失败: %v", err)
	}
	n, err = tcpConn.Read(buf)
	if err != nil {
		t.Fatalf("等待 TCP 回包失败: %v", err)
	}
	if string(buf[:n]) != "ping-frpdesk-tcp" {
		t.Fatalf("TCP 往返内容错误: got %q", buf[:n])
	}
}

// freeUDPPort 取一个空闲 UDP 端口（存在竞速，与 freePort 同口径）。
func freeUDPPort(t *testing.T) int {
	t.Helper()
	conn, err := net.ListenUDP("udp", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1), Port: 0})
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	return conn.LocalAddr().(*net.UDPAddr).Port
}

// startTCPEchoBackend 起一个本地 TCP echo 服务，返回监听地址（生命周期挂 t.Cleanup）。
func startTCPEchoBackend(t *testing.T) *net.TCPAddr {
	t.Helper()
	ln, err := net.ListenTCP("tcp", &net.TCPAddr{IP: net.IPv4(127, 0, 0, 1), Port: 0})
	if err != nil {
		t.Fatal(err)
	}
	go func() {
		for {
			conn, err := ln.AcceptTCP()
			if err != nil {
				return
			}
			go func(c *net.TCPConn) {
				buf := make([]byte, 2048)
				for {
					n, err := c.Read(buf)
					if err != nil {
						return
					}
					if _, err := c.Write(buf[:n]); err != nil {
						return
					}
				}
			}(conn)
		}
	}()
	t.Cleanup(func() { _ = ln.Close() })
	return ln.Addr().(*net.TCPAddr)
}

// startUDPEchoBackend 起一个本地 UDP echo 服务，返回监听地址（生命周期挂 t.Cleanup）。
func startUDPEchoBackend(t *testing.T) *net.UDPAddr {
	t.Helper()
	conn, err := net.ListenUDP("udp", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1), Port: 0})
	if err != nil {
		t.Fatal(err)
	}
	go func() {
		buf := make([]byte, 2048)
		for {
			n, addr, err := conn.ReadFromUDP(buf)
			if err != nil {
				return
			}
			if _, err := conn.WriteToUDP(buf[:n], addr); err != nil {
				return
			}
		}
	}()
	t.Cleanup(func() { _ = conn.Close() })
	return conn.LocalAddr().(*net.UDPAddr)
}

// TestRealFRPSReconcileAutoLifecycle 声明式生命周期端到端：
// 无实例时 Reconcile(enabled>0) 自动连接；enabled 清零自动停止释放；
// 期间逐服务状态与端到端转发均真实可用。
func TestRealFRPSReconcileAutoLifecycle(t *testing.T) {
	bind, vhost := startTestFRPS(t)

	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, "auto-lifecycle")
	}))
	defer backend.Close()
	host, port := backendHostPort(t, backend.URL)

	m := NewManager()
	defer func() { m.StopAll() }()

	server := configstore.ServerConfig{ID: "s", Name: "frps", ServerAddr: "127.0.0.1", ServerPort: bind, HTTPEnabled: true, BaseDomain: "x.test", Scheme: "http"}
	svcs := []configstore.Service{
		{ID: "t1", ServerID: "s", Name: "web", LocalHost: host, LocalPort: port, Subdomain: "auto", Enabled: true, Type: configstore.TypeHTTP},
	}

	// 自动启动：无任何手动启动调用
	m.Reconcile(server, svcs)
	waitForLong(t, "自动连接 running", func() bool {
		return m.ServerStatuses()["s"].State == configstore.StateRunning
	})

	// 端到端可用
	req, _ := http.NewRequest(http.MethodGet, fmt.Sprintf("http://127.0.0.1:%d/", vhost), nil)
	req.Host = "auto.x.svc.test"
	// frps subDomainHost=svc.test：路由键 = auto.svc.test
	req.Host = "auto.svc.test"
	resp, err := (&http.Client{Timeout: 5 * time.Second}).Do(req)
	if err != nil {
		t.Fatalf("经 frps 访问失败: %v", err)
	}
	body, _ := io.ReadAll(resp.Body)
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusOK || string(body) != "auto-lifecycle" {
		t.Fatalf("端到端错误: status=%d body=%q", resp.StatusCode, body)
	}

	// enabled 清零 → 自动停止释放
	m.Reconcile(server, nil)
	waitForLong(t, "自动停止", func() bool {
		return m.ServerStatuses()["s"].State == configstore.StateStopped
	})
	m.mu.Lock()
	_, alive := m.insts["s"]
	m.mu.Unlock()
	if alive {
		t.Fatal("停止后实例应释放")
	}
}
