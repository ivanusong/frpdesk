package main

// 更新流程单元测试：桩替框架 Updater（fwUpdater），不启动应用、不跑 GUI、不真实安装重启。
// 覆盖：dev 禁用、框架 Init 参数、检查状态机（有/无新版/失败/忙拒绝）、
// 安装流程（无新版拒绝、同步完成、下载错误直接返回、重启失败直接返回、
// staged 版本严格匹配与换版重下载）、check/install 真正阻塞中的互斥、
// 进度镜像与迟到事件防回退、并发事件终态一致性、生命周期取消、JSON 字段约定。

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/wailsapp/wails/v3/pkg/updater"
)

// --- 测试桩 ---

// fakeFW 桩替框架 Updater，记录调用并模拟状态迁移。
// checkBlock/dlBlock 非 nil 时对应调用阻塞直至通道关闭（单次使用，测试自行控制）。
type fakeFW struct {
	mu            sync.Mutex
	initCalled    int
	initCfg       updater.Config
	checkCalled   int
	checkRel      *updater.Release
	checkErr      error
	checkBlock    chan struct{}
	checkOpened   chan struct{}
	dlCalled      int
	dlErr         error
	dlBlock       chan struct{}
	dlOpened      chan struct{}
	restartCalled int
	restartErr    error
	state         updater.State
}

func (f *fakeFW) Init(cfg updater.Config) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.initCalled++
	f.initCfg = cfg
	return nil
}

func (f *fakeFW) Check(context.Context) (*updater.Release, error) {
	f.mu.Lock()
	f.checkCalled++
	rel, err := f.checkRel, f.checkErr
	f.mu.Unlock()
	if f.checkBlock != nil {
		close(f.checkOpened)
		<-f.checkBlock
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	switch {
	case err != nil:
		f.state = updater.StateError
		return nil, err
	case rel == nil:
		f.state = updater.StateUpToDate
		return nil, nil
	default:
		f.state = updater.StateAvailable
		return rel, nil
	}
}

func (f *fakeFW) DownloadAndInstall(context.Context) error {
	f.mu.Lock()
	f.dlCalled++
	err := f.dlErr
	f.mu.Unlock()
	if f.dlBlock != nil {
		close(f.dlOpened)
		<-f.dlBlock
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if err != nil {
		f.state = updater.StateError
		return err
	}
	// 模拟框架内部状态推进（同步完成，与真实 Updater 一致）
	f.state = updater.StateVerifying
	f.state = updater.StateInstalling
	f.state = updater.StateReady
	return nil
}

func (f *fakeFW) Restart(context.Context) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.restartCalled++
	return f.restartErr
}

func (f *fakeFW) State() updater.State {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.state
}

func (f *fakeFW) calls() (dl, restart int) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.dlCalled, f.restartCalled
}

func (f *fakeFW) checkCalls() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.checkCalled
}

// emitRecorder 收集 app:update 事件。
type emitRecorder struct {
	mu     sync.Mutex
	events []string
	last   UpdateInfo
}

func (r *emitRecorder) emit(name string, data any) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.events = append(r.events, name)
	if info, ok := data.(UpdateInfo); ok {
		r.last = info
	}
}

func (r *emitRecorder) names() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]string(nil), r.events...)
}

func (r *emitRecorder) lastInfo() UpdateInfo {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.last
}

// logRecorder 收集更新日志。
type logRecorder struct {
	mu   sync.Mutex
	line []string
}

func (l *logRecorder) logf(level, format string, args ...any) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.line = append(l.line, level+"|"+fmt.Sprintf(format, args...))
}

func (l *logRecorder) contains(sub string) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	for _, s := range l.line {
		if strings.Contains(s, sub) {
			return true
		}
	}
	return false
}

// testManifestURL 单测用更新源：不依赖构建参数注入（updateManifestURL 默认空会禁用）。
const testManifestURL = "https://updates.example.invalid/frpdesk/stable.json"

// newTestController 构造全套被测件。
func newTestController() (*updaterController, *fakeFW, *emitRecorder, *logRecorder) {
	logs := &logRecorder{}
	c := newUpdaterControllerWithManifestURL(logs.logf, testManifestURL)
	fw := &fakeFW{}
	em := &emitRecorder{}
	return c, fw, em, logs
}

