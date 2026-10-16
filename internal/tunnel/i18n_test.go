package tunnel

import (
	"errors"
	"strings"
	"testing"

	"frpdesk/internal/configstore"
	"frpdesk/internal/i18n"
)

// TestRefreshLocalizedMessages 语言切换时状态说明类消息按新语言重建；
// 错误详情保持发生时的原文；有变化时触发状态通知（托盘/前端刷新路径）。
func TestRefreshLocalizedMessages(t *testing.T) {
	t.Cleanup(func() { i18n.Set(i18n.ZH) })
	i18n.Set(i18n.ZH)
	m := NewManager()
	defer func() { m.StopAll() }()

	inst := &serverInstance{
		server: testServer("s", "甲", 7000), cancel: func() {}, done: closedDone(),
		state: configstore.StateStarting, message: i18n.T("msg.loggingIn"),
	}
	m.mu.Lock()
	m.insts["s"] = inst
	m.statuses["s"] = configstore.ServerStatus{ServerID: "s", State: configstore.StateStarting, Message: i18n.T("msg.loggingIn"), MessageKey: "msg.loggingIn"}
	// 错误详情：无 MessageKey，不得被重渲染（保留发生时原文，便于排查）
	m.statuses["e"] = configstore.ServerStatus{ServerID: "e", State: configstore.StateFailed, Message: "raw boom: port already used"}
	m.mu.Unlock()

	notified := 0
	m.OnStatus = func() { notified++ }
	i18n.Set(i18n.EN)
	m.RefreshLocalizedMessages()

	if st := m.ServerStatuses()["s"]; st.Message != "Logging in to frps" || st.MessageKey != "msg.loggingIn" {
		t.Fatalf("状态说明未按新语言重建: %+v", st)
	}
	if m.insts["s"].message != "Logging in to frps" {
		t.Fatalf("实例消息未同步: %q", m.insts["s"].message)
	}
	if st := m.ServerStatuses()["e"]; st.Message != "raw boom: port already used" {
		t.Fatalf("错误详情不得被重渲染: %+v", st)
	}
	if notified == 0 {
		t.Fatal("重建状态说明后应触发状态通知")
	}
	// 幂等：已同语言无变化不再通知
	notified = 0
	m.RefreshLocalizedMessages()
	if notified != 0 {
		t.Fatalf("无变化不应重复通知，got %d", notified)
	}
}

// TestLocalizedTunnelMessages 隧道包用户可见文案双语：状态消息与失败友好原因
// 随进程语言切换，frp 原始错误文本不受影响（保留在 RawErr）。
func TestLocalizedTunnelMessages(t *testing.T) {
	t.Cleanup(func() { i18n.Set(i18n.ZH) })

	// 状态消息：zh / en 两套
	i18n.Set(i18n.ZH)
	if got := i18n.T("msg.loggingIn"); got != "正在登录 frps" {
		t.Fatalf("zh loggingIn = %q", got)
	}
	i18n.Set(i18n.EN)
	if got := i18n.T("msg.loggingIn"); !strings.Contains(got, "frps") {
		t.Fatalf("en loggingIn = %q", got)
	}
	if got := i18n.T("msg.reconnecting"); !strings.Contains(got, "reconnect") {
		t.Fatalf("en reconnecting = %q", got)
	}

	// 探测失败分类：英文环境返回英文原因
	if got := classifyProbeFailure(errors.New("dial tcp 1.2.3.4:7000: connect: connection refused")); !strings.Contains(got, "refused") {
		t.Fatalf("en refused = %q", got)
	}
	if got := classifyProbeFailure(errors.New("protocol error: invalid auth token")); !strings.Contains(got, "authToken") {
		t.Fatalf("en auth = %q", got)
	}
	if got := classifyProbeFailure(nil); got != "Connection closed" {
		t.Fatalf("en connClosed = %q", got)
	}

	// 逐服务注册失败友好原因：英文环境返回英文，原始错误保留于 RawErr 不受语言影响
	if got := friendlyProxyErr("start error: port already used"); got != "Remote port already used by another service" {
		t.Fatalf("en portUsed = %q", got)
	}
	raw := "start error: port already used"
	st := ProxyStatus{RawErr: raw}
	if f := friendlyProxyErr(st.RawErr); f != "" {
		st.Friendly = f
	}
	if st.RawErr != raw || st.Friendly == "" {
		t.Fatalf("RawErr 应保留原文且 Friendly 有值: %+v", st)
	}

	// 服务启动校验消息（configstore）同样随语言切换
	i18n.Set(i18n.EN)
	if msg := configstore.ValidateServerStart(configstore.ServerConfig{ID: "s", ServerAddr: ""}, nil); !strings.Contains(msg, "address") {
		t.Fatalf("en ValidateServerStart = %q", msg)
	}
}
