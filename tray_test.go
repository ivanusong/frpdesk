package main

import (
	"bytes"
	"image"
	"image/png"
	"testing"

	"frpdesk/internal/configstore"
	"frpdesk/internal/tunnel"
)

func TestTrayViewFor(t *testing.T) {
	cases := []struct {
		name             string
		status           configstore.TunnelStatus
		connectedTunnels int
		want             trayStatusView
	}{
		{
			name:   "stopped 可启动",
			status: configstore.TunnelStatus{State: configstore.StateStopped},
			want:   trayStatusView{StateText: "已停止"},
		},
		{
			name:   "starting 可停止",
			status: configstore.TunnelStatus{State: configstore.StateStarting},
			want:   trayStatusView{StateText: "启动中"},
		},
		{
			name:             "running 显示已映射隧道数单数字（无分母）",
			status:           configstore.TunnelStatus{State: configstore.StateRunning, RunningCount: 2, TotalCount: 3},
			connectedTunnels: 2,
			want:             trayStatusView{StateText: "已连接 2"},
		},
		{
			name:             "部分失败不停其他（running 主态 + 原因行）",
			status:           configstore.TunnelStatus{State: configstore.StateRunning, RunningCount: 1, TotalCount: 2, FailedCount: 1, Message: "1 台服务器失败：连接被拒"},
			connectedTunnels: 1,
			want:             trayStatusView{StateText: "已连接 1", ShowReason: true},
		},
		{
			name:   "stopping 禁用",
			status: configstore.TunnelStatus{State: configstore.StateStopping},
			want:   trayStatusView{StateText: "停止中"},
		},
		{
			name:   "failed 显示原因与感叹号图标",
			status: configstore.TunnelStatus{State: configstore.StateFailed, Message: "连接被拒", FailedCount: 1, TotalCount: 1},
			want:   trayStatusView{StateText: "失败 1/1", ShowReason: true, Failed: true},
		},
		{
			name:   "failed 无原因不显示原因行",
			status: configstore.TunnelStatus{State: configstore.StateFailed},
			want:   trayStatusView{StateText: "失败 0/0", ShowReason: false, Failed: true},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := trayViewFor(tc.status, tc.connectedTunnels)
			if got != tc.want {
				t.Fatalf("trayViewFor(%v, %d) = %+v, want %+v", tc.status.State, tc.connectedTunnels, got, tc.want)
			}
		})
	}
}

// TestTrayIcons 验证内嵌托盘图标是合法 PNG：36×36、含非透明像素、两枚图标不同。
func TestTrayIcons(t *testing.T) {
	for name, data := range map[string][]byte{
		"tray-icon":        trayIconPNG,
		"tray-icon-failed": trayIconFailedPNG,
	} {
		img, err := png.Decode(bytes.NewReader(data))
		if err != nil {
			t.Fatalf("%s: 解码失败: %v", name, err)
		}
		b := img.Bounds()
		if b.Dx() != 36 || b.Dy() != 36 {
			t.Fatalf("%s: 尺寸 %v, want 36x36", name, b)
		}
		if !hasOpaquePixel(img) {
			t.Fatalf("%s: 不含非透明像素", name)
		}
	}
	if bytes.Equal(trayIconPNG, trayIconFailedPNG) {
		t.Fatal("常态与 failed 图标不应相同")
	}
}

func hasOpaquePixel(img image.Image) bool {
	b := img.Bounds()
	for y := b.Min.Y; y < b.Max.Y; y++ {
		for x := b.Min.X; x < b.Max.X; x++ {
			if _, _, _, a := img.At(x, y).RGBA(); a > 200 {
				return true
			}
		}
	}
	return false
}

// ---------- 已映射隧道数统计（托盘 running 行） ----------

