package main

// 应用内更新：封装 Wails v3 官方 pkg/updater（本地 v3.0.0-beta.28）+ providers/endpoint。
// 更新源由构建参数 UPDATE_MANIFEST_URL 注入；空值时清晰禁用更新检查。
//
// 约定与边界（务必维持）：
//   - 框架 Config.CheckInterval 严禁设置：其定时器每 tick 调 CheckAndInstall，
//     会静默自动下载安装；本应用的定时器只做 Check，下载安装必须用户手动确认
//   - Config.Window = WindowNone：更新 UI 完全由前端自绘，框架绝不弹窗
//   - 重启走框架 Restart（helper 替换二进制 + host.Quit 正常退出流程），
//     ServiceShutdown 停隧道；全程禁止 os.Exit / 杀进程
//   - dev 版（currentVersion 未注入）禁用检查并留清晰日志

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/wailsapp/wails/v3/pkg/updater"
	"github.com/wailsapp/wails/v3/pkg/updater/providers/endpoint"

	"frpdesk/internal/configstore"
)

// updateManifestURL 由 build.sh 的 UPDATE_MANIFEST_URL 参数经 -ldflags 注入；
// 默认空字符串表示发行构建未配置更新源，此时更新检查明确禁用。
var updateManifestURL = ""

// updateCheckInterval 后台自动检查周期：每 6 小时只 Check，绝不自动安装。
const updateCheckInterval = 6 * time.Hour

// updateCheckTimeout 单次检查的网络超时。
const updateCheckTimeout = 30 * time.Second

// 更新状态（与前端约定的统一枚举）。
const (
	updIdle        = "idle"
	updChecking    = "checking"
	updUpToDate    = "up-to-date"
	updAvailable   = "available"
	updDownloading = "downloading"
	updVerifying   = "verifying"
	updInstalling  = "installing"
	updReady       = "ready"
	updError       = "error"
	updDisabled    = "disabled"
)

// appUpdateEvent 推送给前端的更新状态事件名，payload 为 UpdateInfo。
const appUpdateEvent = "app:update"

// 更新流程的明确错误（前端可据此提示/重试）。
var (
	errUpdaterBusy      = errors.New("已有更新操作在进行，请稍后再试")
	errUpdaterDisabled  = errors.New("更新检查已禁用")
	errNoPendingUpdate  = errors.New("当前没有已检查的新版本，请先检查更新")
	errUpdaterNotInit   = errors.New("更新器尚未初始化")
	errUpdaterShuttingD = errors.New("应用正在退出，更新操作已取消")
)

// UpdateInfo 推给前端的更新状态（app:update 事件 payload + 绑定返回值）。
// JSON 字段名与前端约定，勿改。
type UpdateInfo struct {
	CurrentVersion string `json:"currentVersion"`
	LatestVersion  string `json:"latestVersion"`
	// Notes 新版本的更新日志（Markdown，来自更新源 manifest notes 字段）
	Notes    string `json:"notes,omitempty"`
	State    string `json:"state"`
	Message  string `json:"message,omitempty"`
	Progress int    `json:"progress"` // 0..100
}

// fwUpdater 抽象框架 app.Updater 的能力面（单测桩替，绝不启动应用）。
type fwUpdater interface {
	Init(updater.Config) error
	Check(context.Context) (*updater.Release, error)
	DownloadAndInstall(context.Context) error
	Restart(context.Context) error
	State() updater.State
}

// 编译期断言：框架 Updater 满足能力面（Init/Check/DownloadAndInstall/Restart/State）。
var _ fwUpdater = (*updater.Updater)(nil)

