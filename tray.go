package main

// 托盘控制器：状态栏模板图标 + 原生菜单 + 主窗口隐藏/恢复。
//
// 与 wails v3 行为绑定的实现要点（勿凭记忆改动，均已核对 beta.28 源码）：
//   - 关闭拦截必须用 RegisterHook(events.Common.WindowClosing)：钩子先于框架默认
//     销毁监听执行，e.Cancel() 后默认销毁监听不再运行，窗口只隐藏不销毁；
//     若改用 OnWindowEvent 会与框架默认监听共存导致窗口被销毁。
//   - 菜单点击回调由 wails 派发在独立 goroutine（menuItemClicked 通道），
//     Start/Stop 的最长 3s 等待不会阻塞 UI 主线程。
//   - 状态更新在 UI 闭包执行时读取 Manager 当前状态渲染，丢弃迟到过期回调；
//     只改既有菜单项的标签/禁用/隐藏与托盘图标，不重建菜单。
//   - 窗口/Dock 动作与菜单/图标更新统一在 application.InvokeAsync 主线程闭包中按序
//     执行，避免桥 dispatch_async 与窗口 InvokeSync 两个队列交错导致顺序不确定；
//     wails 窗口方法在主线程上调用时同步直接落（InvokeSync 检测主线程后直执），
//     不会自等待。
//   - 黄色最小化按钮在窗口首次 WindowShow 后重定向到 performClose:，
//     与红色关闭完全同路（windowShouldClose → WindowClosing → Cancel + 隐藏）；
//     wails 无最小化拦截（windowWillMiniaturize 不可取消，WindowMinimise 是事后映射）；
//     桥与接管时机见 dock_darwin.go，恢复仍走 restoreWindow。
//   - 构造期（Run 前）不能调 application.InvokeAsync（globalApplication.impl 为 nil
//     会 panic），初始菜单靠 MenuItem setter 仅缓存配置的特性直接摆默认值。
//   - 菜单项 setter 在应用 Run 前调用时仅缓存，Run 后经主线程落到 NSMenuItem。

import (
	_ "embed"
	"fmt"
	"log/slog"
	"sync/atomic"

	"github.com/wailsapp/wails/v3/pkg/application"
	"github.com/wailsapp/wails/v3/pkg/events"

	"frpdesk/internal/configstore"
	"frpdesk/internal/tunnel"
)

//go:embed assets/tray-icon.png
var trayIconPNG []byte

//go:embed assets/tray-icon-failed.png
var trayIconFailedPNG []byte

// connectedTunnelCount 统计「当前已映射隧道数」（托盘 running 状态行展示用）：
// enabled 且归属服务器处于 running、且该隧道代理已注册（registered）的隧道数。
// 删除/停用/未注册/注册失败/服务器不在 running 的都不计入；跨服务器聚合。
// 纯函数（不读 Manager/Store），便于单测。
func connectedTunnelCount(cfg configstore.Config, serverStatuses []configstore.ServerStatus, proxies []tunnel.ProxyStatus) int {
	running := make(map[string]bool, len(serverStatuses))
	for _, st := range serverStatuses {
		if st.State == configstore.StateRunning {
			running[st.ServerID] = true
		}
	}
	registered := make(map[string]bool, len(proxies))
	for _, ps := range proxies {
		if ps.State == "registered" {
			registered[ps.ServiceID] = true
		}
	}
	count := 0
	for i := range cfg.Services {
		svc := cfg.Services[i]
		if !svc.Enabled || !registered[svc.ID] || !running[svc.ServerID] {
			continue
		}
		count++
	}
	return count
}

// trayStatusView 隧道状态 → 托盘菜单展示的映射结果（纯数据，便于单测）。
// 输入为多服务器聚合状态（counts 由 AggregateStatus 派生）。
// 服务器生命周期由 Service.Enabled 自动驱动，托盘无手动启停项。
type trayStatusView struct {
	StateText  string // 首行「frpdesk · 」后的状态文字
	ShowReason bool   // 是否显示失败原因行
	Failed     bool   // 是否使用带感叹号徽标的故障图标
}

