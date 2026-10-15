package tunnel

import (
	"bufio"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"reflect"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/fatedier/frp/client"
	v1 "github.com/fatedier/frp/pkg/config/v1"

	"frpdesk/internal/configstore"
)

// ---------- 测试辅助 ----------

func backendHostPort(t *testing.T, rawURL string) (string, int) {
	t.Helper()
	u, err := url.Parse(rawURL)
	if err != nil {
		t.Fatalf("解析后端地址失败: %v", err)
	}
	port, err := strconv.Atoi(u.Port())
	if err != nil {
		t.Fatalf("解析后端端口失败: %v", err)
	}
	return u.Hostname(), port
}

func newTestCORSProxy(t *testing.T, backend *httptest.Server) *corsProxy {
	t.Helper()
	host, port := backendHostPort(t, backend.URL)
	return newCORSProxyMust(t, host, port)
}

// newCORSProxyMust 按地址创建代理并注册清理，失败即致命。
// 测试默认不接统计（reporter=nil），需要统计断言的用例用 newReportingProxy。
func newCORSProxyMust(t *testing.T, host string, port int) *corsProxy {
	t.Helper()
	p, err := newCORSProxy(newCORSProxySet("srv-test", "测试服务器", nil), host, port, "test")
	if err != nil {
		t.Fatalf("创建本地代理失败: %v", err)
	}
	t.Cleanup(p.Close)
	return p
}

// reportedRequest reporter 采集到的单条上报。
type reportedRequest struct {
	serverID, serverName, serviceID, method, path, ua string
	status                                            int
	durationMs                                        int64
	bytesIn, bytesOut                                 int64
}

// newReportingProxy 创建带统计上报的代理，上报追加进共享缓冲（带锁，回调来自
// 请求 goroutine）。返回代理与取数函数。
func newReportingProxy(t *testing.T, host string, port int) (*corsProxy, func() []reportedRequest) {
	t.Helper()
	var mu sync.Mutex
	var recs []reportedRequest
	set := newCORSProxySet("srv-1", "一号服务器", func(serverID, serverName, serviceID, method, path, ua string, status int, durationMs int64, bytesIn, bytesOut int64) {
		mu.Lock()
		defer mu.Unlock()
		recs = append(recs, reportedRequest{serverID, serverName, serviceID, method, path, ua, status, durationMs, bytesIn, bytesOut})
	})
	p, err := newCORSProxy(set, host, port, "svc-1")
	if err != nil {
		t.Fatalf("创建带统计的本地代理失败: %v", err)
	}
	t.Cleanup(p.Close)
	// 暴露集合供改名用例刷新 serverName
	setRef = set
	return p, func() []reportedRequest {
		mu.Lock()
		defer mu.Unlock()
		return append([]reportedRequest(nil), recs...)
	}
}

// setRef newReportingProxy 创建的集合（serverName 改名用例刷新）。
var setRef *corsProxySet

// waitForReports 轮询等待上报条数达到 n（转发与上报异步，需等待），超时即致命。
func waitForReports(t *testing.T, get func() []reportedRequest, n int) []reportedRequest {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for {
		recs := get()
		if len(recs) >= n {
			return recs
		}
		if time.Now().After(deadline) {
			t.Fatalf("等待 %d 条上报超时，实际 %d 条", n, len(recs))
		}
		time.Sleep(5 * time.Millisecond)
	}
}

func proxyGET(t *testing.T, p *corsProxy, path string, mutate func(*http.Request)) *http.Response {
	t.Helper()
	req, err := http.NewRequest(http.MethodGet, "http://"+p.addr()+path, nil)
	if err != nil {
		t.Fatal(err)
	}
	if mutate != nil {
		mutate(req)
	}
	client := &http.Client{Timeout: 5 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		t.Fatalf("请求代理失败: %v", err)
	}
	return resp
}

func readBody(t *testing.T, resp *http.Response) string {
	t.Helper()
	defer resp.Body.Close()
	b, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("读取响应体失败: %v", err)
	}
	return string(b)
}

func assertProxyClosed(t *testing.T, p *corsProxy) {
	t.Helper()
	// Serve goroutine 注册监听与 Close 存在竞速，且错误路径由 Serve 退出时关监听：轮询等待关闭生效
	deadline := time.Now().Add(2 * time.Second)
	for {
		conn, err := net.DialTimeout("tcp", p.addr(), 100*time.Millisecond)
		if err != nil {
			return
		}
		_ = conn.Close()
		if time.Now().After(deadline) {
			t.Fatalf("代理 %s 应已关闭但仍可连接", p.addr())
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func varyTokens(h http.Header) []string {
	var out []string
	for _, v := range h.Values("Vary") {
		for _, f := range strings.Split(v, ",") {
			if f = strings.TrimSpace(f); f != "" {
				out = append(out, strings.ToLower(f))
			}
		}
	}
	return out
}

func countToken(tokens []string, want string) int {
	n := 0
	for _, tok := range tokens {
		if tok == want {
			n++
		}
	}
	return n
}

// ---------- 代理行为 ----------

// TestCORSProxyPreflightAnsweredLocally 预检由代理直接 204 应答：
// 回显方法与请求头，不触达后端，并补 Vary 的预检维度。
func TestCORSProxyPreflightAnsweredLocally(t *testing.T) {
	var hits atomic.Int32
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		fmt.Fprint(w, "backend")
	}))
	defer backend.Close()
	p := newTestCORSProxy(t, backend)

	req, err := http.NewRequest(http.MethodOptions, "http://"+p.addr()+"/api/data", nil)
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Origin", "https://app.example.com")
	req.Header.Set("Access-Control-Request-Method", "POST")
	req.Header.Set("Access-Control-Request-Headers", "authorization, content-type")
	resp, err := (&http.Client{Timeout: 5 * time.Second}).Do(req)
	if err != nil {
		t.Fatalf("预检请求失败: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusNoContent {
		t.Fatalf("预检状态应为 204，得到 %d", resp.StatusCode)
	}
	if got := resp.Header.Get("Access-Control-Allow-Origin"); got != "https://app.example.com" {
		t.Fatalf("应回显请求 Origin，得到 %q", got)
	}
	if got := resp.Header.Get("Access-Control-Allow-Credentials"); got != "true" {
		t.Fatalf("应允许凭据，得到 %q", got)
	}
	if got := resp.Header.Get("Access-Control-Allow-Methods"); got != "POST" {
		t.Fatalf("应回显请求方法，得到 %q", got)
	}
	if got := resp.Header.Get("Access-Control-Allow-Headers"); !strings.Contains(strings.ToLower(got), "authorization") ||
		!strings.Contains(strings.ToLower(got), "content-type") {
		t.Fatalf("应允许请求的 headers（Authorization/Content-Type），得到 %q", got)
	}
	tokens := varyTokens(resp.Header)
	for _, want := range []string{"origin", "access-control-request-method", "access-control-request-headers"} {
		if countToken(tokens, want) != 1 {
			t.Fatalf("Vary 应恰好包含一次 %q，得到 %v", want, tokens)
		}
	}
	if hits.Load() != 0 {
		t.Fatalf("预检不应请求后端，实际 %d 次", hits.Load())
	}
}

// TestCORSProxyRealResponseOverridesConflicts 真实响应：
// 覆盖后端冲突的 Allow-Origin/Credentials，保留业务头与 Expose-Headers，
// Vary 合并 Origin 且不重复。
func TestCORSProxyRealResponseOverridesConflicts(t *testing.T) {
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h := w.Header()
		h.Add("Access-Control-Allow-Origin", "*")
		h.Add("Access-Control-Allow-Origin", "https://other.example.com")
		h.Set("Access-Control-Allow-Credentials", "false")
		h.Set("Vary", "Accept-Encoding")
		h.Add("Vary", "origin") // 已含 Origin（小写）：不应重复追加
		h.Set("Access-Control-Expose-Headers", "X-Total-Count")
		h.Set("X-Business", "ok")
		w.WriteHeader(http.StatusTeapot)
		fmt.Fprint(w, "teapot")
	}))
	defer backend.Close()
	p := newTestCORSProxy(t, backend)

	resp := proxyGET(t, p, "/", func(r *http.Request) {
		r.Header.Set("Origin", "https://app.example.com")
	})
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusTeapot {
		t.Fatalf("应保留后端状态码，得到 %d", resp.StatusCode)
	}
	if got := resp.Header.Values("Access-Control-Allow-Origin"); len(got) != 1 || got[0] != "https://app.example.com" {
		t.Fatalf("应替换为单个回显 Origin，得到 %v", got)
	}
	if got := resp.Header.Get("Access-Control-Allow-Credentials"); got != "true" {
		t.Fatalf("凭据头应替换为 true，得到 %q", got)
	}
	tokens := varyTokens(resp.Header)
	if countToken(tokens, "origin") != 1 {
		t.Fatalf("Vary 中 Origin 应恰好一次，得到 %v", tokens)
	}
	if countToken(tokens, "accept-encoding") != 1 {
		t.Fatalf("Vary 应保留原有字段，得到 %v", tokens)
	}
	if got := resp.Header.Get("Access-Control-Expose-Headers"); got != "X-Total-Count" {
		t.Fatalf("Expose-Headers 应保留，得到 %q", got)
	}
	if got := resp.Header.Get("X-Business"); got != "ok" {
		t.Fatalf("业务头应保留，得到 %q", got)
	}
	if body := readBody(t, resp); body != "teapot" {
		t.Fatalf("响应体应透传，得到 %q", body)
	}
}