// updaterController 更新流程控制器：自持一套与前端约定的状态机，
// 底层复用框架 Updater（Check/DownloadAndInstall/Restart），事件与日志经宿主注入。
//
// 并发模型：
//   - opMu 统一操作互斥：任一时刻至多一个流程（检查或下载/校验/安装/重启），
//     且互斥覆盖框架调用的完整周期（而非仅启动 goroutine），
//     TryLock 忙时返回明确错误，消除“先读状态再改状态”的 TOCTOU
//   - 框架事件镜像（进度/校验/安装）带框架状态交叉校验，迟到事件不回退状态与进度
//   - install 同步完成并返回实际错误（Wails 绑定在独立 goroutine 调用，不锁 UI）
//
// 锁序：opMu → mu；emitMu → mu（notify）。不存在持 mu 取 opMu/emitMu 的反向路径。
type updaterController struct {
	fw          fwUpdater
	emit        func(name string, data any)             // app:update 事件出口（attach 后可用）
	log         func(level, format string, args ...any) // slog + 应用日志环形缓冲
	manifestURL string                                  // 构建时注入的更新清单地址；空值禁用更新

	mu   sync.Mutex
	info UpdateInfo
	// stagedVersion 已下载校验就绪、待重启的版本号；空表示无。
	// install 时严格匹配当前 latestVersion：不匹配（源出现了更新的版本）则重新下载，
	// 同版重启失败重试则跳过重复下载。
	stagedVersion string

	opMu   sync.Mutex // 操作互斥：检查与下载/重启全程互斥
	emitMu sync.Mutex // 事件发送串行化：保证 app:update 有序、终态不被旧值覆盖

	ctx    context.Context // 生命周期根：ServiceShutdown 取消
	cancel context.CancelFunc
}

func newUpdaterController(log func(level, format string, args ...any)) *updaterController {
	return newUpdaterControllerWithManifestURL(log, updateManifestURL)
}

// newUpdaterControllerWithManifestURL 显式传入更新源，便于隔离测试与构建配置。
func newUpdaterControllerWithManifestURL(log func(level, format string, args ...any), manifestURL string) *updaterController {
	ctx, cancel := context.WithCancel(context.Background())
	return &updaterController{
		log:         log,
		ctx:         ctx,
		cancel:      cancel,
		manifestURL: manifestURL,
		info:        UpdateInfo{State: updIdle},
	}
}

// attach 绑定框架 Updater 与事件出口，完成框架 Init 并启动后台定时检查。
// 在 attachUI（Run 前、框架就绪期）调用一次；重复调用幂等跳过。
func (c *updaterController) attach(fw fwUpdater, version string, emit func(name string, data any)) {
	c.mu.Lock()
	if c.fw != nil {
		c.mu.Unlock()
		return
	}
	c.fw = fw
	c.emit = emit
	c.info.CurrentVersion = version
	isDev := version == "" || version == "dev"
	if isDev {
		c.info.State = updDisabled
		c.info.Message = "开发版（版本号未注入），更新检查已禁用"
	}
	c.mu.Unlock()

	if isDev {
		c.log("warn", "[更新] 当前为开发版（currentVersion=%q），更新检查与安装已禁用", version)
		c.notify()
		return
	}
	if strings.TrimSpace(c.manifestURL) == "" {
		c.mu.Lock()
		c.info.State = updDisabled
		c.info.Message = "未配置更新源，更新检查已禁用"
		c.mu.Unlock()
		c.log("warn", "[更新] 未配置更新源，更新检查与安装已禁用（构建时可设置 UPDATE_MANIFEST_URL）")
		c.notify()
		return
	}

	prov, err := endpoint.New(endpoint.Config{URL: c.manifestURL})
	if err != nil {
		c.log("error", "[更新] 初始化更新源失败：%v", err)
		c.applyState(updError, "更新源初始化失败："+configstore.BriefMessage(err.Error(), 120))
		return
	}

	// 刻意不设置 CheckInterval（框架定时器会 CheckAndInstall 自动安装），
	// 也不设置 PublicKey（清单当前只带 digest；若带签名则按框架规则拒绝安装——fail closed）。
	if err := fw.Init(updater.Config{
		CurrentVersion: version,
		Providers:      []updater.Provider{prov},
		Window:         updater.WindowNone, // 无框架更新窗口；后台检查绝不弹窗
	}); err != nil {
		c.log("error", "[更新] 框架更新器初始化失败：%v", err)
		c.applyState(updError, "更新器初始化失败："+configstore.BriefMessage(err.Error(), 120))
		return
	}

	c.log("info", "[更新] 更新器就绪：当前版本 %s，源 %s，每 %s 自动检查（仅检查不安装）",
		version, c.manifestURL, updateCheckInterval)
	c.notify()

	go c.periodicLoop()
}

// periodicLoop 后台定时检查：每 6 小时只 Check；无新版本仅记日志不弹窗。
func (c *updaterController) periodicLoop() {
	t := time.NewTicker(updateCheckInterval)
	defer t.Stop()
	for {
		select {
		case <-c.ctx.Done():
			c.log("info", "[更新] 后台定时检查已停止")
			return
		case <-t.C:
			c.log("info", "[更新] 定时检查触发（周期 %s）", updateCheckInterval)
			_, _ = c.check("自动定时检查")
		}
	}
}

