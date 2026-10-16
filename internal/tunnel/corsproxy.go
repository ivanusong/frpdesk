package tunnel

import (
	"bufio"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/http/httputil"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"

	"frpdesk/internal/configstore"
	"frpdesk/internal/i18n"
)

// proxyListenHost 跨域代理固定在 loopback 监听，只对本机 frp 客户端可见。
const proxyListenHost = "127.0.0.1"

// RequestReporter 单次 HTTP 请求统计上报回调（宿主如 AppService 桥接到
// internal/stats.Collector）。serverID/serviceID 为请求发生时所在实例与隧道；
// serverName 为该请求开始时所属服务器的名字快照（请求 handler 入口捕获：
// 服务器改名后的新请求用最新名，在途/历史请求保留发生时旧名，不按当前归属
// 回填）。path 为完整 path+query，ua 为请求 User-Agent（可空）。
// status/duration/字节口径由代理层保证，聚合语义见 stats 包。
// nil = 不上报（统计未接入，代理行为不变，测试兼容）。
type RequestReporter func(serverID, serverName, serviceID, method, path, ua string, status int, durationMs int64, bytesIn, bytesOut int64)

// listenCORSProxy 创建代理监听；抽成变量便于测试模拟监听失败（端口耗尽）场景。
var listenCORSProxy = func() (net.Listener, error) {
	return net.Listen("tcp", net.JoinHostPort(proxyListenHost, "0"))
}

// corsProxy 单个服务的本地代理：本实例独占的 loopback HTTP 监听，
// 经 httputil.ReverseProxy 转发到原 LocalHost:LocalPort（明文 HTTP），
// 注入 CORS 头并把 Host 改写为本地目标（开发服务 allowedHosts 兼容）。
// 生命周期完全随创建它的启动实例。
//
// http.Server.Close 不关闭已 hijack 的连接（WebSocket 升级），
// 因此用 trackedListener 记录已接受连接，Close 时一并关闭，避免隧道退出后残留。
type corsProxy struct {
	target    string        // 原后端地址 host:port
	set       *corsProxySet // 所属集合：serverID 固定、serverName 可变（改名刷新）、reporter
	serviceID string        // 所属隧道（统计上报标识）
	listener  net.Listener
	server    *http.Server
	transport *http.Transport // 本实例独占的上游连接池，Close 时随实例释放

	mu        sync.Mutex
	conns     map[*trackedConn]struct{} // 已接受且未关闭的连接
	closeOnce sync.Once
}