// TestCORSProxyPassthroughWithoutOrigin 不带 Origin 的请求正常转发：
// 不注入 Allow-Origin/Credentials（只补 Vary: Origin）；Host 改写为本地目标，
// 原公网 Host 经 X-Forwarded-Host 传递；路径/query/body 保持直连语义。
func TestCORSProxyPassthroughWithoutOrigin(t *testing.T) {
	type seenReq struct {
		host, xfh, escapedPath, rawQuery, body string
	}
	seen := make(chan seenReq, 2)
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		seen <- seenReq{host: r.Host, xfh: r.Header.Get("X-Forwarded-Host"), escapedPath: r.URL.EscapedPath(), rawQuery: r.URL.RawQuery, body: string(body)}
		fmt.Fprint(w, "ok")
	}))
	defer backend.Close()
	host, port := backendHostPort(t, backend.URL)
	p := newCORSProxyMust(t, host, port)

	req, err := http.NewRequest(http.MethodPost, "http://"+p.addr()+"/a/b?x=1&y=2", strings.NewReader("payload"))
	if err != nil {
		t.Fatal(err)
	}
	req.Host = "myapp.example.com:8443" // 公网访问的 Host：代理侧改写为本地目标，原值进 X-Forwarded-Host
	resp, err := (&http.Client{Timeout: 5 * time.Second}).Do(req)
	if err != nil {
		t.Fatal(err)
	}
	if body := readBody(t, resp); body != "ok" {
		t.Fatalf("响应体应透传，得到 %q", body)
	}
	got := <-seen
	if got.host != net.JoinHostPort(host, strconv.Itoa(port)) {
		t.Fatalf("Host 应为本地目标 %s，后端收到 %q", net.JoinHostPort(host, strconv.Itoa(port)), got.host)
	}
	if got.xfh != "myapp.example.com:8443" {
		t.Fatalf("原公网 Host 应进 X-Forwarded-Host，后端收到 %q", got.xfh)
	}
	if got.escapedPath != "/a/b" || got.rawQuery != "x=1&y=2" || got.body != "payload" {
		t.Fatalf("路径/query/body 透传错误: %+v", got)
	}
	if resp.Header.Get("Access-Control-Allow-Origin") != "" || resp.Header.Get("Access-Control-Allow-Credentials") != "" {
		t.Fatal("无 Origin 的请求不应注入 Allow-Origin/Credentials")
	}
	if countToken(varyTokens(resp.Header), "origin") != 1 {
		t.Fatal("无 Origin 的响应也应标 Vary: Origin（防无 Vary 缓存被带 Origin 请求命中）")
	}

	// 真实请求：分号 query 与编码路径不得被规范化/丢弃（纯穿透语义）
	req2, err := http.NewRequest(http.MethodGet, "http://"+p.addr()+"/files/a%2Fb%3Bc?q=1;2&raw=%2Fetc%2Fhosts", nil)
	if err != nil {
		t.Fatal(err)
	}
	resp2, err := (&http.Client{Timeout: 5 * time.Second}).Do(req2)
	if err != nil {
		t.Fatal(err)
	}
	_ = readBody(t, resp2)
	got2 := <-seen
	if got2.escapedPath != "/files/a%2Fb%3Bc" {
		t.Fatalf("编码路径被改写: %q", got2.escapedPath)
	}
	if got2.rawQuery != "q=1;2&raw=%2Fetc%2Fhosts" {
		t.Fatalf("原始 query 被规范化/丢弃: %q", got2.rawQuery)
	}
}

