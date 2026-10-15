package tunnel

import (
	"errors"
	"net"
	"sync"
	"testing"
	"time"

	v1 "github.com/fatedier/frp/pkg/config/v1"
)

// ---------- probeConnector 确定性 mock 测试（不依赖网络/时间） ----------

// mockConn 最小 net.Conn 实现：只跟踪关闭状态。
type mockConn struct {
	mu     sync.Mutex
	closed bool
}

func (c *mockConn) Close() error {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.closed = true
	return nil
}

func (c *mockConn) isClosed() bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.closed
}

// stub 方法（net.Conn 其余接口）。
func (c *mockConn) Read(b []byte) (int, error)         { return 0, net.ErrClosed }
func (c *mockConn) Write(b []byte) (int, error)        { return 0, net.ErrClosed }
func (c *mockConn) LocalAddr() net.Addr                { return nil }
func (c *mockConn) RemoteAddr() net.Addr               { return nil }
func (c *mockConn) SetDeadline(t time.Time) error      { return nil }
func (c *mockConn) SetReadDeadline(t time.Time) error  { return nil }
func (c *mockConn) SetWriteDeadline(t time.Time) error { return nil }

// mockConnector 可控的底层 Connector：Open/Connect 行为可编程。
type mockConnector struct {
	mu         sync.Mutex
	openErr    error
	openCount  int
	connectFn  func() (net.Conn, error)
	closeCount int
}

func (m *mockConnector) Open() error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.openCount++
	return m.openErr
}

func (m *mockConnector) Connect() (net.Conn, error) {
	m.mu.Lock()
	fn := m.connectFn
	m.mu.Unlock()
	if fn != nil {
		return fn()
	}
	return &mockConn{}, nil
}

func (m *mockConnector) Close() error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.closeCount++
	return nil
}

func (m *mockConnector) stats() (openCount, closeCount int) {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.openCount, m.closeCount
}

// TestProbeConnectorCloseBeforeOpen Close 先于 Open：后续 Open 立即 net.ErrClosed
// 且不得触达底层（底层 closeOnce 已消费，新建会话将无法关闭而泄漏）。
func TestProbeConnectorCloseBeforeOpen(t *testing.T) {
	mock := &mockConnector{}
	w := &probeConnector{Connector: mock}

	if err := w.Close(); err != nil {
		t.Fatalf("Close 应成功: %v", err)
	}
	err := w.Open()
	if !errors.Is(err, net.ErrClosed) {
		t.Fatalf("Close 后 Open 应返回 net.ErrClosed，得到 %v", err)
	}
	if openCount, _ := mock.stats(); openCount != 0 {
		t.Fatalf("关闭后 Open 不得触达底层（会话将泄漏），实际 %d 次", openCount)
	}
}

// TestProbeConnectorConnectReturnedAfterClose Connect 底层返回晚于 Close：
// 新连接必须被立即关闭并按 net.ErrClosed 返回（防漏关——通用 wrapper 不假设
// 底层多路复用会代为关闭）。
func TestProbeConnectorConnectReturnedAfterClose(t *testing.T) {
	release := make(chan struct{})
	entered := make(chan struct{})
	conn := &mockConn{}
	mock := &mockConnector{connectFn: func() (net.Conn, error) {
		close(entered) // 已越过前置检查：确定性走「底层返回后二次检查」路径
		<-release      // 模拟 Connect 阻塞（登录读挂起）
		return conn, nil
	}}
	w := &probeConnector{Connector: mock}

	type result struct {
		c net.Conn
		e error
	}
	done := make(chan result, 1)
	go func() {
		c, err := w.Connect()
		done <- result{c, err}
	}()

	<-entered // 确保底层 Connect 已在阻塞中
	// Connect 阻塞期间 Close：closed 置位 + 已登记连接关闭
	if err := w.Close(); err != nil {
		t.Fatalf("Close 应成功: %v", err)
	}
	close(release) // 放行阻塞的底层 Connect

	r := <-done
	if !errors.Is(r.e, net.ErrClosed) {
		t.Fatalf("Close 后返回的连接应按 net.ErrClosed 拒绝，得到 %v", r.e)
	}
	if r.c != nil {
		t.Fatalf("返回的 conn 应为 nil（已由 wrapper 关闭）: %+v", r.c)
	}
	if !conn.isClosed() {
		t.Fatal("漏网的新连接必须被 wrapper 关闭")
	}
}

// TestProbeConnectorConnectCallCheckBeforeCall 调用前已关闭：Connect 快速返回
// net.ErrClosed，不触达底层。
func TestProbeConnectorConnectCallCheckBeforeCall(t *testing.T) {
	mock := &mockConnector{}
	w := &probeConnector{Connector: mock}
	_ = w.Close()

	c, err := w.Connect()
	if !errors.Is(err, net.ErrClosed) || c != nil {
		t.Fatalf("关闭后 Connect 应返回 net.ErrClosed，得到 %v/%v", c, err)
	}
}

// TestProbeTrackerShutdownBeforeCreator tracker.shutdown 先于 creator：
// 粘性停止标志生效——之后创建的包装器直接以已关闭形态返回（Open/Connect
// 立即 net.ErrClosed），不存在「shutdown 后仍造出活跃连接」的竞态窗口。
func TestProbeTrackerShutdownBeforeCreator(t *testing.T) {
	tracker := &probeTracker{}
	tracker.shutdown() // 无 current：仅置粘性标志

	w := tracker.creator()(t.Context(), &v1.ClientCommonConfig{})
	if w == nil {
		t.Fatal("creator 应返回已关闭形态的包装器")
	}
	if err := w.Open(); !errors.Is(err, net.ErrClosed) {
		t.Fatalf("sticky 关闭下 Open 应 net.ErrClosed，得到 %v", err)
	}
	c, err := w.Connect()
	if !errors.Is(err, net.ErrClosed) || c != nil {
		t.Fatalf("sticky 关闭下 Connect 应 net.ErrClosed，得到 %v/%v", c, err)
	}
}
