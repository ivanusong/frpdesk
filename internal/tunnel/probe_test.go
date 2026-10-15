package tunnel

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	v1 "github.com/fatedier/frp/pkg/config/v1"
	frpserver "github.com/fatedier/frp/server"

	"frpdesk/internal/configstore"
)

// ---------- 连通性探测 ----------

// startTestFRPSWithToken 起带鉴权的真实 frps（探测测试用），返回端口与 token。
func startTestFRPSWithToken(t *testing.T, token string) int {
	t.Helper()
	bindPort := freePort(t)
	cfg := &v1.ServerConfig{
		BindAddr: "127.0.0.1",
		BindPort: bindPort,
		Auth:     v1.AuthServerConfig{Token: token},
	}
	if err := cfg.Complete(); err != nil {
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
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		conn, err := net.DialTimeout("tcp", fmt.Sprintf("127.0.0.1:%d", bindPort), 200*time.Millisecond)
		if err == nil {
			_ = conn.Close()
			return bindPort
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatal("frps bind 端口未就绪")
	return 0
}

// shortenProbe 缩短探测超时（避免不可达用例拖慢测试），返回恢复函数。
func shortenProbe(t *testing.T, d time.Duration) {
	t.Helper()
	old := probeTimeout
	probeTimeout = d
	t.Cleanup(func() { probeTimeout = old })
}

func probeServer(id, name, addr string, port int, token string) configstore.ServerConfig {
	return configstore.ServerConfig{
		ID: id, Name: name, ServerAddr: addr, ServerPort: port,
		AuthToken: token, HTTPEnabled: true, BaseDomain: "x.test", Scheme: "http",
	}
}

// TestProbeRealFRPSSuccess 真实 frps + 正确 token：success、latency>0、
// message 说明控制连接与鉴权通过。
func TestProbeRealFRPSSuccess(t *testing.T) {
	token := "probe-secret"
	bind := startTestFRPSWithToken(t, token)

	res := TestServerConnection(probeServer("s", "ok-srv", "127.0.0.1", bind, token))
	if !res.Success {
		t.Fatalf("真实 frps 应探测成功: %+v", res)
	}
	if res.LatencyMs <= 0 {
		t.Fatalf("成功探测应有耗时: %+v", res)
	}
	if !contains(res.Message, "控制连接") || !contains(res.Message, "鉴权") {
		t.Fatalf("成功 message 应说明控制连接与鉴权: %q", res.Message)
	}
}

// TestProbeBadTokenRejected token 不匹配：LoginFailExit=true 首登即退，
// 失败被归类为「鉴权失败」（frps 回 token 不匹配错误），message 面向用户。
func TestProbeBadTokenRejected(t *testing.T) {
	bind := startTestFRPSWithToken(t, "right-token")

	res := TestServerConnection(probeServer("s", "bad-token", "127.0.0.1", bind, "wrong-token"))
	if res.Success {
		t.Fatalf("坏 token 不应成功: %+v", res)
	}
	if contains(res.Message, "超时") {
		t.Fatalf("首登失败应立即返回而非超时: %q", res.Message)
	}
	if !contains(res.Message, "鉴权失败") {
		t.Fatalf("token 不匹配应归类为鉴权失败: %q", res.Message)
	}
}

// TestProbePlainTCPNotFRPS 仅普通 TCP 监听（非 FRPS，不响应登录）：探测超时分类，
// 且探测实例的连接必须被完整收尾——服务端等待对端关闭（EOF）验证无连接残留，
// 不能只断言返回了超时。
func TestProbePlainTCPNotFRPS(t *testing.T) {
	shortenProbe(t, 1200*time.Millisecond)
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()

	var acceptedMu sync.Mutex
	var accepted []net.Conn
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			acceptedMu.Lock()
			accepted = append(accepted, c)
			acceptedMu.Unlock()
		}
	}()
	port := ln.Addr().(*net.TCPAddr).Port

	started := time.Now()
	res := TestServerConnection(probeServer("s", "plain-tcp", "127.0.0.1", port, ""))
	if res.Success {
		t.Fatalf("非 FRPS 服务不应成功: %+v", res)
	}
	if !contains(res.Message, "超时") {
		t.Fatalf("挂起的登录应归类为超时: %q", res.Message)
	}
	if time.Since(started) < 1100*time.Millisecond {
		t.Fatal("超时分支应等待探测时限")
	}

	// 无连接残留：探测实例收尾后必须关闭全部在途连接（对端读到 EOF 而非读超时）。
	// 若客户端 goroutine 仍挂在登录读上，这里会以「读超时」失败暴露残留。
	acceptedMu.Lock()
	conns := append([]net.Conn(nil), accepted...)
	acceptedMu.Unlock()
	if len(conns) == 0 {
		t.Fatal("探测应至少建立一次登录连接")
	}
	for i, c := range conns {
		_ = c.SetReadDeadline(time.Now().Add(5 * time.Second))
		buf := make([]byte, 1)
		for {
			_, err := c.Read(buf)
			if err == nil {
				continue // 先读到数据（如 TLS 握手字节）：继续等对端关闭
			}
			if errors.Is(err, io.EOF) || errors.Is(err, net.ErrClosed) {
				break // 对端已关闭：无连接残留 ✓
			}
			closeErr := c.Close()
			t.Fatalf("连接 %d 在探测结束后仍有残留（读未按预期结束）: err=%v closeErr=%v", i, err, closeErr)
		}
		_ = c.Close()
	}
}