// TestConnectedTunnelCount 隧道粒度计数：enabled + 归属服务器 running + 代理
// registered 才计入；failed/未注册/disabled/服务器非 running 均不算；跨服务器聚合。
func TestConnectedTunnelCount(t *testing.T) {
	cfg := configstore.Config{
		Servers: []configstore.ServerConfig{
			{ID: "a", Name: "甲"}, {ID: "b", Name: "乙"}, {ID: "c", Name: "丙"},
		},
		Services: []configstore.Service{
			{ID: "t1", ServerID: "a", Enabled: true},     // ✓ registered
			{ID: "t2", ServerID: "a", Enabled: true},     // ✓ registered（同服务器第 2 条）
			{ID: "t3", ServerID: "a", Enabled: true},     // ✗ 代理注册失败
			{ID: "t4", ServerID: "a", Enabled: true},     // ✗ 代理未注册
			{ID: "t5", ServerID: "b", Enabled: true},     // ✓ registered（跨服务器聚合）
			{ID: "t6", ServerID: "b", Enabled: false},    // ✗ 已停用
			{ID: "t7", ServerID: "c", Enabled: true},     // ✗ 服务器 c 非 running
			{ID: "t8", ServerID: "c", Enabled: true},     // ✗ 服务器 c 无状态记录（未启动）
			{ID: "t9", ServerID: "ghost", Enabled: true}, // ✗ 归属服务器不存在
		},
	}
	statuses := []configstore.ServerStatus{
		{ServerID: "a", State: configstore.StateRunning},
		{ServerID: "b", State: configstore.StateRunning},  // 跨服务器聚合：b 也 running
		{ServerID: "c", State: configstore.StateStarting}, // 非 running：t7 不计
		// t8 的服务器无状态记录（未启动）
	}
	proxies := []tunnel.ProxyStatus{
		{ServiceID: "t1", State: "registered"},
		{ServiceID: "t2", State: "registered"},
		{ServiceID: "t3", State: "failed"},
		{ServiceID: "t4", State: "starting"},
		{ServiceID: "t5", State: "registered"},
		{ServiceID: "t6", State: "registered"}, // disabled 但代理仍在列表：不计
		{ServiceID: "t7", State: "registered"}, // 服务器非 running：不计
		{ServiceID: "t9", State: "registered"}, // 服务器不存在：不计
	}

	if got := connectedTunnelCount(cfg, statuses, proxies); got != 3 {
		t.Fatalf("应计入 3 条（t1/t2/t5），得到 %d", got)
	}

	// 空输入：0
	if got := connectedTunnelCount(configstore.Config{}, nil, nil); got != 0 {
		t.Fatalf("空输入应为 0，得到 %d", got)
	}
}

// TestTrayRunningSingleDigit 托盘 running 行使用隧道粒度单数字的端到端映射
// （trayViewFor 与 connectedTunnelCount 的组合口径）。
func TestTrayRunningSingleDigit(t *testing.T) {
	cfg := configstore.Config{
		Servers: []configstore.ServerConfig{{ID: "a", Name: "甲"}, {ID: "b", Name: "乙"}},
		Services: []configstore.Service{
			{ID: "t1", ServerID: "a", Enabled: true},
			{ID: "t2", ServerID: "b", Enabled: true},
			{ID: "t3", ServerID: "b", Enabled: true}, // b 上注册失败
		},
	}
	statuses := []configstore.ServerStatus{
		{ServerID: "a", State: configstore.StateRunning},
		{ServerID: "b", State: configstore.StateRunning},
	}
	proxies := []tunnel.ProxyStatus{
		{ServiceID: "t1", State: "registered"},
		{ServiceID: "t2", State: "registered"},
		{ServiceID: "t3", State: "failed"},
	}
	count := connectedTunnelCount(cfg, statuses, proxies)
	v := trayViewFor(configstore.TunnelStatus{
		State:        configstore.StateRunning,
		RunningCount: 2, TotalCount: 2, // 服务器维度计数不再进入文案
		FailedCount: 1, Message: "1 台服务器失败：xx",
	}, count)
	if v.StateText != "已连接 2" {
		t.Fatalf("托盘应显示隧道粒度「已连接 2」，得到 %q", v.StateText)
	}
}