// attachReal 以真实版本号完成 attach（模拟 attachUI 装配）。
func attachReal(c *updaterController, fw *fakeFW, em *emitRecorder, version string) {
	c.attach(fw, version, em.emit)
}

// waitFor 轮询等待异步条件落定（后台 goroutine/定时器）。
func waitFor(t *testing.T, cond func() bool, msg string) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatal("等待超时: " + msg)
}

// --- JSON 字段约定 ---

func TestUpdateInfoJSONFields(t *testing.T) {
	info := UpdateInfo{
		CurrentVersion: "1.0.0",
		LatestVersion:  "2.0.0",
		Notes:          "## 更新内容\n- 修复若干问题",
		State:          updAvailable,
		Message:        "发现新版本",
		Progress:       50,
	}
	b, err := json.Marshal(info)
	if err != nil {
		t.Fatal(err)
	}
	var m map[string]any
	if err := json.Unmarshal(b, &m); err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{"currentVersion", "latestVersion", "notes", "state", "message", "progress"} {
		if _, ok := m[key]; !ok {
			t.Errorf("缺少约定的 JSON 字段 %q：%s", key, b)
		}
	}
	if m["progress"].(float64) != 50 {
		t.Errorf("progress 应为 0..100 数字，got %v", m["progress"])
	}
	// notes 为 omitempty：无日志时不得出现在 payload 里
	b, err = json.Marshal(UpdateInfo{State: updUpToDate})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(b), "notes") {
		t.Errorf("notes 为空时不应序列化：%s", b)
	}
}

// --- attach / dev 禁用 ---

func TestAttachDevVersionDisables(t *testing.T) {
	for _, version := range []string{"dev", ""} {
		c, fw, em, logs := newTestController()
		t.Cleanup(c.cancel)
		attachReal(c, fw, em, version)

		info := c.snapshot()
		if info.State != updDisabled {
			t.Errorf("version=%q: state = %q, want disabled", version, info.State)
		}
		if info.CurrentVersion != version {
			t.Errorf("version=%q: currentVersion = %q", version, info.CurrentVersion)
		}
		if fw.initCalled != 0 {
			t.Errorf("version=%q: dev 版不得 Init 框架更新器", version)
		}
		if !logs.contains("禁用") {
			t.Errorf("version=%q: dev 版应有清晰禁用日志", version)
		}
		if _, err := c.check("手动检查"); !errors.Is(err, errUpdaterDisabled) {
			t.Errorf("version=%q: check err = %v, want errUpdaterDisabled", version, err)
		}
		if err := c.install(); !errors.Is(err, errUpdaterDisabled) {
			t.Errorf("version=%q: install err = %v, want errUpdaterDisabled", version, err)
		}
	}
}

func TestAttachRealVersionInitsFramework(t *testing.T) {
	c, fw, em, logs := newTestController()
	t.Cleanup(c.cancel)
	attachReal(c, fw, em, "1.2.3")

	if fw.initCalled != 1 {
		t.Fatalf("Init 调用次数 = %d, want 1", fw.initCalled)
	}
	cfg := fw.initCfg
	if cfg.CurrentVersion != "1.2.3" {
		t.Errorf("CurrentVersion = %q", cfg.CurrentVersion)
	}
	if len(cfg.Providers) != 1 {
		t.Fatalf("Providers 数量 = %d, want 1（endpoint）", len(cfg.Providers))
	}
	if cfg.Providers[0].Name() != "endpoint" {
		t.Errorf("Provider = %q, want endpoint", cfg.Providers[0].Name())
	}
	if cfg.Window != updater.WindowNone {
		t.Error("必须使用 WindowNone（绝不弹框架窗口）")
	}
	// 严禁框架 CheckInterval：它每 tick 调 CheckAndInstall 自动下载安装
	if cfg.CheckInterval != 0 {
		t.Errorf("CheckInterval = %s, 必须为 0（自动定时检查由本应用实现且只 Check）", cfg.CheckInterval)
	}
	if info := c.snapshot(); info.State != updIdle || info.CurrentVersion != "1.2.3" {
		t.Errorf("初始状态 = %+v", info)
	}
	// 显式来源必须被使用并出现在日志中（便于排查构建注入结果）
	if c.manifestURL != testManifestURL {
		t.Errorf("manifestURL = %q, want %q", c.manifestURL, testManifestURL)
	}
	if !logs.contains(testManifestURL) {
		t.Errorf("显式更新源应出现在日志中：%v", logs.line)
	}
}

