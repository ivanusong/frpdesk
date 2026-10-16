package tunnel

import (
	"sort"
	"strings"

	"github.com/fatedier/frp/client"

	"frpdesk/internal/i18n"
)

// ProxyStatus 单个代理的运行状态（简单字段，前端直接消费）。
// State 取值：unregistered（未启动/未注册）、starting（注册中）、
// registered（已注册）、failed（注册失败）。
type ProxyStatus struct {
	ServiceID string `json:"serviceId"`
	State     string `json:"state"`
	Friendly  string `json:"friendly,omitempty"` // 中文失败原因（可空）
	RawErr    string `json:"rawErr,omitempty"`   // frp 原始错误（保留排查）
}

// 逐服务状态归一化（frp ProxyPhase → 内部 state）。
const (
	proxyStateUnregistered = "unregistered"
	proxyStateStarting     = "starting"
	proxyStateRegistered   = "registered"
	proxyStateFailed       = "failed"
)

func mapProxyState(frpPhase string) string {
	switch frpPhase {
	case "running":
		return proxyStateRegistered
	case "new", "wait start":
		return proxyStateStarting
	case "start error", "check failed":
		return proxyStateFailed
	case "closed":
		return proxyStateUnregistered
	default:
		return proxyStateStarting
	}
}

// friendlyProxyErr 把 frp 的常见注册失败精准翻译成用户可读原因（当前语言）；
// 保留原始错误（RawErr）供排查。
// 注意：permission denied 不是端口占用，不得误归为「已被占用」。
func friendlyProxyErr(raw string) string {
	low := strings.ToLower(raw)
	switch {
	case strings.Contains(low, "port already used"):
		return i18n.T("proxy.portUsed")
	case strings.Contains(low, "address already in use"):
		return i18n.T("proxy.addrInUse")
	case strings.Contains(low, "port unavailable"):
		return i18n.T("proxy.portUnavailable")
	case strings.Contains(low, "port not allowed"):
		return i18n.T("proxy.portNotAllowed")
	case strings.Contains(low, "already exists"):
		return i18n.T("proxy.exists")
	case strings.Contains(low, "conflict"):
		return i18n.T("proxy.conflict")
	default:
		return ""
	}
}

// proxyStatuses 返回副本并按 ServiceID 稳定排序（避免 map 遍历顺序导致
// 相同采样被误判为变化而重复推送）。
func proxyStatuses(m map[string]ProxyStatus) []ProxyStatus {
	out := make([]ProxyStatus, 0, len(m))
	for _, st := range m {
		out = append(out, st)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ServiceID < out[j].ServiceID })
	return out
}

// sortProxyStatus 原地按 ServiceID 稳定排序。
func sortProxyStatus(list []ProxyStatus) {
	sort.Slice(list, func(i, j int) bool { return list[i].ServiceID < list[j].ServiceID })
}

// samplingFn 按 serviceID 拉取 frp 代理状态（生产绑定 StatusExporter，测试可桩替）。
type samplingFn = func(serviceID string) (phase string, errMsg string, ok bool)

// frpStatusFunc 生产实现：从 frp client.Service 的 StatusExporter 拉取。
func frpStatusFunc(svc *client.Service) samplingFn {
	exporter := svc.StatusExporter()
	return func(serviceID string) (string, string, bool) {
		ws, ok := exporter.GetProxyStatus(serviceID)
		if !ok {
			return "", "", false
		}
		return ws.Phase, ws.Err, true
	}
}

// sampleAll 对当前实例的全部服务做一次状态采样。
func sampleAll(svc *client.Service, ids []string, fn samplingFn) []ProxyStatus {
	if fn == nil || svc == nil {
		return nil
	}
	out := make([]ProxyStatus, 0, len(ids))
	for _, id := range ids {
		phase, errMsg, ok := fn("frpdesk-" + id)
		if !ok {
			out = append(out, ProxyStatus{ServiceID: id, State: proxyStateUnregistered})
			continue
		}
		st := ProxyStatus{ServiceID: id, State: mapProxyState(phase), RawErr: strings.TrimSpace(errMsg)}
		if st.State == proxyStateFailed {
			if f := friendlyProxyErr(st.RawErr); f != "" {
				st.Friendly = f
			}
		}
		out = append(out, st)
	}
	return out
}

// proxyStatusEqual 比较两次采样是否一致（避免重复推送）。
func proxyStatusEqual(a, b []ProxyStatus) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