// startupCheck 启动后的首次检查（ServiceStartup 中以 goroutine 调用，不阻塞启动）。
func (c *updaterController) startupCheck() {
	c.log("info", "[更新] 启动检查开始（当前版本 %s）", c.snapshot().CurrentVersion)
	_, _ = c.check("启动检查")
}

// check 执行一次更新检查。source 标记触发来源（手动/启动/定时），仅用于日志。
func (c *updaterController) check(source string) (UpdateInfo, error) {
	if c.disabled() {
		c.log("warn", "[更新] %s被拒绝：开发版禁用更新检查", source)
		return c.snapshot(), errUpdaterDisabled
	}
	if err := c.ctx.Err(); err != nil {
		c.log("warn", "[更新] %s被拒绝：应用正在退出", source)
		return c.snapshot(), errUpdaterShuttingD
	}
	fw := c.current()
	if fw == nil {
		return c.snapshot(), errUpdaterNotInit
	}
	// 操作互斥：与下载/重启流程全程互斥（含框架 Check 调用期间），忙时明确拒绝。
	// 持锁期间 state 只会由本流程推进，不存在并发流程同时开始的窗口。
	if !c.opMu.TryLock() {
		c.log("warn", "[更新] %s被拒绝：已有更新操作（检查/下载/重启）在进行", source)
		return c.snapshot(), errUpdaterBusy
	}
	defer c.opMu.Unlock()

	c.log("info", "[更新] %s：正在检查（当前版本 %s，源 %s）",
		source, c.snapshot().CurrentVersion, c.manifestURL)
	c.applyState(updChecking, "")

	ctx, cancel := context.WithTimeout(c.ctx, updateCheckTimeout)
	defer cancel()
	rel, err := fw.Check(ctx)
	if err != nil {
		c.log("error", "[更新] %s失败：%v", source, err)
		// 失败保留 latestVersion：已有发现不被错误回退，UI 可重试
		c.applyState(updError, "检查更新失败："+configstore.BriefMessage(err.Error(), 120))
		return c.snapshot(), err
	}
	if rel == nil {
		// 无新版：按约定清空 latestVersion，UI 收起更新按钮；
		// 已 staged 的旧包随版本确认作废
		c.log("info", "[更新] %s结果：已是最新版本（%s）", source, c.snapshot().CurrentVersion)
		c.invalidateStaged()
		c.applyState(updUpToDate, "已是最新版本", true)
		return c.snapshot(), nil
	}
	c.log("info", "[更新] %s结果：发现新版本 %s（当前 %s），等待用户确认安装",
		source, rel.Version, c.snapshot().CurrentVersion)
	c.setLatest(rel.Version, rel.Notes)
	c.applyState(updAvailable, "发现新版本 "+rel.Version)
	return c.snapshot(), nil
}