// TestAttachWithoutManifestURLDisables 构建未注入更新源（updateManifestURL 为空）时：
// 状态明确 disabled、不 Init 框架、检查/安装返回禁用错误，绝不使用任何隐式默认地址。
func TestAttachWithoutManifestURLDisables(t *testing.T) {
	logs := &logRecorder{}
	c := newUpdaterController(logs.logf) // 与无链接参数的构建一致
	fw := &fakeFW{}
	em := &emitRecorder{}
	t.Cleanup(c.cancel)
	attachReal(c, fw, em, "1.2.3")

	info := c.snapshot()
	if info.State != updDisabled {
		t.Errorf("未配置更新源 state = %q, want disabled", info.State)
	}
	if info.Message != "未配置更新源，更新检查已禁用" {
		t.Errorf("未配置更新源的提示 = %q", info.Message)
	}
	if fw.initCalled != 0 {
		t.Errorf("未配置更新源不得 Init 框架更新器")
	}
	if !logs.contains("未配置更新源") {
		t.Errorf("应有清晰禁用日志：%v", logs.line)
	}
	if _, err := c.check("手动检查"); !errors.Is(err, errUpdaterDisabled) {
		t.Errorf("check err = %v, want errUpdaterDisabled", err)
	}
	if err := c.install(); !errors.Is(err, errUpdaterDisabled) {
		t.Errorf("install err = %v, want errUpdaterDisabled", err)
	}
}

// --- 检查状态机 ---

func TestCheckAvailable(t *testing.T) {
	c, fw, em, logs := newTestController()
	t.Cleanup(c.cancel)
	attachReal(c, fw, em, "1.0.0")
	fw.checkRel = &updater.Release{Version: "2.0.0", Notes: "## 更新内容\n- 修复若干问题"}

	info, err := c.check("手动检查")
	if err != nil {
		t.Fatalf("check: %v", err)
	}
	if info.State != updAvailable || info.LatestVersion != "2.0.0" {
		t.Errorf("info = %+v, want available/2.0.0", info)
	}
	if info.Notes != fw.checkRel.Notes {
		t.Errorf("快照 Notes = %q, want 透传 %q", info.Notes, fw.checkRel.Notes)
	}
	found := false
	for _, name := range em.names() {
		if name == appUpdateEvent {
			found = true
		}
	}
	if !found {
		t.Errorf("事件名应包含 %s：%v", appUpdateEvent, em.names())
	}
	last := em.lastInfo()
	if last.State != updAvailable || last.LatestVersion != "2.0.0" {
		t.Errorf("事件 payload = %+v, want available/2.0.0", last)
	}
	if last.Notes != fw.checkRel.Notes {
		t.Errorf("事件 payload Notes = %q, want 透传 %q", last.Notes, fw.checkRel.Notes)
	}
	if !logs.contains("发现新版本 2.0.0") {
		t.Error("发现新版本应记日志")
	}
}

func TestCheckUpToDateClearsLatest(t *testing.T) {
	c, fw, em, _ := newTestController()
	t.Cleanup(c.cancel)
	attachReal(c, fw, em, "1.0.0")

	// 第一次：有新版（带更新日志）
	fw.checkRel = &updater.Release{Version: "2.0.0", Notes: "## 更新内容\n- 修复若干问题"}
	if _, err := c.check("手动检查"); err != nil {
		t.Fatal(err)
	}
	// 第二次：无新版 → latestVersion/Notes 必须清空，staged 包作废
	c.mu.Lock()
	c.stagedVersion = "2.0.0" // 模拟此前已 staged
	c.mu.Unlock()
	fw.checkRel = nil
	info, err := c.check("手动检查")
	if err != nil {
		t.Fatal(err)
	}
	if info.State != updUpToDate {
		t.Errorf("state = %q, want up-to-date", info.State)
	}
	if info.LatestVersion != "" {
		t.Errorf("无新版时 latestVersion 必须清空，got %q", info.LatestVersion)
	}
	if info.Notes != "" {
		t.Errorf("无新版时 Notes 必须清空，got %q", info.Notes)
	}
	if got := c.stagedVersionSnapshot(); got != "" {
		t.Errorf("无新版时 stagedVersion 必须作废，got %q", got)
	}
	if em.lastInfo().LatestVersion != "" || em.lastInfo().Notes != "" {
		t.Errorf("事件 payload 应清空 latestVersion/Notes，got %+v", em.lastInfo())
	}
}

