package tunnel

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	"frpdesk/internal/configstore"
)

// TestReproFrpCtlRace 回归：frp client.Service 的 ctl 字段无锁读竞态
// （keepControllerWorking 读 svr.ctl 与 stop() 持 ctlMu 写 svr.ctl=nil 并发）。
// 已在本地 fork third_party/frp 修复（getControl 快照读，见 client/service.go
// 「frpdesk fork fix」注释）；本测试以连接→停止→立即重连循环压测该交错，
// -race 下必须稳定通过（修复前循环内必现 DATA RACE，复现记录见
// /tmp/frpdesk-race-repro.log）。
func TestReproFrpCtlRace(t *testing.T) {
	bind, _ := startTestFRPS(t)
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, "ok")
	}))
	defer backend.Close()
	host, port := backendHostPort(t, backend.URL)

	server := configstore.ServerConfig{ID: "r", Name: "repro", ServerAddr: "127.0.0.1", ServerPort: bind, HTTPEnabled: true, BaseDomain: "x.test", Scheme: "http"}
	svcs := []configstore.Service{{ID: "tr", ServerID: "r", Name: "web", LocalHost: host, LocalPort: port, Subdomain: "w", Enabled: true, Type: configstore.TypeHTTP}}

	for i := 0; i < 10; i++ {
		m := NewManager()
		m.Reconcile(server, svcs)
		waitForLong(t, fmt.Sprintf("round %d running", i), func() bool {
			return m.ServerStatuses()["r"].State == configstore.StateRunning
		})
		// login 成功后立即停 + 立即重启（交错触发 stop 写与 keep 读/新 login 写）
		done := make(chan struct{})
		go func() {
			defer close(done)
			m.Reconcile(server, nil) // enabled=0 → 停止（cancel 终止重试）
		}()
		m.Reconcile(server, svcs) // 与停止交错：热更新路径或停止后重新启动
		<-done
		m.Reconcile(server, svcs) // 停止后重新启动
		m.StopAll()
	}
}