// TestCORSProxyForwardsPlainOptionsAndAddsErrorCORS 普通 OPTIONS 不拦截；
// 后端错误状态与代理 502 都带 CORS 头。
func TestCORSProxyForwardsPlainOptionsAndAddsErrorCORS(t *testing.T) {
	var hits atomic.Int32
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		if r.URL.Path == "/err" {
			w.WriteHeader(http.StatusInternalServerError)
			fmt.Fprint(w, "boom")
			return
		}
		fmt.Fprint(w, "ok")
	}))
	defer backend.Close()
	p := newTestCORSProxy(t, backend)

	// 带 Origin 但无 Access-Control-Request-Method 的普通 OPTIONS：照常转发
	req, err := http.NewRequest(http.MethodOptions, "http://"+p.addr()+"/plain", nil)
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Origin", "https://app.example.com")
	resp, err := (&http.Client{Timeout: 5 * time.Second}).Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("普通 OPTIONS 应转发到后端，得到 %d", resp.StatusCode)
	}
	if hits.Load() != 1 {
		t.Fatalf("普通 OPTIONS 应触达后端，实际 %d 次", hits.Load())
	}
	if got := resp.Header.Get("Access-Control-Allow-Origin"); got != "https://app.example.com" {
		t.Fatalf("普通 OPTIONS 也应携带 CORS 头，得到 %q", got)
	}

	// 后端 500：CORS 头同样存在
	errResp := proxyGET(t, p, "/err", func(r *http.Request) { r.Header.Set("Origin", "https://app.example.com") })
	if errResp.StatusCode != http.StatusInternalServerError {
		t.Fatalf("应透传后端 500，得到 %d", errResp.StatusCode)
	}
	if got := errResp.Header.Get("Access-Control-Allow-Origin"); got != "https://app.example.com" {
		t.Fatalf("错误响应应携带 CORS 头，得到 %q", got)
	}
	_ = readBody(t, errResp)

	// 后端不可达：代理由 ErrorHandler 返回 502，仍带 CORS 头
	dead := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	deadHost, deadPort := backendHostPort(t, dead.URL)
	dead.Close()
	deadProxy, err := newCORSProxy(newCORSProxySet("srv-dead", "死服务器", nil), deadHost, deadPort, "dead")
	if err != nil {
		t.Fatal(err)
	}
	defer deadProxy.Close()
	bad := proxyGET(t, deadProxy, "/", func(r *http.Request) { r.Header.Set("Origin", "https://app.example.com") })
	defer bad.Body.Close()
	if bad.StatusCode != http.StatusBadGateway {
		t.Fatalf("后端不可达应返回 502，得到 %d", bad.StatusCode)
	}
	if got := bad.Header.Get("Access-Control-Allow-Origin"); got != "https://app.example.com" {
		t.Fatalf("502 应携带 CORS 头，得到 %q", got)
	}
	// 无 Origin 的 502：不注入 CORS 凭据头，但 Vary: Origin 必须存在，避免错误响应被缓存
	badNoOrigin := proxyGET(t, deadProxy, "/", nil)
	defer badNoOrigin.Body.Close()
	if badNoOrigin.StatusCode != http.StatusBadGateway {
		t.Fatalf("后端不可达应返回 502，得到 %d", badNoOrigin.StatusCode)
	}
	if got := badNoOrigin.Header.Get("Access-Control-Allow-Origin"); got != "" {
		t.Fatalf("无 Origin 的 502 不应注入 Allow-Origin，得到 %q", got)
	}
	if countToken(varyTokens(badNoOrigin.Header), "origin") != 1 {
		t.Fatal("无 Origin 的 502 也应标 Vary: Origin")
	}
}

// TestCORSProxyCloseClosesIdleUpstreamConn 上游连接池为实例独占：
// Close 关闭自己的 idle 上游连接，另一个实例不受影响继续可用。
func TestCORSProxyCloseClosesIdleUpstreamConn(t *testing.T) {
	closed := make(chan struct{}, 4)
	backend := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, "ok")
	}))
	backend.Config.ConnState = func(_ net.Conn, s http.ConnState) {
		if s == http.StateClosed {
			select {
			case closed <- struct{}{}:
			default:
			}
		}
	}
	backend.Start()
	defer backend.Close()

	p1 := newTestCORSProxy(t, backend)
	p2 := newTestCORSProxy(t, backend)

	// 两个实例各建一条到后端的 keep-alive 连接
	for _, p := range []*corsProxy{p1, p2} {
		resp := proxyGET(t, p, "/", nil)
		_ = readBody(t, resp)
	}
	select {
	case <-closed:
		t.Fatal("keep-alive 请求期间不应出现上游连接关闭")
	default:
	}

	p1.Close()
	select {
	case <-closed:
	case <-time.After(2 * time.Second):
		t.Fatal("代理关闭后未关闭自己的 idle 上游连接")
	}

	// 另一实例的连接池不受影响
	resp := proxyGET(t, p2, "/", nil)
	if body := readBody(t, resp); body != "ok" {
		t.Fatalf("另一实例应继续可用，得到 %q", body)
	}
}

// TestCORSProxyStreamFlush 流式/SSE：后端 flush 后客户端应立即读到，无需等响应结束。
func TestCORSProxyStreamFlush(t *testing.T) {
	release := make(chan struct{})
	var releaseOnce sync.Once
	releaseFn := func() { releaseOnce.Do(func() { close(release) }) }
	defer releaseFn() // 失败路径也要放行，避免 httptest.Server.Close 等待挂起

	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		fmt.Fprint(w, "data: first\n\n")
		w.(http.Flusher).Flush()
		select {
		case <-release:
		case <-r.Context().Done():
			return
		}
		fmt.Fprint(w, "data: second\n\n")
	}))
	defer backend.Close()
	p := newTestCORSProxy(t, backend)

	resp := proxyGET(t, p, "/events", func(r *http.Request) { r.Header.Set("Origin", "https://app.example.com") })
	defer resp.Body.Close()
	br := bufio.NewReader(resp.Body)

	first := make([]byte, len("data: first\n\n"))
	if _, err := io.ReadFull(br, first); err != nil {
		t.Fatalf("应能立即读到首个流式分片: %v", err)
	}
	if string(first) != "data: first\n\n" {
		t.Fatalf("首个分片内容错误: %q", first)
	}
	releaseFn()
	second := make([]byte, len("data: second\n\n"))
	if _, err := io.ReadFull(br, second); err != nil {
		t.Fatalf("应能读到第二段分片: %v", err)
	}
	if string(second) != "data: second\n\n" {
		t.Fatalf("第二段分片内容错误: %q", second)
	}
}