func TestCheckErrorKeepsLatest(t *testing.T) {
	c, fw, _, _ := newTestController()
	t.Cleanup(c.cancel)
	attachReal(c, fw, &emitRecorder{}, "1.0.0")

	fw.checkRel = &updater.Release{Version: "2.0.0"}
	if _, err := c.check("手动检查"); err != nil {
		t.Fatal(err)
	}
	fw.checkRel = nil
	fw.checkErr = errors.New("network unreachable")

	info, err := c.check("手动检查")
	if err == nil {
		t.Fatal("检查失败应返回错误")
	}
	if info.State != updError {
		t.Errorf("state = %q, want error", info.State)
	}
	// 失败不回退已有发现：latestVersion 保留，UI 可重试安装
	if info.LatestVersion != "2.0.0" {
		t.Errorf("失败后 latestVersion = %q, want 保留 2.0.0", info.LatestVersion)
	}
}

// --- 忙拒绝（统一操作互斥） ---

func TestCheckBusyWhenOpHeld(t *testing.T) {
	c, fw, _, _ := newTestController()
	t.Cleanup(c.cancel)
	attachReal(c, fw, &emitRecorder{}, "1.0.0")

	c.opMu.Lock() // 模拟另一操作（检查/下载/重启）在途
	before := c.snapshot()
	_, err := c.check("手动检查")
	c.opMu.Unlock()
	if !errors.Is(err, errUpdaterBusy) {
		t.Errorf("err = %v, want errUpdaterBusy", err)
	}
	if c.snapshot().State != before.State {
		t.Errorf("忙拒绝不得改变状态：%q → %q", before.State, c.snapshot().State)
	}
	if fw.checkCalls() != 0 {
		t.Error("忙时不得发起网络检查")
	}
}

// --- 安装流程（同步完成，错误直接返回） ---

func TestInstallWithoutCheckRejected(t *testing.T) {
	c, fw, _, _ := newTestController()
	t.Cleanup(c.cancel)
	attachReal(c, fw, &emitRecorder{}, "1.0.0")

	err := c.install()
	if !errors.Is(err, errNoPendingUpdate) {
		t.Errorf("err = %v, want errNoPendingUpdate", err)
	}
	if dl, _ := fw.calls(); dl != 0 {
		t.Error("未检查到新版绝不得开始下载")
	}
}

func TestInstallHappyPath(t *testing.T) {
	c, fw, em, logs := newTestController()
	t.Cleanup(c.cancel)
	attachReal(c, fw, em, "1.0.0")
	fw.checkRel = &updater.Release{Version: "2.0.0"}
	if _, err := c.check("手动检查"); err != nil {
		t.Fatal(err)
	}

	// 同步完成：返回 nil 即全部落定，无需等待 goroutine
	if err := c.install(); err != nil {
		t.Fatalf("install: %v", err)
	}
	dl, restart := fw.calls()
	if dl != 1 || restart != 1 {
		t.Fatalf("dl=%d restart=%d, want 1/1", dl, restart)
	}
	info := c.snapshot()
	if info.State != updReady || info.LatestVersion != "2.0.0" || info.Progress != 100 {
		t.Errorf("info = %+v, want ready/2.0.0/100", info)
	}
	if got := c.stagedVersionSnapshot(); got != "2.0.0" {
		t.Errorf("stagedVersion = %q, want 2.0.0", got)
	}
	if em.lastInfo().State != updReady {
		t.Errorf("最终事件 = %+v, want ready", em.lastInfo())
	}
	if !logs.contains("用户确认安装") || !logs.contains("重启流程已启动") {
		t.Error("安装与重启应记日志")
	}
}

