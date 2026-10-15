// probe.go 独立的 FRPS 控制连通性探测：
// 构造一次性无代理的 client.Service 与运行实例完全隔离（不触碰 Manager、
// 配置、服务器状态与统计），登录成功（控制连接+鉴权通过）即判定可达。
//
// 资源生命周期：frp 默认 Connector 在登录读阶段不响应 ctx 取消（ctx 只约束
// dial），因此探测使用 ConnectorCreator 包装器跟踪每次登录建立的底层连接，
// 超时/取消后 shutdown 强制关闭以中断在途登录读——每个返回路径（成功/失败/
// 超时/排队超时）都先完成客户端收尾再返回，探测整体有界（排队等待也计入时限）。
package tunnel

import (
	"context"
	"log/slog"
	"net"
	"strings"
	"sync"
	"time"

	"github.com/fatedier/frp/client"
	"github.com/fatedier/frp/pkg/config/source"
	v1 "github.com/fatedier/frp/pkg/config/v1"

	"frpdesk/internal/configstore"
)

// probeTimeout 单次探测的整体时限（含并发排队等待）：超时按「连接超时」分类。
// 包级变量便于测试缩短（真实探测 8s 覆盖慢网络登录握手）。
var probeTimeout = 8 * time.Second

// probePollInterval 登录成功轮询间隔（IsConnected 是 ctlMu 保护的快照读，开销可忽略）。
const probePollInterval = 100 * time.Millisecond

// probeConcurrency 并发探测上限：防止界面上重复触发堆叠大量临时 frpc 实例。
var probeConcurrency = make(chan struct{}, 2)

// ServerConnectionTestResult 单台服务器连通性探测结果（前端契约，全小驼峰）。
type ServerConnectionTestResult struct {
	Success   bool   `json:"success"`
	Message   string `json:"message"`
	LatencyMs int64  `json:"latencyMs"`
}

// probeConnector 包装 frp 默认 Connector：跟踪每次登录尝试建立的底层连接。
// frp 的登录读阻塞在已建立连接上且不响应 ctx 取消，shutdown() 强制关闭全部
// 跟踪连接与多路复用会话以中断在途读（这是探测超时能及时返回的关键）。
// 仅用于探测的临时 Service，不影响正式 Manager 的默认 Connector。
type probeConnector struct {
	client.Connector
	mu     sync.Mutex
	conns  []net.Conn
	closed bool
}

// 生命周期与并发约定（底层 default Connector 的 muxSession 仅 Open 写一次、
// Close/Connect 只读，因此只需保证 Open 与 Close 互斥、Close 幂等且不可逆）：
//   - Open：持锁执行并先检查关闭标志（Close 先到则返回 net.ErrClosed，杜绝
//     「closed 后仍建会话且底层 closeOnce 已消费无法再关」的泄漏）；
//   - Connect：底层调用不持锁（登录读挂起时依赖 Close 关闭连接解阻，持锁会让
//     Close 无法进入），调用前检查关闭标志，底层返回后持锁复检——期间发生过
//     Close 则关闭新连接并按 net.ErrClosed 返回，否则才登记；
//   - Close：持锁幂等，先关全部已登记连接（中断在途登录读）再关底层会话。

func (c *probeConnector) Open() error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.closed {
		return net.ErrClosed
	}
	return c.Connector.Open()
}

// probeTracker 持有当前登录尝试的包装器（frp 每次重试都会新建 Connector）。
// stopped 为粘性标志：shutdown 先于任何 creator 调用时，后续创建的包装器
// 也以「已关闭」形态返回（Open/Connect 立即 net.ErrClosed），杜绝竞态泄漏。
type probeTracker struct {
	mu      sync.Mutex
	stopped bool
	current *probeConnector
}

// creator 返回注入 ServiceOptions 的 ConnectorCreator。
func (t *probeTracker) creator() func(context.Context, *v1.ClientCommonConfig) client.Connector {
	return func(connCtx context.Context, connCfg *v1.ClientCommonConfig) client.Connector {
		t.mu.Lock()
		defer t.mu.Unlock()
		if t.stopped {
			return &probeConnector{Connector: client.NewConnector(connCtx, connCfg), closed: true}
		}
		w := &probeConnector{Connector: client.NewConnector(connCtx, connCfg)}
		t.current = w
		return w
	}
}

// shutdown 置粘性停止标志并强制关闭当前登录尝试的全部连接（无尝试则仅置标志）。
func (t *probeTracker) shutdown() {
	t.mu.Lock()
	t.stopped = true
	w := t.current
	t.mu.Unlock()
	if w != nil {
		w.shutdown()
	}
}

func (c *probeConnector) Connect() (net.Conn, error) {
	c.mu.Lock()
	if c.closed {
		c.mu.Unlock()
		return nil, net.ErrClosed
	}
	c.mu.Unlock()

	conn, err := c.Connector.Connect()
	if err != nil {
		return nil, err
	}

	// 底层返回后持锁复检：阻塞期间发生过 Close 则关闭新连接（防漏关）并按已关闭返回
	c.mu.Lock()
	if c.closed {
		_ = conn.Close()
		c.mu.Unlock()
		return nil, net.ErrClosed
	}
	c.conns = append(c.conns, conn)
	c.mu.Unlock()
	return conn, nil
}

// Close 关闭全部跟踪连接与底层会话（幂等；与登录流程的 defer Close 共用本锁）。
func (c *probeConnector) Close() error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.closed {
		return nil
	}
	c.closed = true
	for _, conn := range c.conns {
		_ = conn.Close() // 强制中断在途登录读
	}
	return c.Connector.Close()
}