// TestCORSProxyWebSocketUpgradeAndClose 升级链路（WebSocket）= 实际转发 + 生命周期：
// 代理释放必须关闭 hijack 后的升级连接，不能只关监听。
func TestCORSProxyWebSocketUpgradeAndClose(t *testing.T) {
	upgraded := make(chan struct{})
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hj, ok := w.(http.Hijacker)
		if !ok {
			http.Error(w, "no hijack", http.StatusInternalServerError)
			return
		}
		conn, brw, err := hj.Hijack()
		if err != nil {
			return
		}
		defer conn.Close()
		if _, err := brw.WriteString("HTTP/1.1 101 Switching Protocols\r\nUpgrade: websocket\r\nConnection: Upgrade\r\n\r\n"); err != nil {
			return
		}
		if err := brw.Flush(); err != nil {
			return
		}
		close(upgraded)
		for {
			line, err := brw.ReadString('\n')
			if err != nil {
				return
			}
			if _, err := brw.WriteString("echo:" + line); err != nil {
				return
			}
			if err := brw.Flush(); err != nil {
				return
			}
		}
	}))
	defer backend.Close()
	p := newTestCORSProxy(t, backend)

	conn, err := net.DialTimeout("tcp", p.addr(), 2*time.Second)
	if err != nil {
		t.Fatalf("连接代理失败: %v", err)
	}
	defer conn.Close()
	_ = conn.SetDeadline(time.Now().Add(5 * time.Second))
	if _, err := fmt.Fprintf(conn, "GET /ws HTTP/1.1\r\nHost: myapp.example.com\r\nConnection: Upgrade\r\nUpgrade: websocket\r\n\r\n"); err != nil {
		t.Fatal(err)
	}
	br := bufio.NewReader(conn)
	status, err := br.ReadString('\n')
	if err != nil {
		t.Fatalf("读取升级响应失败: %v", err)
	}
	if !strings.Contains(status, "101") {
		t.Fatalf("应升级成功，得到 %q", status)
	}
	for {
		line, err := br.ReadString('\n')
		if err != nil {
			t.Fatalf("读取升级响应头失败: %v", err)
		}
		if line == "\r\n" {
			break
		}
	}
	select {
	case <-upgraded:
	case <-time.After(2 * time.Second):
		t.Fatal("后端未进入升级态")
	}

	if _, err := fmt.Fprint(conn, "hello\n"); err != nil {
		t.Fatal(err)
	}
	echo, err := br.ReadString('\n')
	if err != nil {
		t.Fatalf("升级连接转发失败: %v", err)
	}
	if echo != "echo:hello\n" {
		t.Fatalf("升级连接回显错误: %q", echo)
	}

	// 代理释放：hijack 后的连接也必须被关闭（读应立刻返回 EOF/重置，而不是超时等待）
	p.Close()
	_ = conn.SetReadDeadline(time.Now().Add(2 * time.Second))
	if _, err := br.ReadByte(); err == nil {
		t.Fatal("代理释放后升级连接仍可读")
	} else if ne, ok := err.(net.Error); ok && ne.Timeout() {
		t.Fatal("代理释放未关闭 hijack 的升级连接（读超时）")
	}
}

// TestCORSProxyRewritesHostToLocalTarget 模拟 Vite server.allowedHosts 拒绝：
// 后端只接受本地 Host，公网域名 Host 直连被拒；经代理后 Host 变为本地目标放行，
// 原公网 Host 经 X-Forwarded-Host 传递（网关已有值保留），请求 Origin 保留。
func TestCORSProxyRewritesHostToLocalTarget(t *testing.T) {
	type seenReq struct {
		host, xfh, origin string
	}
	seen := make(chan seenReq, 4)
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Vite 行为：Host 非 localhost/127.0.0.1 时拒绝
		h := r.Host
		if hName, _, err := net.SplitHostPort(h); err == nil {
			h = hName
		}
		if h != "127.0.0.1" && h != "localhost" {
			seen <- seenReq{host: r.Host, xfh: r.Header.Get("X-Forwarded-Host"), origin: r.Header.Get("Origin")}
			http.Error(w, "Blocked request. This host \"\" is not allowed.", http.StatusForbidden)
			return
		}
		seen <- seenReq{host: r.Host, xfh: r.Header.Get("X-Forwarded-Host"), origin: r.Header.Get("Origin")}
		fmt.Fprint(w, "vite-ok")
	}))
	defer backend.Close()
	host, port := backendHostPort(t, backend.URL)
	p := newCORSProxyMust(t, host, port)

	client := &http.Client{Timeout: 5 * time.Second}
	do := func(mutate func(*http.Request)) *http.Response {
		t.Helper()
		req, err := http.NewRequest(http.MethodGet, "http://"+p.addr()+"/", nil)
		if err != nil {
			t.Fatal(err)
		}
		mutate(req)
		resp, err := client.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		return resp
	}

	// 1) 公网 Host 直连后端：被 allowedHosts 拒绝（403）
	req, err := http.NewRequest(http.MethodGet, backend.URL+"/", nil)
	if err != nil {
		t.Fatal(err)
	}
	req.Host = "myapp.example.com"
	direct, err := client.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	if direct.StatusCode != http.StatusForbidden {
		t.Fatalf("公网 Host 直连应被后端拒绝（403），得到 %d", direct.StatusCode)
	}
	_ = direct.Body.Close()
	got := <-seen
	if got.host != "myapp.example.com" {
		t.Fatalf("直连时后端应收到公网 Host，得到 %q", got.host)
	}

	// 2) 走代理：Host 变为本地目标放行，原公网 Host 进 X-Forwarded-Host，Origin 保留
	resp := do(func(r *http.Request) {
		r.Host = "myapp.example.com"
		r.Header.Set("Origin", "https://app.example.com")
	})
	if body := readBody(t, resp); body != "vite-ok" {
		t.Fatalf("走代理后应通过 allowedHosts 校验，得到 %q", body)
	}
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("走代理应得到 200，得到 %d", resp.StatusCode)
	}
	got = <-seen
	if got.host != net.JoinHostPort(host, strconv.Itoa(port)) {
		t.Fatalf("走代理后 Host 应为本地目标 %s，得到 %q", net.JoinHostPort(host, strconv.Itoa(port)), got.host)
	}
	if got.xfh != "myapp.example.com" {
		t.Fatalf("原公网 Host 应进 X-Forwarded-Host，得到 %q", got.xfh)
	}
	if got.origin != "https://app.example.com" {
		t.Fatalf("请求 Origin 应保留，得到 %q", got.origin)
	}

	// 3) 上游网关已提供的 X-Forwarded-Host 保留，不被 In.Host 覆盖
	resp2 := do(func(r *http.Request) {
		r.Host = "public.example.com"
		r.Header.Set("X-Forwarded-Host", "gw.example.com")
	})
	_ = readBody(t, resp2)
	got = <-seen
	if got.xfh != "gw.example.com" {
		t.Fatalf("网关已有的 X-Forwarded-Host 应保留，得到 %q", got.xfh)
	}
}

// ---------- 构建与生命周期 ----------