// install 用户手动确认后的下载 + 校验 + 安装 + 重启，同步完成并返回实际错误。
// Wails 绑定在独立 goroutine 调用服务方法，同步执行不会锁 UI；
// 进度/阶段仍经 app:update 事件持续推送，前端 RPC 可直接拿到下载/重启失败错误。
// 只有确实存在已检查的新版本（latestVersion 非空）才开始；
// 同版已 staged（上次重启失败）时直接重试重启，不重复下载。
func (c *updaterController) install() error {
	if c.disabled() {
		c.log("warn", "[更新] 安装被拒绝：开发版禁用更新")
		return errUpdaterDisabled
	}
	if err := c.ctx.Err(); err != nil {
		c.log("warn", "[更新] 安装被拒绝：应用正在退出")
		return errUpdaterShuttingD
	}
	fw := c.current()
	if fw == nil {
		return errUpdaterNotInit
	}
	// 操作互斥：与检查及另一安装流程全程互斥（含框架 Download/Restart 调用期间）
	if !c.opMu.TryLock() {
		c.log("warn", "[更新] 安装被拒绝：已有更新操作（检查/下载/重启）在进行")
		return errUpdaterBusy
	}
	defer c.opMu.Unlock()

	snap := c.snapshot()
	if snap.LatestVersion == "" {
		c.log("warn", "[更新] 安装被拒绝：尚未检查到新版本")
		return errNoPendingUpdate
	}

	// 同版更新包已就绪（上次重启失败）：跳过下载直接重启；
	// 版本不匹配（检查发现了更新的版本）则走下方完整下载流程
	if c.stagedVersionSnapshot() == snap.LatestVersion {
		c.log("info", "[更新] 版本 %s 更新包已就绪，重试重启（跳过下载）", snap.LatestVersion)
		c.applyState(updInstalling, "正在重启以应用更新")
		return c.finishInstall(snap.LatestVersion)
	}

	c.log("info", "[更新] 用户确认安装：开始下载版本 %s（当前 %s，源 %s）",
		snap.LatestVersion, snap.CurrentVersion, c.manifestURL)
	c.applyState(updDownloading, "开始下载更新")
	c.setProgress(0, "开始下载更新")

	ctx, cancel := context.WithCancel(c.ctx)
	defer cancel()
	if err := fw.DownloadAndInstall(ctx); err != nil {
		if c.ctx.Err() != nil {
			c.log("warn", "[更新] 下载安装中断（应用退出）：%v", err)
			return err
		}
		c.log("error", "[更新] 版本 %s 下载/校验/安装失败：%v", snap.LatestVersion, err)
		// 保留 latestVersion，UI 可重试；实际错误直接返回前端
		c.applyState(updError, "更新失败："+configstore.BriefMessage(err.Error(), 120))
		return err
	}
	c.mu.Lock()
	c.stagedVersion = snap.LatestVersion
	c.mu.Unlock()
	c.log("info", "[更新] 版本 %s 下载校验完成，已就绪待重启", snap.LatestVersion)
	c.applyState(updReady, "更新已就绪，即将重启")
	c.setProgress(100, "更新已就绪，即将重启")

	return c.finishInstall(snap.LatestVersion)
}

// finishInstall 调用框架 Restart：拉起 helper（同二进制哨兵模式）→ host.Quit 正常退出
// → helper 等本进程退出后替换二进制并重启。ServiceShutdown 停隧道，无 os.Exit。
// 同步返回实际错误（重启失败时前端 RPC 可直接感知并重试）。
func (c *updaterController) finishInstall(version string) error {
	fw := c.current()
	if fw == nil {
		return errUpdaterNotInit
	}
	ctx, cancel := context.WithCancel(c.ctx)
	defer cancel()
	if err := fw.Restart(ctx); err != nil {
		if c.ctx.Err() != nil {
			c.log("warn", "[更新] 重启中断（应用退出）：%v", err)
			return err
		}
		c.log("error", "[更新] 版本 %s 重启失败（更新包已就绪，可直接重试安装）：%v", version, err)
		c.applyState(updError, "重启以应用更新失败："+configstore.BriefMessage(err.Error(), 120))
		return err
	}
	c.log("info", "[更新] 版本 %s 重启流程已启动：helper 就绪，宿主即将正常退出并替换二进制", version)
	// 收敛到 ready 终态（覆盖 staged 重试路径的 installing 过渡态）；
	// 宿主正常退出链（含 ServiceShutdown 停隧道）由框架 host.Quit 触发
	c.applyState(updReady, "正在重启以应用更新")
	return nil
}

// onProgress 框架 EventDownloadProgress 的镜像。
// 双重校验（框架状态 + 本地状态均为 downloading）保证迟到的进度事件不回退状态。
func (c *updaterController) onProgress(p updater.Progress) {
	fw := c.current()
	if fw == nil || fw.State() != updater.StateDownloading {
		return
	}
	c.mu.Lock()
	if c.info.State != updDownloading {
		c.mu.Unlock()
		return
	}
	if p.Total > 0 {
		pct := int(float64(p.Written) / float64(p.Total) * 100)
		if pct < 0 {
			pct = 0
		}
		if pct > 100 {
			pct = 100
		}
		if pct <= c.info.Progress {
			c.mu.Unlock()
			return // 百分比未变或迟到的小百分比：不重复推送、不回退进度
		}
		c.info.Progress = pct
		c.info.Message = fmt.Sprintf("正在下载更新 %s/%s", humanBytes(p.Written), humanBytes(p.Total))
	} else {
		// 总大小未知：保留已有百分比，只更新字节进度消息
		c.info.Message = fmt.Sprintf("正在下载更新（已下载 %s）", humanBytes(p.Written))
	}
	c.mu.Unlock()
	c.notify()
}