// shutdown 强制关闭全部跟踪连接与底层会话（幂等）。
func (c *probeConnector) shutdown() {
	_ = c.Close()
}

// TestServerConnection 独立探测一台已保存服务器的 FRPS 控制连通性。
// 与 Service.Enabled 无关、与运行实例完全隔离：不调用 Reconcile、不改配置、
// 不影响服务器状态与统计；每次探测构造一个无代理的临时 frpc（LoginFailExit=true，
// 登录失败立即返回），登录成功即 cancel 并等待完整收尾。
// 返回的 message 面向用户（原始诊断进 slog）；Success 仅代表 FRPS 控制连接与
// 鉴权通过，不代表 HTTP 域名解析/业务端口可用。
func TestServerConnection(server configstore.ServerConfig) ServerConnectionTestResult {
	started := time.Now()
	deadline := started.Add(probeTimeout)
	res := ServerConnectionTestResult{}

	// 并发排队也纳入整体时限（信号量等待发生在计时窗口内）
	select {
	case probeConcurrency <- struct{}{}:
		defer func() { <-probeConcurrency }()
	case remaining := <-time.After(time.Until(deadline)):
		_ = remaining
		slog.Info("[探测] 排队超时", "server", server.Name, "id", server.ID)
		res.Message = "探测排队超时，请稍后重试"
		return res
	}

	if strings.TrimSpace(server.ServerAddr) == "" {
		res.Message = "服务器地址为空"
		return res
	}

	// 与 buildCommon 同源：auth/TLS/协议一致；探测要求首登失败立即退出
	common := buildCommon(server)
	loginFailExit := true
	common.LoginFailExit = &loginFailExit

	// 剩余时限内创建登录 ctx；ConnectorCreator 注入包装器以便超时后强制中断
	ctx, cancel := context.WithDeadline(context.Background(), deadline)
	defer cancel()
	tracker := &probeTracker{}

	configSource := source.NewConfigSource()
	if err := configSource.ReplaceAll(nil, nil); err != nil { // 无代理：不注册任何隧道
		res.Message = "初始化探测客户端失败"
		slog.Warn("[探测] 配置源初始化失败", "server", server.Name, "id", server.ID, "err", err)
		return res
	}
	svc, err := client.NewService(client.ServiceOptions{
		Common:                 common,
		ConfigSourceAggregator: source.NewAggregator(configSource),
		ConnectorCreator:       tracker.creator(), // 包装器：超时/取消后 shutdown 强制中断在途登录读
	})
	if err != nil {
		res.Message = "创建探测客户端失败"
		slog.Warn("[探测] 创建 frpc 失败", "server", server.Name, "id", server.ID, "err", err)
		return res
	}

	done := make(chan error, 1)
	go func() {
		done <- svc.Run(ctx)
	}()

	slog.Info("[探测] 开始", "server", server.Name, "id", server.ID,
		"addr", server.ServerAddr, "port", server.ServerPort)
	ticker := time.NewTicker(probePollInterval)
	defer ticker.Stop()
	for {
		select {
		case runErr := <-done:
			// 首登失败即退出（Run 已自然收尾）：按原始错误分类为用户可读消息
			slog.Info("[探测] 登录失败", "server", server.Name, "id", server.ID,
				"addr", server.ServerAddr, "port", server.ServerPort,
				"elapsed", time.Since(started), "rawErr", runErr)
			res.Message = classifyProbeFailure(runErr)
			return res
		case <-ctx.Done():
			// 整体时限到：强制中断在途登录读，然后等待 Run 完整收尾再返回
			tracker.shutdown()
			<-done
			slog.Info("[探测] 超时", "server", server.Name, "id", server.ID,
				"addr", server.ServerAddr, "port", server.ServerPort, "timeout", probeTimeout)
			res.Message = "连接超时（8 秒内未完成登录，请检查地址与端口）"
			return res
		case <-ticker.C:
			if svc.IsConnected() {
				latency := time.Since(started) // 登录完成时刻：不含后续清理耗时
				cancel()
				tracker.shutdown() // 探测连接与登录流一并收尾
				<-done             // 等待 Run 完整收尾（cancel+shutdown 保证退出）
				res.Success = true
				res.LatencyMs = latency.Milliseconds()
				res.Message = "FRPS 控制连接与鉴权通过（不代表 HTTP 域名或业务端口可用）"
				slog.Info("[探测] 成功", "server", server.Name, "id", server.ID,
					"latencyMs", res.LatencyMs)
				return res
			}
		}
	}
}

// classifyProbeFailure 把 frp 登录失败的原始错误归类为用户可读消息。
func classifyProbeFailure(err error) string {
	if err == nil {
		return "连接已关闭"
	}
	msg := err.Error()
	switch {
	case strings.Contains(msg, "connection refused"):
		return "无法连接 frps：地址或端口不可达（连接被拒绝）"
	case strings.Contains(msg, "i/o timeout"),
		strings.Contains(msg, "context deadline exceeded"),
		strings.Contains(msg, "context canceled"):
		return "无法连接 frps：连接超时"
	case strings.Contains(msg, "token"), strings.Contains(msg, "auth"):
		return "frps 拒绝登录：鉴权失败（请检查 authToken）"
	default:
		return "无法连接 frps：登录被拒绝或连接中断"
	}
}