// TestBuildServiceProxiesEnabledHTTPOnly 接入规则：所有 enabled 的 HTTP 服务
// （含旧 cors:true 与无 cors 字段）默认创建本地代理；停用服务不建；
// TCP 始终直连不建代理（旧 JSON cors:true 也不改变 TCP）。
func TestBuildServiceProxiesEnabledHTTPOnly(t *testing.T) {
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, "direct-backend")
	}))
	defer backend.Close()
	host, port := backendHostPort(t, backend.URL)

	server := configstore.ServerConfig{ID: "srv", Name: "srv", ServerAddr: "127.0.0.1", ServerPort: 7000, HTTPEnabled: true}
	svcs := []configstore.Service{
		{ID: "http-on", ServerID: "srv", Name: "http-on", LocalHost: host, LocalPort: port, Subdomain: "c1", Enabled: true, Type: configstore.TypeHTTP},
		{ID: "legacy-type", ServerID: "srv", Name: "legacy-type", LocalHost: host, LocalPort: port, Subdomain: "l1", Enabled: true},
		{ID: "disabled", ServerID: "srv", Name: "disabled", LocalHost: host, LocalPort: port, Subdomain: "d1", Enabled: false, Type: configstore.TypeHTTP},
		{ID: "tcp", ServerID: "srv", Name: "tcp", LocalHost: host, LocalPort: port, Enabled: true, Type: configstore.TypeTCP, RemotePort: 36000},
	}

	// buildService 只接收 enabled 列表（过滤在调用方）：模拟 StartServer 的行为
	var enabled []configstore.Service
	for _, svc := range svcs {
		if svc.Enabled {
			enabled = append(enabled, svc)
		}
	}
	svc, proxies, err := buildService(server, enabled, nil)
	if err != nil {
		t.Fatalf("buildService 失败: %v", err)
	}
	if svc == nil || proxies == nil {
		t.Fatal("buildService 应返回 frp 服务与代理集合")
	}
	defer proxies.Close()

	for _, id := range []string{"http-on", "legacy-type"} {
		p := proxies.get(id)
		if p == nil {
			t.Fatalf("enabled 的 HTTP 服务 %s 应创建本地代理", id)
		}
		// frp 侧应指向 loopback 代理
		gotHost, gotPort := proxies.localAddr(svcs[indexOfService(svcs, id)])
		if gotHost != "127.0.0.1" || gotPort != p.port() {
			t.Fatalf("%s 的 frp 本地目标应指向代理，得到 %s:%d", id, gotHost, gotPort)
		}
		// 代理确实转发到原后端
		resp := proxyGET(t, p, "/", nil)
		if body := readBody(t, resp); body != "direct-backend" {
			t.Fatalf("代理应转发到原后端，得到 %q", body)
		}
	}
	if proxies.get("disabled") != nil {
		t.Fatal("停用服务不应创建代理")
	}
	if proxies.get("tcp") != nil {
		t.Fatal("TCP 服务不应创建代理（始终直连）")
	}
	// 停用与 TCP 保持直连地址
	for _, id := range []string{"disabled", "tcp"} {
		dHost, dPort := proxies.localAddr(svcs[indexOfService(svcs, id)])
		if dHost != host || dPort != port {
			t.Fatalf("%s 应保持直连 %s:%d，得到 %s:%d", id, host, port, dHost, dPort)
		}
	}
}

func indexOfService(svcs []configstore.Service, id string) int {
	for i, svc := range svcs {
		if svc.ID == id {
			return i
		}
	}
	return -1
}

// recordingListener 记录 Close 调用，用于验证构建失败时的资源回滚。
type recordingListener struct {
	net.Listener
	closed atomic.Bool
}

func (l *recordingListener) Close() error {
	l.closed.Store(true)
	return l.Listener.Close()
}

// TestBuildServiceRollbackOnProxyCreateFailure 第二个服务代理创建失败时，
// 第一个已创建的代理必须被回滚（监听关闭）。
func TestBuildServiceRollbackOnProxyCreateFailure(t *testing.T) {
	var mu sync.Mutex
	var created []*recordingListener
	old := listenCORSProxy
	listenCORSProxy = func() (net.Listener, error) {
		mu.Lock()
		defer mu.Unlock()
		if len(created) >= 1 {
			return nil, errors.New("模拟监听失败")
		}
		ln, err := net.Listen("tcp", "127.0.0.1:0")
		if err != nil {
			return nil, err
		}
		rl := &recordingListener{Listener: ln}
		created = append(created, rl)
		return rl, nil
	}
	t.Cleanup(func() { listenCORSProxy = old })

	server := configstore.ServerConfig{ID: "srv", Name: "srv", ServerAddr: "127.0.0.1", ServerPort: 7000, HTTPEnabled: true}
	svcs := []configstore.Service{
		{ID: "s1", ServerID: "srv", Name: "s1", LocalHost: "127.0.0.1", LocalPort: 8081, Subdomain: "s1", Enabled: true, Type: configstore.TypeHTTP},
		{ID: "s2", ServerID: "srv", Name: "s2", LocalHost: "127.0.0.1", LocalPort: 8082, Subdomain: "s2", Enabled: true, Type: configstore.TypeHTTP},
	}
	svc, proxies, err := buildService(server, svcs, nil)
	if err == nil || svc != nil || proxies != nil {
		t.Fatalf("代理创建失败应返回错误且不返回资源: svc=%v proxies=%v err=%v", svc, proxies, err)
	}
	mu.Lock()
	defer mu.Unlock()
	if len(created) != 1 {
		t.Fatalf("应只成功创建第一个代理，实际 %d", len(created))
	}
	// Serve goroutine 注册监听与 Close 存在竞速：等待关闭生效（Serve 退出路径也会关监听）
	deadline := time.Now().Add(2 * time.Second)
	for !created[0].closed.Load() && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
	if !created[0].closed.Load() {
		t.Fatal("创建失败时应回滚已创建的代理（监听未关闭）")
	}
}

// TestStopReleasesInstanceProxies Stop 释放本实例代理资源。
func TestStopReleasesInstanceProxies(t *testing.T) {
	m := NewManager()
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, "ok")
	}))
	defer backend.Close()
	p := newTestCORSProxy(t, backend)
	set := newCORSProxySet("srv-a", "A", nil)
	set.add("s1", p)

	inst := &serverInstance{
		server:  configstore.ServerConfig{ID: "srv-a", Name: "a"},
		svc:     &client.Service{},
		cancel:  func() {}, // 测试无真实 Run goroutine：no-op 取消
		done:    make(chan struct{}),
		proxies: set,
		state:   configstore.StateRunning,
	}
	m.mu.Lock()
	m.insts["srv-a"] = inst
	var notes []notify
	m.stopInstanceLocked(&notes, inst)
	m.mu.Unlock()
	m.flush(notes)

	if set.get("s1") != nil {
		t.Fatal("Stop 后代理集合应已清空")
	}
	assertProxyClosed(t, p)
}