func TestInstallDownloadErrorReturnedDirectly(t *testing.T) {
	c, fw, _, logs := newTestController()
	t.Cleanup(c.cancel)
	attachReal(c, fw, &emitRecorder{}, "1.0.0")
	fw.checkRel = &updater.Release{Version: "2.0.0"}
	if _, err := c.check("手动检查"); err != nil {
		t.Fatal(err)
	}

	// 下载失败：实际错误同步返回给前端 RPC，状态进 error 且 latestVersion 保留
	dlErr := errors.New("HTTP 500")
	fw.dlErr = dlErr
	err := c.install()
	if !errors.Is(err, dlErr) {
		t.Fatalf("install err = %v, want 原始下载错误 %v", err, dlErr)
	}
	info := c.snapshot()
	if info.State != updError {
		t.Errorf("state = %q, want error", info.State)
	}
	if info.LatestVersion != "2.0.0" {
		t.Errorf("失败后 latestVersion = %q, want 保留", info.LatestVersion)
	}
	if got := c.stagedVersionSnapshot(); got != "" {
		t.Errorf("下载失败不得置 staged，got %q", got)
	}
	if !logs.contains("下载/校验/安装失败") {
		t.Error("下载失败应记日志")
	}

	// 失败后可重试：重新下载成功
	fw.dlErr = nil
	if err := c.install(); err != nil {
		t.Fatalf("重试 install: %v", err)
	}
	dl, restart := fw.calls()
	if dl != 2 || restart != 1 {
		t.Errorf("dl=%d restart=%d, want 2/1", dl, restart)
	}
	if info := c.snapshot(); info.State != updReady {
		t.Errorf("重试后 state = %q, want ready", info.State)
	}
}

func TestInstallRestartErrorReturnedAndSkipsRedownload(t *testing.T) {
	c, fw, _, _ := newTestController()
	t.Cleanup(c.cancel)
	attachReal(c, fw, &emitRecorder{}, "1.0.0")
	fw.checkRel = &updater.Release{Version: "2.0.0"}
	if _, err := c.check("手动检查"); err != nil {
		t.Fatal(err)
	}

	// 下载成功但重启失败（helper 拉起失败等）：实际错误同步返回
	restartErr := errors.New("spawn helper failed")
	fw.restartErr = restartErr
	if err := c.install(); !errors.Is(err, restartErr) {
		t.Fatalf("install err = %v, want 原始重启错误 %v", err, restartErr)
	}
	if c.snapshot().State != updError {
		t.Errorf("state = %q, want error", c.snapshot().State)
	}
	if got := c.stagedVersionSnapshot(); got != "2.0.0" {
		t.Errorf("下载成功后 stagedVersion = %q, want 2.0.0", got)
	}

	// 重试：同版已 staged，跳过下载直接重启
	fw.restartErr = nil
	if err := c.install(); err != nil {
		t.Fatalf("重试 install: %v", err)
	}
	dl, restart := fw.calls()
	if dl != 1 || restart != 2 {
		t.Errorf("dl=%d restart=%d, want 1/2（不得重复下载）", dl, restart)
	}
	if info := c.snapshot(); info.State != updReady {
		t.Errorf("重试后 state = %q, want ready", info.State)
	}
}

func TestStagedVersionMismatchRedownloads(t *testing.T) {
	c, fw, _, _ := newTestController()
	t.Cleanup(c.cancel)
	attachReal(c, fw, &emitRecorder{}, "1.0.0")

	// 2.0.0 下载成功但重启失败 → staged=2.0.0
	fw.checkRel = &updater.Release{Version: "2.0.0"}
	if _, err := c.check("手动检查"); err != nil {
		t.Fatal(err)
	}
	fw.restartErr = errors.New("spawn helper failed")
	if err := c.install(); err == nil {
		t.Fatal("重启失败应返回错误")
	}
	if got := c.stagedVersionSnapshot(); got != "2.0.0" {
		t.Fatalf("stagedVersion = %q, want 2.0.0", got)
	}

	// 源出现 2.1.0：旧 staged 包必须作废，重新下载新包
	fw.restartErr = nil
	fw.checkRel = &updater.Release{Version: "2.1.0"}
	info, err := c.check("手动检查")
	if err != nil {
		t.Fatal(err)
	}
	if info.LatestVersion != "2.1.0" {
		t.Fatalf("latestVersion = %q, want 2.1.0", info.LatestVersion)
	}
	if got := c.stagedVersionSnapshot(); got != "" {
		t.Fatalf("发现不同新版本后 stagedVersion = %q, want 空", got)
	}

	// 安装：必须重新下载（绝不拿 2.0.0 旧包安装 2.1.0）
	if err := c.install(); err != nil {
		t.Fatal(err)
	}
	dl, restart := fw.calls()
	if dl != 2 || restart != 2 {
		t.Errorf("dl=%d restart=%d, want 2/2", dl, restart)
	}
	if info := c.snapshot(); info.LatestVersion != "2.1.0" || info.State != updReady {
		t.Errorf("info = %+v, want 2.1.0/ready", info)
	}
}