// TestProbeUnreachable 不可达端口（连接拒绝）：快速失败分类。
func TestProbeUnreachable(t *testing.T) {
	dead := newFakeFRPS(t)
	deadPort := dead.port()
	dead.ln.Close() // 立即关闭：连接被拒

	res := TestServerConnection(probeServer("s", "dead", "127.0.0.1", deadPort, ""))
	if res.Success {
		t.Fatalf("不可达不应成功: %+v", res)
	}
	if contains(res.Message, "超时") {
		t.Fatalf("连接拒绝应立即失败而非超时: %q", res.Message)
	}
}

// TestProbeEmptyAddress 空地址直接失败（不发任何网络请求）。
func TestProbeEmptyAddress(t *testing.T) {
	res := TestServerConnection(probeServer("s", "empty", "  ", 7000, ""))
	if res.Success || !contains(res.Message, "地址为空") {
		t.Fatalf("空地址应直接失败: %+v", res)
	}
}

// TestProbeIsolatedFromRunningManager 探测与运行实例完全隔离：
// Manager 已连接实例的状态/逐服务状态不受探测影响，探测自身无 proxy 注册
// （临时实例不进 Manager，也不向 frps 注册任何隧道）。
func TestProbeIsolatedFromRunningManager(t *testing.T) {
	shortenProbe(t, 2*time.Second)
	bind, vhost := startTestFRPS(t)
	vhostPortHolder.mu.Lock()
	vhostPortHolder.port = vhost
	vhostPortHolder.mu.Unlock()

	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, "ok")
	}))
	defer backend.Close()
	host, port := backendHostPort(t, backend.URL)

	m := NewManager()
	defer func() { m.StopAll() }()

	server := testServer("s", "运行中", bind)
	svcs := []configstore.Service{testService("t1", "s", "iso", host, port)}
	m.Reconcile(server, svcs)
	waitForLong(t, "实例 running", func() bool {
		return m.ServerStatuses()["s"].State == configstore.StateRunning
	})
	beforeStatuses := m.ServerStatuses()["s"]
	beforeProxies := len(m.ProxyStatuses())
	if beforeProxies != 1 {
		t.Fatalf("运行实例应有 1 个代理: %d", beforeProxies)
	}
	m.mu.Lock()
	beforeService := m.insts["s"].svc
	m.mu.Unlock()

	// 探测同一台运行中的 frps（用户场景：已在连接时点「测试连接」）
	res := TestServerConnection(probeServer("s", "运行中", "127.0.0.1", bind, ""))
	if !res.Success {
		t.Fatalf("对运行中 frps 的探测应成功: %+v", res)
	}

	// Manager 状态与实例完全不受影响
	afterStatuses := m.ServerStatuses()["s"]
	if afterStatuses != beforeStatuses {
		t.Fatalf("探测不应改变实例状态: %+v → %+v", beforeStatuses, afterStatuses)
	}
	if got := len(m.ProxyStatuses()); got != beforeProxies {
		t.Fatalf("探测不应改变逐服务状态: %d → %d", beforeProxies, got)
	}
	m.mu.Lock()
	afterSvc := m.insts["s"].svc
	m.mu.Unlock()
	if afterSvc != beforeService {
		t.Fatal("探测后 client.Service 指针必须保持一致（实例未被替换）")
	}

	// 既有业务转发在探测之后仍成功（运行隧道未被探测干扰）
	req, err := http.NewRequest(http.MethodGet, fmt.Sprintf("http://127.0.0.1:%d/", vhostPortOf(t)), nil)
	if err != nil {
		t.Fatal(err)
	}
	req.Host = "iso.svc.test"
	resp, err := (&http.Client{Timeout: 5 * time.Second}).Do(req)
	if err != nil {
		t.Fatalf("探测后业务转发失败: %v", err)
	}
	body, _ := io.ReadAll(resp.Body)
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusOK || string(body) != "ok" {
		t.Fatalf("探测后转发错误: status=%d body=%q", resp.StatusCode, body)
	}
}

// vhostPortHolder 测试内共享 vhost 端口（TestProbeIsolatedFromRunningManager 写入）。
var vhostPortHolder struct {
	mu   sync.Mutex
	port int
}

func vhostPortOf(t *testing.T) int {
	vhostPortHolder.mu.Lock()
	defer vhostPortHolder.mu.Unlock()
	return vhostPortHolder.port
}

// TestProbeConcurrentLimited 并发探测受限（信号量）：多路同时探测全部成功
// 且互不干扰（泄漏防护由 probeConcurrency 上限保证）。
func TestProbeConcurrentLimited(t *testing.T) {
	bind := startTestFRPSWithToken(t, "")
	var ok int32
	var wg sync.WaitGroup
	for i := 0; i < 4; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			res := TestServerConnection(probeServer(fmt.Sprintf("s%d", i), fmt.Sprintf("srv%d", i), "127.0.0.1", bind, ""))
			if res.Success {
				atomic.AddInt32(&ok, 1)
			}
		}(i)
	}
	wg.Wait()
	if ok != 4 {
		t.Fatalf("并发探测应全部成功: %d/4", ok)
	}
}

func contains(s, sub string) bool { return strings.Contains(s, sub) }