// TestFinishRunReleasesOnlyOwnProxies 旧实例收尾只释放自己的资源，
// 不得关闭新实例的代理；新实例收尾再释放自己的。
func TestFinishRunReleasesOnlyOwnProxies(t *testing.T) {
	m := NewManager()
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, "ok")
	}))
	defer backend.Close()

	oldP := newTestCORSProxy(t, backend)
	newP := newTestCORSProxy(t, backend)
	oldSet := newCORSProxySet("srv-old", "Old", nil)
	oldSet.add("old", oldP)
	newSet := newCORSProxySet("srv-new", "New", nil)
	newSet.add("new", newP)

	oldInst := &serverInstance{server: configstore.ServerConfig{ID: "srv-old", Name: "old"}, svc: &client.Service{}, done: make(chan struct{}), proxies: oldSet}
	newInst := &serverInstance{server: configstore.ServerConfig{ID: "srv-new", Name: "new"}, svc: &client.Service{}, done: make(chan struct{}), proxies: newSet}
	m.mu.Lock()
	m.insts["srv-old"] = oldInst
	m.insts["srv-new"] = newInst
	m.mu.Unlock()

	// 旧实例陈旧收尾：只释放自己，新实例代理保持可用
	m.finishRun(oldInst, nil)
	if oldSet.get("old") != nil {
		t.Fatal("旧实例收尾应释放自己的代理")
	}
	assertProxyClosed(t, oldP)
	if newSet.get("new") == nil {
		t.Fatal("旧实例收尾不应释放新实例的代理")
	}
	resp := proxyGET(t, newP, "/", nil)
	if body := readBody(t, resp); body != "ok" {
		t.Fatalf("新实例代理应仍可用，得到 %q", body)
	}

	// 新实例正常收尾：释放自己的代理
	m.finishRun(newInst, nil)
	if newSet.get("new") != nil {
		t.Fatal("新实例收尾应释放自己的代理")
	}
	assertProxyClosed(t, newP)
}

// TestReconcileStartStopReleasesProxies Reconcile 为 enabled HTTP 服务创建代理，
// enabled 清零自动停止并释放代理（隧道退出不能留下可连的 loopback 监听）。
func TestReconcileStartStopReleasesProxies(t *testing.T) {
	// 假 frps：接受连接但不响应登录，让 Run 停在登录中，便于观察启动期资源
	fake, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer fake.Close()
	var accepted []net.Conn
	var acceptMu sync.Mutex
	go func() {
		for {
			c, err := fake.Accept()
			if err != nil {
				return
			}
			acceptMu.Lock()
			accepted = append(accepted, c)
			acceptMu.Unlock()
		}
	}()
	t.Cleanup(func() {
		acceptMu.Lock()
		for _, c := range accepted {
			_ = c.Close()
		}
		acceptMu.Unlock()
	})

	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, "backend")
	}))
	defer backend.Close()
	host, port := backendHostPort(t, backend.URL)
	frpsPort := fake.Addr().(*net.TCPAddr).Port

	server := configstore.ServerConfig{ID: "srv1", Name: "web-srv", ServerAddr: "127.0.0.1", ServerPort: frpsPort, HTTPEnabled: true, BaseDomain: "example.com", Scheme: "https"}
	svcs := []configstore.Service{{
		ID: "s1", ServerID: "srv1", Name: "web", LocalHost: host, LocalPort: port,
		Subdomain: "web", Enabled: true, Type: configstore.TypeHTTP,
	}}

	m := NewManager()
	m.Reconcile(server, svcs)
	waitFor(t, "进入 starting", func() bool {
		return m.ServerStatuses()["srv1"].State == configstore.StateStarting
	})
	m.mu.Lock()
	inst := m.insts["srv1"]
	m.mu.Unlock()
	if inst == nil {
		t.Fatal("启动中应存在运行实例")
	}
	p := inst.proxies.get("s1")
	if p == nil {
		t.Fatal("enabled 且勾选跨域的服务应创建代理")
	}
	resp := proxyGET(t, p, "/", nil)
	if body := readBody(t, resp); body != "backend" {
		t.Fatalf("启动期代理应可用，得到 %q", body)
	}

	m.Reconcile(server, nil) // enabled=0 → 自动停止
	if inst.proxies.get("s1") != nil {
		t.Fatal("Stop 后实例代理集合应已清空")
	}
	assertProxyClosed(t, p)

	// 关闭假 frps 已接受的连接，让 Run 收尾更快，避免测试残留 goroutine
	acceptMu.Lock()
	for _, c := range accepted {
		_ = c.Close()
	}
	acceptMu.Unlock()
	select {
	case <-inst.done:
	case <-time.After(3 * time.Second):
		t.Fatal("Stop 后 Run 未在预期时间内收尾")
	}
}

// ---------- 热更新同步 ----------

// TestProxySetSyncToRetainCreateRemoveTypeSwitch syncTo 四分支：
// 同 ID 同 target 保留复用（指针不变）、缺失的新建、删除/禁用/切成 TCP 的移除、
// target 变化的先移除旧代理再新建。
func TestProxySetSyncToRetainCreateRemoveTypeSwitch(t *testing.T) {
	set := newCORSProxySet("srv-x", "X", nil)
	t.Cleanup(set.Close)

	svc := func(id string, port int, enabled bool, typ string) configstore.Service {
		return configstore.Service{
			ID: id, ServerID: "srv-x", Name: id, LocalHost: "127.0.0.1", LocalPort: port,
			Subdomain: id, Enabled: enabled, Type: typ, RemotePort: 36000,
		}
	}
	// 初始态：keep / retarget / drop 三个 HTTP 隧道
	cfg1 := []configstore.Service{
		svc("keep", 9001, true, configstore.TypeHTTP),
		svc("retarget", 9002, true, configstore.TypeHTTP),
		svc("drop", 9003, true, configstore.TypeHTTP),
	}
	if err := set.syncTo(cfg1); err != nil {
		t.Fatalf("初始 syncTo 失败: %v", err)
	}
	pKeep, pRetarget, pDrop := set.get("keep"), set.get("retarget"), set.get("drop")
	if pKeep == nil || pRetarget == nil || pDrop == nil {
		t.Fatal("初始 syncTo 应为全部 enabled HTTP 服务新建代理")
	}

	// 目标态：keep 不变（保留）、retarget 换端口（target 变化）、
	// drop 删除（移除）、new 新建、off 停用（不建代理）、toTcp 切 TCP（不建代理）
	cfg2 := []configstore.Service{
		svc("keep", 9001, true, configstore.TypeHTTP),
		svc("retarget", 9102, true, configstore.TypeHTTP), // target 变化
		svc("new", 9004, true, configstore.TypeHTTP),
		svc("off", 9005, false, configstore.TypeHTTP), // 停用
		svc("toTcp", 9006, true, configstore.TypeTCP), // 切成 TCP：直连不建代理
		// drop 缺席（删除）
	}
	if err := set.syncTo(cfg2); err != nil {
		t.Fatalf("第二次 syncTo 失败: %v", err)
	}

	if got := set.get("keep"); got != pKeep {
		t.Fatal("同 ID 同 target 的代理应保留复用（同一实例）")
	}
	if got := set.get("retarget"); got == nil || got == pRetarget {
		t.Fatal("target 变化应移除旧代理并新建（新实例且非 nil）")
	}
	if set.get("new") == nil {
		t.Fatal("目标态中缺失的代理应新建")
	}
	if set.get("drop") != nil {
		t.Fatal("被删除服务的代理应从集合移除")
	}
	if set.get("off") != nil {
		t.Fatal("停用服务不应创建代理")
	}
	if set.get("toTcp") != nil {
		t.Fatal("切成 TCP 的服务不应创建代理（始终直连）")
	}
	// 被移除的代理（drop 与 retarget 旧实例）必须已关闭，不留可连监听
	assertProxyClosed(t, pDrop)
	assertProxyClosed(t, pRetarget)
}