// newCORSProxy 创建代理并开始监听。失败时不需要调用方清理。
// 归属信息（serverID/serverName/reporter）取自集合；serverName 经集合可更新，
// 代理不固定快照（避免服务器改名后新请求永久上报旧名）。
func newCORSProxy(set *corsProxySet, localHost string, localPort int, serviceID string) (*corsProxy, error) {
	target := net.JoinHostPort(localHost, strconv.Itoa(localPort))
	ln, err := listenCORSProxy()
	if err != nil {
		// 双语错误：状态/错误 UI 按当前语言，环形日志经 i18n.ErrText 固定英文
		return nil, i18n.LErrWrap("err.listenProxyFail", err)
	}

	// 上游连接池每实例独占：不污染全局 DefaultTransport，Close 时能关闭自己的
	// idle 连接；Proxy=nil 保持 frp 原来的直连语义（不读环境代理），
	// DisableCompression 避免代理在客户端未要求时自动协商 gzip 改变响应字节。
	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.Proxy = nil
	transport.DisableCompression = true

	p := &corsProxy{target: target, set: set, serviceID: serviceID, transport: transport, conns: map[*trackedConn]struct{}{}}
	targetURL := &url.URL{Scheme: "http", Host: target}

	rp := &httputil.ReverseProxy{
		Transport: transport,
		Rewrite: func(r *httputil.ProxyRequest) {
			rawQuery := r.In.URL.RawQuery // SetURL 前 net/http 会清理非法 query，统一恢复原始字节
			r.SetURL(targetURL)
			r.Out.URL.RawQuery = rawQuery
			// 保留原始 query 字节；Host 改为实际原目标（如 127.0.0.1:5173）：
			// Vite 等开发服务的 server.allowedHosts 只接受本机 Host，公网域名 Host 会被拒；
			// 请求 Origin 经默认转发保留，原公网 Host 改经 X-Forwarded-Host 传递
			r.Out.Host = target
			// Rewrite 默认剥离转发头；按直连语义透传已有值，不额外追加；
			// X-Forwarded-Host 无值时补原公网 Host（网关已提供的保留）
			for _, name := range []string{"Forwarded", "X-Forwarded-For", "X-Forwarded-Host", "X-Forwarded-Proto"} {
				if v := r.In.Header.Values(name); len(v) > 0 {
					r.Out.Header[name] = v
				}
			}
			if len(r.Out.Header.Values("X-Forwarded-Host")) == 0 {
				r.Out.Header.Set("X-Forwarded-Host", r.In.Host)
			}
		},
		ModifyResponse: func(resp *http.Response) error {
			origin := resp.Request.Header.Get("Origin")
			if origin == "" {
				// 无 Origin 也标 Vary：防止响应被缓存后又被带 Origin 的 fetch 命中
				mergeVary(resp.Header, "Origin")
				return nil
			}
			// 覆盖后端冲突的 Allow-Origin/Credentials，避免重复头被浏览器拒绝
			setCORSResponseHeaders(resp.Header, origin)
			return nil
		},
		ErrorHandler: func(w http.ResponseWriter, r *http.Request, err error) {
			if origin := r.Header.Get("Origin"); origin != "" {
				// 502 也要带 CORS 头，浏览器端才能读到错误
				setCORSResponseHeaders(w.Header(), origin)
			} else {
				mergeVary(w.Header(), "Origin")
			}
			slog.Warn("[cors] failed to forward to upstream", "target", target, "err", err)
			w.WriteHeader(http.StatusBadGateway)
		},
		// 立即 flush：大文件流式转发与 SSE 都及时到达客户端
		FlushInterval: -1,
	}

	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		serverName := p.set.serverDisplayName() // 请求开始时的名字快照（改名不回填在途请求）
		origin := r.Header.Get("Origin")
		if origin != "" && r.Method == http.MethodOptions {
			if reqMethod := r.Header.Get("Access-Control-Request-Method"); reqMethod != "" {
				writeCORSPreflight(w, r, origin, reqMethod) // 预检本地应答，不触达后端
				// 预检也计一次访问：204，无 body 读写，字节恒为 0
				p.report(serverName, r.Method, r.URL.RequestURI(), r.UserAgent(), http.StatusNoContent, start, 0, 0)
				return
			}
		}
		// 统计挂载：请求 body 与响应 body 计数，收尾时上报一次。
		// 必须包在 rp 外层，统计才同时覆盖转发成功、上游失败与 101 升级路径。
		cw := &countingResponseWriter{ResponseWriter: w, proxy: p, method: r.Method, uri: r.URL.RequestURI(), ua: r.UserAgent(), serverName: serverName, start: start}
		if r.Body != nil {
			cb := &countingBody{rc: r.Body}
			r.Body = cb
			cw.body = cb
		}
		defer cw.finish()
		rp.ServeHTTP(cw, r)
	})

	p.listener = ln
	p.server = &http.Server{Handler: handler}
	go func() {
		if err := p.server.Serve(&trackedListener{Listener: ln, proxy: p}); err != nil && err != http.ErrServerClosed {
			slog.Warn("[proxy] HTTP local proxy listener exited", "target", target, "err", err)
		}
	}()
	return p, nil
}

// report 上报一次请求统计；reporter 未注入（Manager 字段留空）时静默跳过。
// serverName 为请求 handler 入口捕获的名字快照（见 handler）。
// 统计生命周期为 App 级内存态（不随隧道启停清零），此处只负责喂数据。
func (p *corsProxy) report(serverName, method, uri, ua string, status int, start time.Time, bytesIn, bytesOut int64) {
	if p.set.reporter == nil {
		return
	}
	p.set.reporter(p.set.serverID, serverName, p.serviceID, method, uri, ua, status, time.Since(start).Milliseconds(), bytesIn, bytesOut)
}

// countingBody 请求 body 计数包装：bytesIn 按实际读取字节计，不信任 ContentLength。
type countingBody struct {
	rc io.ReadCloser
	n  int64
}

func (b *countingBody) Read(p []byte) (int, error) {
	n, err := b.rc.Read(p)
	b.n += int64(n)
	return n, err
}