// trayViewFor 聚合状态映射：running 显示「已连接 N」（N=当前已映射隧道数，
// 由 connectedTunnelCount 按隧道粒度统计，非服务器计数、无分母）；
// 部分失败不停其他实例（失败原因可见）。
func trayViewFor(s configstore.TunnelStatus, connectedTunnels int) trayStatusView {
	v := trayStatusView{StateText: "已停止"}
	switch s.State {
	case configstore.StateRunning:
		v.StateText = fmt.Sprintf("已连接 %d", connectedTunnels)
		v.ShowReason = s.Message != "" // 部分失败：原因行可见但不改主态
	case configstore.StateStarting:
		v.StateText = "启动中"
	case configstore.StateStopping:
		v.StateText = "停止中"
	case configstore.StateFailed:
		v.StateText = fmt.Sprintf("失败 %d/%d", s.FailedCount, s.TotalCount)
		v.Failed = true
		v.ShowReason = s.Message != ""
	}
	return v
}

type trayController struct {
	svc    *AppService
	app    *application.App
	window *application.WebviewWindow
	tray   *application.SystemTray

	statusItem *application.MenuItem // 「frpdesk · 状态」不可点行（聚合：已连接 N/M 等）
	reasonItem *application.MenuItem // 失败原因不可点行（按需显示）

	quitting atomic.Bool // 退出标记：置位后 UI 闭包与动作入口全部跳过，退出不等待 UI
}

// newTrayController 在应用对象创建后、Run 之前装配托盘与窗口钩子；
// 此时各 setter 仅缓存配置，Run 时框架统一落到原生对象（框架就绪期安全初始化）。
func newTrayController(app *application.App, window *application.WebviewWindow, svc *AppService) *trayController {
	t := &trayController{svc: svc, app: app, window: window}

	menu := app.Menu.New()
	t.statusItem = menu.Add("frpdesk · 已停止").SetEnabled(false)
	t.reasonItem = menu.Add("").SetEnabled(false)
	t.reasonItem.SetHidden(true)
	menu.AddSeparator()
	menu.Add("显示主窗口").OnClick(func(*application.Context) { t.restoreWindow() })
	menu.AddSeparator()
	menu.Add("退出 frpdesk").OnClick(func(*application.Context) { t.quit() })

	// 黑白透明 PNG 模板图标自动适配深浅色（源 assets/tray-icon*.svg，
	// 生成方式见 cmd/genicons/svg2png.swift）；failed 时换带感叹号徽标的故障图标
	t.tray = app.SystemTray.New()
	t.tray.SetTemplateIcon(trayIconPNG)
	t.tray.SetTooltip("frpdesk")
	t.tray.SetMenu(menu)

	// 红色关闭按钮 / Cmd+W → 隐藏到托盘（页面与未保存编辑保留，隧道继续）。
	// 先 Cancel 拦下默认销毁，再调度隐藏；真正退出（quitting）时不拦截
	window.RegisterHook(events.Common.WindowClosing, func(e *application.WindowEvent) {
		if t.quitting.Load() {
			return // 真正退出中：放行默认关闭，不拦截成隐藏
		}
		e.Cancel()
		t.hideToTray()
	})

	// 黄色最小化按钮 → 与红色关闭完全同路：重定向到 performClose:（target=窗口自身），
	// 走既有 windowShouldClose delegate → WindowClosing → Cancel + hideToTray
	// （无系统最小化动画、无 Dock 缩略图）。wails 无最小化拦截：macOS 侧仅发不可
	// 取消的 windowWillMiniaturize 通知，Common.WindowMinimise 是事后映射，Cancel 无意义。
	// 窗口就绪（首次 WindowShow，此时 NSWindow 已存在）后主线程接管一次；恢复仍走 restoreWindow
	var miniTakenOver atomic.Bool
	window.OnWindowEvent(events.Common.WindowShow, func(*application.WindowEvent) {
		if miniTakenOver.Swap(true) {
			return // 只接管一次；重复设置 target/action 虽无害但无必要
		}
		application.InvokeAsync(func() {
			macTakeOverMiniaturize(window)
		})
	})

	// Dock 图标点击（Finder/Launchpad reopen，窗口最小化或被遮挡时）→ 统一恢复窗口。
	// 必须 hook + Cancel 接管：框架在 setupCommonEvents 里注册了默认 reopen 监听
	// （无可见窗口时 Show 所有窗口），若用 OnApplicationEvent 追加监听会与之并行执行、
	// 抢先 Show 改变最小化状态；hook 同步先执行，Cancel 后本次事件所有 listener 不再分发
	app.Event.RegisterApplicationEventHook(events.Mac.ApplicationShouldHandleReopen,
		func(e *application.ApplicationEvent) {
			e.Cancel()
			t.restoreWindow()
		})

	return t
}