// onVerifying 框架 EventVerifying 的镜像：仅 downloading → verifying，
// 框架状态交叉校验挡掉乱序迟到事件。
func (c *updaterController) onVerifying() {
	fw := c.current()
	if fw == nil || fw.State() != updater.StateVerifying {
		return
	}
	c.mu.Lock()
	if c.info.State != updDownloading {
		c.mu.Unlock()
		return
	}
	c.info.State = updVerifying
	c.info.Message = "正在校验更新包"
	c.mu.Unlock()
	c.log("info", "[更新] 更新包校验开始")
	c.notify()
}

// onInstalling 框架 EventInstalling 的镜像：仅 verifying → installing。
func (c *updaterController) onInstalling() {
	fw := c.current()
	if fw == nil || fw.State() != updater.StateInstalling {
		return
	}
	c.mu.Lock()
	if c.info.State != updVerifying {
		c.mu.Unlock()
		return
	}
	c.info.State = updInstalling
	c.info.Message = "正在安装更新"
	c.mu.Unlock()
	c.log("info", "[更新] 更新包安装（staging）开始")
	c.notify()
}

// shutdown 生命周期收尾（ServiceShutdown 调用）：取消后台定时检查与进行中的检查/下载。
func (c *updaterController) shutdown() {
	c.cancel()
	c.log("info", "[更新] 生命周期结束：已取消后台检查与进行中的更新操作")
}

// --- 状态与快照 ---

func (c *updaterController) snapshot() UpdateInfo {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.info
}

func (c *updaterController) disabled() bool {
	return c.snapshot().State == updDisabled
}

func (c *updaterController) stagedVersionSnapshot() string {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.stagedVersion
}

// invalidateStaged 作废已 staged 的更新包（确认无新版或发现不同新版本时）。
func (c *updaterController) invalidateStaged() {
	c.mu.Lock()
	c.stagedVersion = ""
	c.mu.Unlock()
}

func (c *updaterController) current() fwUpdater {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.fw
}

// applyState 更新状态与消息；clear 为 true 时清空 latestVersion（检查确认无新版）。
// 进度字段不受影响。事件在锁外串行发出（notify 内部重读当下快照）。
func (c *updaterController) applyState(state, message string, clearLatest ...bool) {
	c.mu.Lock()
	c.info.State = state
	c.info.Message = message
	if len(clearLatest) > 0 && clearLatest[0] {
		c.info.LatestVersion = ""
		c.info.Notes = "" // 与 latestVersion 对称清空，避免残留旧版本的更新日志
	}
	c.mu.Unlock()
	c.notify()
}

// setProgress 更新进度（0..100）与可选消息；事件在锁外串行发出。
func (c *updaterController) setProgress(progress int, message string) {
	c.mu.Lock()
	c.info.Progress = progress
	if message != "" {
		c.info.Message = message
	}
	c.mu.Unlock()
	c.notify()
}

// setLatest 记录检查发现的新版本号与更新日志（available 时调用）；
// 与已 staged 的版本不同则作废旧包，绝不拿旧包安装新版本。
func (c *updaterController) setLatest(version, notes string) {
	c.mu.Lock()
	if version != c.stagedVersion {
		c.stagedVersion = ""
	}
	c.info.LatestVersion = version
	c.info.Notes = notes
	c.mu.Unlock()
}

func (c *updaterController) notify() {
	// 串行化发送并重读当下快照：旧通知只会发出新状态，
	// 消除“mu 内捕获旧快照 → 调度延迟 → 新状态之后发出旧值”的倒退窗口。
	// 锁序：emitMu → mu（无持 mu 取 emitMu 的反向路径）；
	// 框架 Event.Emit 对监听器/前端均为异步投递，不会阻塞持锁方。
	if c.emit == nil {
		return
	}
	c.emitMu.Lock()
	defer c.emitMu.Unlock()
	c.emit(appUpdateEvent, c.snapshot())
}

// humanBytes 人类可读的字节数（日志/进度消息用）。
func humanBytes(n int64) string {
	const unit = 1024
	if n < unit {
		return fmt.Sprintf("%d B", n)
	}
	div, exp := int64(unit), 0
	for m := n / unit; m >= unit; m /= unit {
		div *= unit
		exp++
	}
	return fmt.Sprintf("%.1f %cB", float64(n)/float64(div), "KMGTPE"[exp])
}