func TestInstallBusyWhenOpHeld(t *testing.T) {
	c, fw, _, _ := newTestController()
	t.Cleanup(c.cancel)
	attachReal(c, fw, &emitRecorder{}, "1.0.0")
	fw.checkRel = &updater.Release{Version: "2.0.0"}
	if _, err := c.check("手动检查"); err != nil {
		t.Fatal(err)
	}

	c.opMu.Lock() // 模拟另一操作在途
	err := c.install()
	c.opMu.Unlock()
	if !errors.Is(err, errUpdaterBusy) {
		t.Errorf("err = %v, want errUpdaterBusy", err)
	}
	if dl, _ := fw.calls(); dl != 0 {
		t.Error("忙拒绝不得触发下载")
	}
}

// --- 真正阻塞中的 check/install 相互排斥（互斥覆盖框架调用全程） ---

func TestCheckAndInstallMutualExclusion(t *testing.T) {
	c, fw, _, _ := newTestController()
	t.Cleanup(c.cancel)
	attachReal(c, fw, &emitRecorder{}, "1.0.0")

	// 一、检查阻塞在框架 Check 中：install 必须被拒
	fw.checkRel = &updater.Release{Version: "2.0.0"}
	fw.checkBlock = make(chan struct{})
	fw.checkOpened = make(chan struct{})
	checkDone := make(chan error, 1)
	go func() {
		_, err := c.check("手动检查")
		checkDone <- err
	}()
	<-fw.checkOpened // 已进入 Check（opMu 被持有）
	if err := c.install(); !errors.Is(err, errUpdaterBusy) {
		t.Errorf("检查阻塞中 install err = %v, want errUpdaterBusy", err)
	}
	if dl, _ := fw.calls(); dl != 0 {
		t.Error("检查阻塞中不得开始下载")
	}
	close(fw.checkBlock)
	if err := <-checkDone; err != nil {
		t.Fatalf("check: %v", err)
	}
	if c.snapshot().State != updAvailable {
		t.Fatalf("state = %q, want available", c.snapshot().State)
	}

	// 二、安装阻塞在框架 DownloadAndInstall 中：check 必须被拒
	fw.dlBlock = make(chan struct{})
	fw.dlOpened = make(chan struct{})
	installDone := make(chan error, 1)
	go func() {
		installDone <- c.install()
	}()
	<-fw.dlOpened
	checksBefore := fw.checkCalls()
	if _, err := c.check("自动定时检查"); !errors.Is(err, errUpdaterBusy) {
		t.Errorf("下载阻塞中 check err = %v, want errUpdaterBusy", err)
	}
	if fw.checkCalls() != checksBefore {
		t.Error("下载阻塞中不得发起网络检查")
	}
	close(fw.dlBlock)
	if err := <-installDone; err != nil {
		t.Fatalf("install: %v", err)
	}
	if info := c.snapshot(); info.State != updReady || info.Progress != 100 {
		t.Errorf("info = %+v, want ready/100", info)
	}
}

// --- 框架事件镜像与迟到事件防回退 ---

func TestOnProgressMirrorAndLateGuard(t *testing.T) {
	c, fw, _, _ := newTestController()
	t.Cleanup(c.cancel)
	attachReal(c, fw, &emitRecorder{}, "1.0.0")

	// 正常镜像：本地与框架均处于 downloading
	c.applyState(updDownloading, "")
	c.setProgress(0, "")
	fw.state = updater.StateDownloading
	c.onProgress(updater.Progress{Written: 50, Total: 100})
	info := c.snapshot()
	if info.Progress != 50 || !strings.Contains(info.Message, "50") {
		t.Errorf("info = %+v, want progress 50", info)
	}
	// 相同百分比不重复推送
	c.onProgress(updater.Progress{Written: 55, Total: 110})
	if c.snapshot().Progress != 50 {
		t.Errorf("相同百分比不得更新，progress = %d", c.snapshot().Progress)
	}

	// 进度只增不减：迟到的小百分比不得回退
	c.setProgress(80, "")
	c.onProgress(updater.Progress{Written: 10, Total: 100})
	if c.snapshot().Progress != 80 {
		t.Errorf("迟到小百分比回退了进度：%d, want 80", c.snapshot().Progress)
	}
	// clamp 到 0..100
	c.onProgress(updater.Progress{Written: 150, Total: 100})
	if c.snapshot().Progress != 100 {
		t.Errorf("超界百分比未收敛到 100：%d", c.snapshot().Progress)
	}

	// 迟到事件：框架已完成（ready），进度事件不得回退/污染状态
	fw.state = updater.StateReady
	c.applyState(updReady, "更新已就绪")
	c.onProgress(updater.Progress{Written: 100, Total: 100})
	info = c.snapshot()
	if info.State != updReady || info.Progress != 100 {
		t.Errorf("迟到进度事件回退了状态：%+v", info)
	}

	// 本地非 downloading（例如检查中），框架进度事件不应用
	fw.state = updater.StateDownloading
	c.applyState(updChecking, "")
	c.onProgress(updater.Progress{Written: 10, Total: 100})
	if c.snapshot().State != updChecking {
		t.Errorf("进度事件不得改变非 downloading 状态：%q", c.snapshot().State)
	}
}