// hideToTray 隐藏主窗口并收起 Dock 图标；托盘常驻，隧道不受影响。
// Hide 与 Dock 策略在同一主线程闭包按序执行（见文件头要点）。
func (t *trayController) hideToTray() {
	slog.Info("[托盘] 隐藏主窗口到托盘")
	application.InvokeAsync(func() {
		if t.quitting.Load() {
			return
		}
		t.window.Hide()
		macSetDockVisible(false)
	})
}

// restoreWindow 恢复原窗口：Dock 图标 → 还原最小化 → 显示 → 焦点 → 前台激活。
// 菜单/托盘 reopen/第二实例三条路径统一走这里，动作在同一主线程闭包按序执行。
func (t *trayController) restoreWindow() {
	slog.Info("[托盘] 恢复主窗口")
	application.InvokeAsync(func() {
		if t.quitting.Load() {
			return
		}
		macSetDockVisible(true)
		if t.window.IsMinimised() {
			t.window.UnMinimise()
		}
		t.window.Show()
		t.window.Focus()
		macActivateApp()
	})
}

// quit 真正退出：ServiceShutdown 会停止所有隧道，无确认框；CAS 保证幂等。
func (t *trayController) quit() {
	if !t.quitting.CompareAndSwap(false, true) {
		return
	}
	slog.Info("[托盘] 用户请求退出")
	t.app.Quit()
}

// markQuitting 供 ServiceShutdown 提前置位：wails cleanup 顺序是先停服务再销毁窗口/托盘，
// 期间状态回调仍会到达，置位后 UI 闭包与动作入口全部跳过，退出路径不等待 UI。
func (t *trayController) markQuitting() {
	t.quitting.Store(true)
}

// updateStatus 状态变化时调度主线程刷新托盘（只反映状态，不发起业务动作）。
// 聚合状态由宿主组装（Snapshot），迟到回调在 UI 执行时统一读最新值。
func (t *trayController) updateStatus() {
	if t.quitting.Load() {
		return
	}
	application.InvokeAsync(func() {
		if t.quitting.Load() {
			return // 退出开始后不再触碰菜单/图标
		}
		snap := t.svc.Snapshot()
		proxies := t.svc.tunnel.ProxyStatuses()
		count := connectedTunnelCount(snap.Config, snap.ServerStatuses, proxies)
		t.applyStatus(snap.Status, count)
	})
}

// applyStatus 主线程内执行：只改既有菜单项与图标，不重建菜单
// （NSStatusItem 持有 NSMenu 引用，逐项更新下一次展开即生效）。
func (t *trayController) applyStatus(s configstore.TunnelStatus, connectedTunnels int) {
	v := trayViewFor(s, connectedTunnels)
	t.statusItem.SetLabel("frpdesk · " + v.StateText)
	// 托盘菜单宽度有限：失败原因单行化 + 截断（完整原文在环形日志与前端 title）
	t.reasonItem.SetLabel(configstore.BriefMessage(s.Message, 60))
	t.reasonItem.SetHidden(!v.ShowReason)
	if v.Failed {
		t.tray.SetTemplateIcon(trayIconFailedPNG)
	} else {
		t.tray.SetTemplateIcon(trayIconPNG)
	}
	slog.Info("[托盘] 状态更新", "state", s.State, "message", s.Message)
}