func (b *countingBody) Close() error { return b.rc.Close() }

// countingResponseWriter 响应计数包装：WriteHeader 捕获状态码、Write 计数 body 字节
// （不含状态行/头）。必须透传 Flusher/Hijacker——FlushInterval=-1 的流式转发与
// WebSocket 升级都依赖 ReverseProxy 对这两个接口的类型断言；刻意不实现 ReaderFrom，
// 避免 sendfile 类捷径绕过 Write 计数。
type countingResponseWriter struct {
	http.ResponseWriter
	status     int           // 首次 WriteHeader 捕获的状态码；0 = 尚未写头
	bytes      int64         // 响应 body 已写字节
	body       *countingBody // 请求 body 计数（可为 nil）
	reported   bool          // 升级路径已在 Hijack 处上报，收尾不重复
	proxy      *corsProxy
	method     string
	uri        string
	ua         string
	serverName string // 请求开始时的服务器名快照
	start      time.Time
}

func (w *countingResponseWriter) WriteHeader(code int) {
	if w.status == 0 {
		w.status = code
	}
	w.ResponseWriter.WriteHeader(code)
}

func (w *countingResponseWriter) Write(b []byte) (int, error) {
	if w.status == 0 {
		w.status = http.StatusOK // net/http 语义：未写头先 Write 视为 200
	}
	n, err := w.ResponseWriter.Write(b)
	w.bytes += int64(n)
	return n, err
}

func (w *countingResponseWriter) Flush() {
	if f, ok := w.ResponseWriter.(http.Flusher); ok {
		f.Flush()
	}
}

// Hijack 透传升级。ReverseProxy 的 101 状态行经 hijack 后的裸连接直写，本包装器
// 看不到 WriteHeader(101)，因此在 Hijack 成功处单独上报：计一次访问 status=101、
// 字节 0、duration 到 101 应答为止。
// 已知限制：升级后的裸流（WebSocket 帧）不经本包装器，不计字节。
func (w *countingResponseWriter) Hijack() (net.Conn, *bufio.ReadWriter, error) {
	hj, ok := w.ResponseWriter.(http.Hijacker)
	if !ok {
		return nil, nil, fmt.Errorf("ResponseWriter does not support Hijack: %T", w.ResponseWriter)
	}
	conn, brw, err := hj.Hijack()
	if err == nil && !w.reported {
		w.reported = true
		w.proxy.report(w.serverName, w.method, w.uri, w.ua, http.StatusSwitchingProtocols, w.start, w.bodyBytes(), 0)
	}
	return conn, brw, err
}

// finish handler 收尾（含 panic 路径）上报一次；升级请求已在 Hijack 处上报。
func (w *countingResponseWriter) finish() {
	if w.reported {
		return
	}
	w.reported = true
	status := w.status
	if status == 0 {
		status = http.StatusOK // handler 未显式写头：对齐 net/http 默认 200
	}
	w.proxy.report(w.serverName, w.method, w.uri, w.ua, status, w.start, w.bodyBytes(), w.bytes)
}

func (w *countingResponseWriter) bodyBytes() int64 {
	if w.body == nil {
		return 0
	}
	return w.body.n
}

// addr 代理监听地址（host:port）。
func (p *corsProxy) addr() string {
	return p.listener.Addr().String()
}

// port 代理监听端口（frp 侧 LocalPort 用）。
func (p *corsProxy) port() int {
	if addr, ok := p.listener.Addr().(*net.TCPAddr); ok {
		return addr.Port
	}
	return 0
}

// Close 释放代理：关闭监听、在途请求连接、hijack 后的升级连接与上游 idle 连接。幂等。
func (p *corsProxy) Close() {
	p.closeOnce.Do(func() {
		_ = p.server.Close()   // 先置 inShutdown，让 Serve 识别为正常关闭
		_ = p.listener.Close() // 显式关监听：Serve 尚未注册 listener 时也不留未关闭窗口
		p.mu.Lock()
		conns := make([]*trackedConn, 0, len(p.conns))
		for c := range p.conns {
			conns = append(conns, c)
		}
		p.conns = nil
		p.mu.Unlock()
		for _, c := range conns {
			_ = c.Close() // 含 hijack 的升级连接（server.Close 不负责）
		}
		p.transport.CloseIdleConnections() // 本实例独占连接池：闲置上游连接随之关闭
		slog.Info("[proxy] released HTTP local proxy", "listen", p.addr(), "target", p.target)
	})
}