// TestProxySetUDPDirectAndBuildUDPProxyCfgs UDP 服务不走本地代理（直连语义与 TCP
// 一致），buildProxyCfgs 生成 v1.UDPProxyConfig；HTTP 切 UDP 时旧代理被移除。
func TestProxySetUDPDirectAndBuildUDPProxyCfgs(t *testing.T) {
	set := newCORSProxySet("srv-x", "X", nil)
	t.Cleanup(set.Close)

	http := configstore.Service{ID: "h1", ServerID: "srv-x", Name: "web", LocalHost: "127.0.0.1", LocalPort: 9001, Subdomain: "web", Enabled: true, Type: configstore.TypeHTTP}
	udp := configstore.Service{ID: "u1", ServerID: "srv-x", Name: "dns", LocalHost: "127.0.0.1", LocalPort: 53, Enabled: true, Type: configstore.TypeUDP, RemotePort: 36053}

	if err := set.syncTo([]configstore.Service{http, udp}); err != nil {
		t.Fatalf("syncTo 失败: %v", err)
	}
	if set.get("h1") == nil {
		t.Fatal("HTTP 服务应建本地代理")
	}
	if set.get("u1") != nil {
		t.Fatal("UDP 服务不应建本地代理（直连）")
	}

	cfgs, err := buildProxyCfgs([]configstore.Service{udp}, set)
	if err != nil {
		t.Fatalf("buildProxyCfgs 失败: %v", err)
	}
	if len(cfgs) != 1 {
		t.Fatalf("应生成 1 个代理配置: %d", len(cfgs))
	}
	u, ok := cfgs[0].(*v1.UDPProxyConfig)
	if !ok {
		t.Fatalf("应生成 *v1.UDPProxyConfig: %T", cfgs[0])
	}
	if u.Name != "frpdesk-u1" || u.Type != "udp" || u.LocalIP != "127.0.0.1" || u.LocalPort != 53 || u.RemotePort != 36053 {
		t.Fatalf("UDP 代理配置字段错误: %+v", u)
	}

	// HTTP 切 UDP：旧本地代理移除（后续 localAddr 直连原目标，不再指向 loopback）
	switched := http
	switched.Type = configstore.TypeUDP
	switched.Subdomain = ""
	switched.RemotePort = 36001
	if err := set.syncTo([]configstore.Service{switched, udp}); err != nil {
		t.Fatalf("切换后 syncTo 失败: %v", err)
	}
	if set.get("h1") != nil {
		t.Fatal("HTTP 切 UDP 后旧代理应移除")
	}
}

// TestBuildProxyCfgsStableAcrossHotReload 热更新不误杀的前提：
// 未变化服务两次生成的代理配置（含保留复用的本地代理端口）必须 DeepEqual 相等，
// 否则 frp UpdateAll 按名 diff 会把未变化代理判定为「配置变化」而注销重注册。
func TestBuildProxyCfgsStableAcrossHotReload(t *testing.T) {
	set := newCORSProxySet("srv-x", "X", nil)
	t.Cleanup(set.Close)

	svcs := []configstore.Service{
		{ID: "c1", Name: "c1", LocalHost: "127.0.0.1", LocalPort: 8001, Subdomain: "c1", Enabled: true, Type: configstore.TypeHTTP},
		{ID: "p1", Name: "p1", LocalHost: "127.0.0.1", LocalPort: 8002, Subdomain: "p1", Enabled: true, Type: configstore.TypeHTTP},
		{ID: "t1", Name: "t1", LocalHost: "127.0.0.1", LocalPort: 8003, Enabled: true, Type: configstore.TypeTCP, RemotePort: 36000},
	}
	first, err := buildProxyCfgs(svcs, set)
	if err != nil {
		t.Fatalf("首次 buildProxyCfgs 失败: %v", err)
	}

	// 模拟热更新：syncTo（保留复用）→ 再次生成，两次结果必须逐字段一致
	if err := set.syncTo(svcs); err != nil {
		t.Fatalf("syncTo 失败: %v", err)
	}
	second, err := buildProxyCfgs(svcs, set)
	if err != nil {
		t.Fatalf("二次 buildProxyCfgs 失败: %v", err)
	}

	if len(first) != 3 || len(second) != 3 {
		t.Fatalf("应生成 3 个代理配置，得到 %d / %d", len(first), len(second))
	}
	for i := range first {
		if !reflect.DeepEqual(first[i], second[i]) {
			t.Fatalf("第 %d 个代理配置两次生成不一致（会触发 frp 误杀重注册）:\n  %#v\n  %#v",
				i, first[i], second[i])
		}
	}
}

// ---------- 请求统计挂载 ----------