func TestVerifyingInstallingMirrorAndLateGuard(t *testing.T) {
	c, fw, _, logs := newTestController()
	t.Cleanup(c.cancel)
	attachReal(c, fw, &emitRecorder{}, "1.0.0")

	c.applyState(updDownloading, "")
	fw.state = updater.StateVerifying // 框架进入校验阶段
	c.onVerifying()
	if c.snapshot().State != updVerifying {
		t.Fatalf("state = %q, want verifying", c.snapshot().State)
	}
	fw.state = updater.StateInstalling
	c.onInstalling()
	if c.snapshot().State != updInstalling {
		t.Fatalf("state = %q, want installing", c.snapshot().State)
	}
	if !logs.contains("校验") || !logs.contains("staging") {
		t.Error("校验/安装阶段应记日志")
	}

	// 迟到的 verifying 事件（框架实际已完成 + 本地已 ready）不得回退到 verifying
	fw.state = updater.StateVerifying
	c.applyState(updReady, "更新已就绪，即将重启")
	c.onVerifying()
	if c.snapshot().State != updReady {
		t.Errorf("迟到 verifying 事件回退了状态：%q", c.snapshot().State)
	}
}

// --- 并发事件终态一致性 ---

func TestNotifyConcurrentFinalState(t *testing.T) {
	c, fw, em, _ := newTestController()
	t.Cleanup(c.cancel)
	attachReal(c, fw, em, "1.0.0")

	// 多 goroutine 并发推进状态/进度：notify 串行化后事件终态必须等于当下快照，
	// 不得以旧捕获值收尾（前端倒退）
	var wg sync.WaitGroup
	for i := 1; i <= 50; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			c.applyState(updDownloading, "并发推进")
			c.setProgress(i, "")
		}(i)
	}
	wg.Wait()
	// applyState/setProgress 同步调用 notify（emitMu 串行但同步发出），
	// wg.Wait 返回时全部事件已发出
	last, cur := em.lastInfo(), c.snapshot()
	if last != cur {
		t.Errorf("最终事件 %+v != 当前快照 %+v（终态被旧值覆盖）", last, cur)
	}
	if last.Progress < 1 || last.Progress > 50 {
		t.Errorf("终态进度越界：%d", last.Progress)
	}
}

// --- 生命周期 ---

func TestShutdownCancelsOps(t *testing.T) {
	c, fw, _, logs := newTestController()
	attachReal(c, fw, &emitRecorder{}, "1.0.0")

	c.shutdown()
	if _, err := c.check("手动检查"); !errors.Is(err, errUpdaterShuttingD) {
		t.Errorf("err = %v, want errUpdaterShuttingD", err)
	}
	if err := c.install(); !errors.Is(err, errUpdaterShuttingD) {
		t.Errorf("err = %v, want errUpdaterShuttingD", err)
	}
	waitFor(t, func() bool { return logs.contains("后台定时检查已停止") }, "定时检查 goroutine 应退出")
}

// --- 辅助函数 ---

func TestHumanBytes(t *testing.T) {
	cases := []struct {
		in   int64
		want string
	}{
		{0, "0 B"},
		{512, "512 B"},
		{1024, "1.0 KB"},
		{1536, "1.5 KB"},
		{5 * 1024 * 1024, "5.0 MB"},
	}
	for _, tc := range cases {
		if got := humanBytes(tc.in); got != tc.want {
			t.Errorf("humanBytes(%d) = %q, want %q", tc.in, got, tc.want)
		}
	}
}