func (p *corsProxy) track(c *trackedConn) {
	p.mu.Lock()
	if p.conns == nil { // 已关闭：连接不再保留，直接关掉
		p.mu.Unlock()
		_ = c.Close()
		return
	}
	p.conns[c] = struct{}{}
	p.mu.Unlock()
}

func (p *corsProxy) forget(c *trackedConn) {
	p.mu.Lock()
	if p.conns != nil {
		delete(p.conns, c)
	}
	p.mu.Unlock()
}

// trackedListener 记录所有已接受连接，使代理关闭时能关闭
// hijack 后不再受 http.Server 管理的升级连接。
type trackedListener struct {
	net.Listener
	proxy *corsProxy
}

func (l *trackedListener) Accept() (net.Conn, error) {
	c, err := l.Listener.Accept()
	if err != nil {
		return nil, err
	}
	tc := &trackedConn{Conn: c, proxy: l.proxy}
	l.proxy.track(tc)
	return tc, nil
}

// trackedConn 连接关闭时从代理登记表中移除，避免长时间运行下登记表只增不减。
type trackedConn struct {
	net.Conn
	proxy *corsProxy
	once  sync.Once
}

func (c *trackedConn) Close() error {
	var err error
	c.once.Do(func() {
		err = c.Conn.Close()
		c.proxy.forget(c)
	})
	return err
}

// corsProxySet 一次实例创建的本地代理集合（serviceID → 代理），绑定单台服务器。
// 实例独占：释放只影响自己，绝不触碰其他实例的资源。
type corsProxySet struct {
	mu         sync.Mutex
	proxies    map[string]*corsProxy
	closed     bool
	serverID   string          // 所属服务器 ID（不可变）
	serverName string          // 所属服务器名（可随改名刷新；mu 保护）
	reporter   RequestReporter // 请求统计上报回调（构造后只读，可为 nil）
}

func newCORSProxySet(serverID, serverName string, reporter RequestReporter) *corsProxySet {
	return &corsProxySet{proxies: map[string]*corsProxy{}, serverID: serverID, serverName: serverName, reporter: reporter}
}

// setServerName 刷新服务器展示名（服务器改名后由热更新流程调用）。
func (s *corsProxySet) setServerName(name string) {
	s.mu.Lock()
	s.serverName = name
	s.mu.Unlock()
}

// serverDisplayName 返回当前服务器名（请求 handler 入口快照用）。
func (s *corsProxySet) serverDisplayName() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.serverName
}

func (s *corsProxySet) add(serviceID string, p *corsProxy) {
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		p.Close()
		return
	}
	s.proxies[serviceID] = p
	s.mu.Unlock()
}

func (s *corsProxySet) get(serviceID string) *corsProxy {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.proxies[serviceID]
}

// Close 释放集合内全部代理，幂等。
func (s *corsProxySet) Close() {
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return
	}
	s.closed = true
	proxies := make([]*corsProxy, 0, len(s.proxies))
	for _, p := range s.proxies {
		proxies = append(proxies, p)
	}
	s.proxies = nil
	s.mu.Unlock()
	for _, p := range proxies {
		p.Close()
	}
}

// localAddr 返回服务在 frp 侧应使用的本地目标：
// 有本地代理的服务（enabled HTTP）指向本实例的 loopback 代理，其余原样直连。
func (s *corsProxySet) localAddr(svc configstore.Service) (string, int) {
	if p := s.get(svc.ID); p != nil {
		return proxyListenHost, p.port()
	}
	return svc.LocalHost, svc.LocalPort
}