// TestCORSProxyStatsReporting 统计挂载口径：正常请求/带 body 请求/预检 204/上游失败
// 502/101 升级各计一次，字节与 path 符合约定（bytesIn=请求 body、bytesOut=响应 body）。
func TestCORSProxyStatsReporting(t *testing.T) {
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		if r.URL.Path == "/echo" {
			w.Write(body) // 原样回显：bytesIn/bytesOut 可精确对账
			return
		}
		fmt.Fprint(w, "backend!")
	}))
	defer backend.Close()
	host, port := backendHostPort(t, backend.URL)
	p, getReports := newReportingProxy(t, host, port)

	client := &http.Client{Timeout: 5 * time.Second}

	// 1) 普通 GET 200：bytesOut = 响应 body 长度，bytesIn = 0；UA 原文透传
	req1, err := http.NewRequest(http.MethodGet, "http://"+p.addr()+"/", nil)
	if err != nil {
		t.Fatal(err)
	}
	req1.Header.Set("User-Agent", "Mozilla/5.0 (Windows NT 10.0; Win64; x64) Chrome/126.0")
	resp, err := client.Do(req1)
	if err != nil {
		t.Fatal(err)
	}
	if body := readBody(t, resp); body != "backend!" {
		t.Fatalf("响应体应透传，得到 %q", body)
	}
	recs := waitForReports(t, getReports, 1)
	if r := recs[0]; r.serverID != "srv-1" || r.serverName != "一号服务器" || r.serviceID != "svc-1" || r.method != "GET" || r.path != "/" ||
		r.status != 200 || r.bytesIn != 0 || r.bytesOut != int64(len("backend!")) {
		t.Fatalf("GET 上报错误: %+v", r)
	}
	if !strings.Contains(recs[0].ua, "Windows NT") {
		t.Fatalf("UA 应原样透传，得到 %q", recs[0].ua)
	}

	// 2) POST 带 body + query：bytesIn = body 长度，path 保留完整 path+query
	req, err := http.NewRequest(http.MethodPost, "http://"+p.addr()+"/echo?x=1", strings.NewReader("hello"))
	if err != nil {
		t.Fatal(err)
	}
	resp, err = client.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	if body := readBody(t, resp); body != "hello" {
		t.Fatalf("回显错误: %q", body)
	}
	recs = waitForReports(t, getReports, 2)
	if r := recs[1]; r.method != "POST" || r.path != "/echo?x=1" || r.status != 200 ||
		r.bytesIn != 5 || r.bytesOut != 5 {
		t.Fatalf("POST 上报错误: %+v", r)
	}

	// 3) 预检：204、字节 0、后端未触达（后端 handler 对 OPTIONS 会回显非 204，可区分）；UA 也透传
	req, err = http.NewRequest(http.MethodOptions, "http://"+p.addr()+"/echo", nil)
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Origin", "https://app.example.com")
	req.Header.Set("Access-Control-Request-Method", "POST")
	req.Header.Set("User-Agent", "Mozilla/5.0 (iPhone; CPU iPhone OS 17_0) Safari/604.1")
	resp, err = client.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode != http.StatusNoContent {
		t.Fatalf("预检应本地 204，得到 %d", resp.StatusCode)
	}
	_ = resp.Body.Close()
	recs = waitForReports(t, getReports, 3)
	if r := recs[2]; r.method != "OPTIONS" || r.status != 204 || r.bytesIn != 0 || r.bytesOut != 0 {
		t.Fatalf("预检上报错误: %+v", r)
	}
	if !strings.Contains(recs[2].ua, "iPhone") {
		t.Fatalf("预检 UA 应透传，得到 %q", recs[2].ua)
	}

	// 4) 上游不可达：ErrorHandler 502 也计一次
	dead := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	deadHost, deadPort := backendHostPort(t, dead.URL)
	dead.Close()
	p2, getReports2 := newReportingProxy(t, deadHost, deadPort)
	resp, err = client.Get("http://" + p2.addr() + "/gone")
	if err != nil {
		t.Fatal(err)
	}
	_ = readBody(t, resp)
	recs = waitForReports(t, getReports2, 1)
	if r := recs[0]; r.status != http.StatusBadGateway || r.path != "/gone" || r.bytesOut != 0 {
		t.Fatalf("502 上报错误: %+v", r)
	}
}

// TestCORSProxyStatsReportUpgrade WebSocket 升级计一次 status=101、字节 0
// （升级后裸流不计字节是已知限制）。
func TestCORSProxyStatsReportUpgrade(t *testing.T) {
	upgraded := make(chan struct{})
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hj, ok := w.(http.Hijacker)
		if !ok {
			http.Error(w, "no hijack", http.StatusInternalServerError)
			return
		}
		conn, brw, err := hj.Hijack()
		if err != nil {
			return
		}
		defer conn.Close()
		if _, err := brw.WriteString("HTTP/1.1 101 Switching Protocols\r\nUpgrade: websocket\r\nConnection: Upgrade\r\n\r\n"); err != nil {
			return
		}
		_ = brw.Flush()
		close(upgraded)
		select {
		case <-r.Context().Done(): // 客户端断开即退出
		}
	}))
	defer backend.Close()
	host, port := backendHostPort(t, backend.URL)
	p, getReports := newReportingProxy(t, host, port)

	conn, err := net.DialTimeout("tcp", p.addr(), 2*time.Second)
	if err != nil {
		t.Fatalf("连接代理失败: %v", err)
	}
	defer conn.Close()
	_ = conn.SetDeadline(time.Now().Add(5 * time.Second))
	if _, err := fmt.Fprintf(conn, "GET /ws HTTP/1.1\r\nHost: myapp.example.com\r\nConnection: Upgrade\r\nUpgrade: websocket\r\n\r\n"); err != nil {
		t.Fatal(err)
	}
	br := bufio.NewReader(conn)
	status, err := br.ReadString('\n')
	if err != nil {
		t.Fatalf("读取升级响应失败: %v", err)
	}
	if !strings.Contains(status, "101") {
		t.Fatalf("应升级成功，得到 %q", status)
	}
	select {
	case <-upgraded:
	case <-time.After(2 * time.Second):
		t.Fatal("后端未进入升级态")
	}

	recs := waitForReports(t, getReports, 1)
	if r := recs[0]; r.method != "GET" || r.path != "/ws" || r.status != 101 || r.bytesIn != 0 || r.bytesOut != 0 {
		t.Fatalf("升级上报错误: %+v", r)
	}
}

// TestCORSProxyNilReporterNoReport reporter 为 nil（Manager 字段留空）时不上报、
// 代理行为不变。
func TestCORSProxyNilReporterNoReport(t *testing.T) {
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, "ok")
	}))
	defer backend.Close()
	p := newTestCORSProxy(t, backend) // reporter=nil
	if p.set.reporter != nil {
		t.Fatal("测试辅助应创建无 reporter 的代理")
	}
	resp := proxyGET(t, p, "/", nil)
	if body := readBody(t, resp); body != "ok" {
		t.Fatalf("无 reporter 时代理应正常转发，得到 %q", body)
	}
}

// TestProxySetSyncToCarriesReporter 热更新路径：syncTo 新建的代理沿用集合注入的
// reporter（Manager 字段只在 Start 时读取一次，之后热更新新建代理不上报会漏统计）。
func TestProxySetSyncToCarriesReporter(t *testing.T) {
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, "ok")
	}))
	defer backend.Close()

	var mu sync.Mutex
	var count int
	reporter := func(string, string, string, string, string, string, int, int64, int64, int64) {
		mu.Lock()
		defer mu.Unlock()
		count++
	}
	reporterSet := newCORSProxySet("srv-1", "一号服务器", reporter)
	set := reporterSet
	t.Cleanup(set.Close)

	host, port := backendHostPort(t, backend.URL)
	svcs := []configstore.Service{{
		ID: "s1", Name: "s1", LocalHost: host, LocalPort: port, Enabled: true,
	}}

	if err := set.syncTo(svcs); err != nil {
		t.Fatalf("syncTo 失败: %v", err)
	}
	p := set.get("s1")
	if p == nil {
		t.Fatal("syncTo 应为 enabled HTTP 服务新建代理")
	}
	if p.set.reporter == nil {
		t.Fatal("syncTo 新建的代理应携带集合的 reporter")
	}
	if body := readBody(t, proxyGET(t, p, "/", nil)); body != "ok" {
		t.Fatalf("代理应正常转发，得到 %q", body)
	}
	mu.Lock()
	defer mu.Unlock()
	if count < 1 {
		t.Fatalf("经热更新新建代理的请求应上报，实际 %d 条", count)
	}
}