// syncTo 把集合同步到目标态：目标 = enabled 的 HTTP 服务（跨域内置，不看旧 cors 字段；
// TCP/UDP 始终直连不建代理）。
//   - 保留：已有同 ID 且 target（原后端 host:port）相同的代理，监听端口不变
//     （配置稳定，frp 热更新按名 diff 才不会误杀重注册）；
//   - 移除：服务删除/禁用/切成 TCP/UDP/target 变化的旧代理，关闭并从集合移除；
//   - 新建：目标态中缺失的代理。
//
// 必须先于 buildProxyCfgs 调用：先移除，localAddr 才不会把已停用/切成 TCP/UDP 的服务
// 仍指向已关闭的 loopback。新建监听失败返回错误（已创建的保留在集合中，由调用方
// 决定回退；集合随实例释放时统一关闭，不泄漏）。
func (s *corsProxySet) syncTo(svcs []configstore.Service) error {
	// 目标态：ID → 期望的 target（原后端 host:port）；仅 enabled 的 HTTP 隧道（含旧缺省空类型）
	want := map[string]string{}
	for i := range svcs {
		svc := svcs[i]
		if svc.Enabled && svc.IsHTTP() {
			want[svc.ID] = net.JoinHostPort(svc.LocalHost, strconv.Itoa(svc.LocalPort))
		}
	}

	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return i18n.LErr("err.proxySetClosed")
	}
	var removed []*corsProxy
	for id, p := range s.proxies {
		if t, ok := want[id]; ok && t == p.target {
			continue // target 未变：保留复用
		}
		removed = append(removed, p)
		delete(s.proxies, id)
	}
	s.mu.Unlock()
	for _, p := range removed {
		slog.Info("[proxy] removed HTTP local proxy", "listen", p.addr(), "target", p.target)
		p.Close()
	}

	for i := range svcs {
		svc := svcs[i]
		if !svc.Enabled || !svc.IsHTTP() || s.get(svc.ID) != nil {
			continue
		}
		p, err := newCORSProxy(s, svc.LocalHost, svc.LocalPort, svc.ID)
		if err != nil {
			return i18n.LErrWrap("err.svcProxyFail", err, svc.Name)
		}
		s.add(svc.ID, p)
		slog.Info("[proxy] created HTTP local proxy (built-in CORS + Host rewrite)",
			"service", svc.Name, "id", svc.ID, "listen", p.addr(), "target", p.target)
	}
	return nil
}

// setCORSResponseHeaders 注入显式跨域允许头：回显请求 Origin（不使用 *，兼容凭据），
// 覆盖后端已有的 Allow-Origin/Credentials，并合并 Vary: Origin。
func setCORSResponseHeaders(h http.Header, origin string) {
	h.Del("Access-Control-Allow-Origin")
	h.Del("Access-Control-Allow-Credentials")
	h.Set("Access-Control-Allow-Origin", origin)
	h.Set("Access-Control-Allow-Credentials", "true")
	mergeVary(h, "Origin")
}

// writeCORSPreflight 本地应答 CORS 预检（204，不请求后端）：
// 回显请求的方法与 headers，保证 Authorization/Content-Type 等自定义头可用。
func writeCORSPreflight(w http.ResponseWriter, r *http.Request, origin, reqMethod string) {
	h := w.Header()
	setCORSResponseHeaders(h, origin)
	h.Set("Access-Control-Allow-Methods", reqMethod)
	if reqHeaders := r.Header.Get("Access-Control-Request-Headers"); reqHeaders != "" {
		h.Set("Access-Control-Allow-Headers", reqHeaders)
	} else {
		h.Set("Access-Control-Allow-Headers", "Authorization, Content-Type")
	}
	// 预检结果随 Origin/请求方法/请求头变化，缓存不得跨这些维度复用
	mergeVary(h, "Access-Control-Request-Method", "Access-Control-Request-Headers")
	w.WriteHeader(http.StatusNoContent)
	slog.Debug("[cors] local preflight response", "origin", origin, "method", reqMethod)
}

// mergeVary 把字段合并进已有 Vary：大小写不敏感去重、保留原顺序与原始写法；
// 已有 Vary: * 时不再追加。
func mergeVary(h http.Header, fields ...string) {
	seen := map[string]bool{}
	var out []string
	for _, v := range h.Values("Vary") {
		for _, f := range strings.Split(v, ",") {
			f = strings.TrimSpace(f)
			if f == "" {
				continue
			}
			key := strings.ToLower(f)
			if seen[key] {
				continue
			}
			seen[key] = true
			out = append(out, f)
		}
	}
	if seen["*"] {
		return
	}
	for _, f := range fields {
		if key := strings.ToLower(f); !seen[key] {
			seen[key] = true
			out = append(out, f)
		}
	}
	if len(out) == 0 {
		return
	}
	h.Del("Vary")
	h.Set("Vary", strings.Join(out, ", "))
}
