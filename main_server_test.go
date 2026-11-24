//go:build server

package main

import (
	"bytes"
	"errors"
	"flag"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"frpdesk/internal/configstore"
	"frpdesk/internal/i18n"
)

func TestParseServeOptionsDefaults(t *testing.T) {
	opts, err := parseServeOptions(nil)
	if err != nil {
		t.Fatalf("parseServeOptions(nil): %v", err)
	}
	if opts.Host != "127.0.0.1" || opts.Port != 8080 || opts.ConfigDir != "" {
		t.Fatalf("默认参数不符：%+v", opts)
	}
}

func TestParseServeOptionsCustom(t *testing.T) {
	opts, err := parseServeOptions([]string{
		"--host", "0.0.0.0",
		"--port", "9090",
		"--config-dir", "/tmp/frpdesk-test",
	})
	if err != nil {
		t.Fatalf("parseServeOptions: %v", err)
	}
	if opts.Host != "0.0.0.0" || opts.Port != 9090 || opts.ConfigDir != "/tmp/frpdesk-test" {
		t.Fatalf("自定义参数不符：%+v", opts)
	}

	// IPv6 方括号形式归一，交给 net.JoinHostPort 重新加括号
	opts, err = parseServeOptions([]string{"--host", "[::1]"})
	if err != nil {
		t.Fatalf("parseServeOptions ipv6: %v", err)
	}
	if opts.Host != "::1" {
		t.Fatalf("IPv6 host = %q, want ::1", opts.Host)
	}
}

func TestParseServeOptionsInvalid(t *testing.T) {
	cases := []struct {
		name string
		args []string
	}{
		{"端口为 0", []string{"--port", "0"}},
		{"端口超出范围", []string{"--port", "65536"}},
		{"端口非数字", []string{"--port", "abc"}},
		{"地址为空", []string{"--host", "  "}},
		{"未知参数", []string{"--bogus"}},
		{"多余位置参数", []string{"extra"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := parseServeOptions(tc.args); err == nil {
				t.Fatalf("parseServeOptions(%v) 应返回错误", tc.args)
			}
		})
	}
}

func TestParseServeOptionsHelp(t *testing.T) {
	_, err := parseServeOptions([]string{"--help"})
	if !errors.Is(err, flag.ErrHelp) {
		t.Fatalf("--help 应返回 flag.ErrHelp，实际 %v", err)
	}
}

func TestRunCLICommands(t *testing.T) {
	var out, errOut bytes.Buffer
	if code := runCLI([]string{"--version"}, &out, &errOut); code != cliExitOK {
		t.Fatalf("--version 退出码 = %d，want %d", code, cliExitOK)
	}
	if got := strings.TrimSpace(out.String()); got != currentVersion {
		t.Fatalf("--version 输出 = %q，want %q", got, currentVersion)
	}

	out.Reset()
	if code := runCLI([]string{"--help"}, &out, &errOut); code != cliExitOK {
		t.Fatalf("--help 退出码 = %d，want %d", code, cliExitOK)
	}
	if !strings.Contains(out.String(), "frpdesk serve") {
		t.Fatalf("--help 输出缺少用法：%q", out.String())
	}

	out.Reset()
	errOut.Reset()
	if code := runCLI(nil, &out, &errOut); code != cliExitUsage {
		t.Fatalf("无命令退出码 = %d，want %d", code, cliExitUsage)
	}
	if !strings.Contains(errOut.String(), "missing command") {
		t.Fatalf("无命令 stderr = %q", errOut.String())
	}

	errOut.Reset()
	if code := runCLI([]string{"bogus"}, &out, &errOut); code != cliExitUsage {
		t.Fatalf("未知命令退出码 = %d，want %d", code, cliExitUsage)
	}

	errOut.Reset()
	if code := runCLI([]string{"serve", "--port", "0"}, &out, &errOut); code != cliExitUsage {
		t.Fatalf("非法端口退出码 = %d，want %d", code, cliExitUsage)
	}
	if !strings.Contains(errOut.String(), "invalid arguments") {
		t.Fatalf("非法端口 stderr = %q", errOut.String())
	}

	out.Reset()
	if code := runCLI([]string{"serve", "--help"}, &out, &errOut); code != cliExitOK {
		t.Fatalf("serve --help 退出码 = %d，want %d", code, cliExitOK)
	}
}

func TestRunServePortInUse(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("占用端口失败：%v", err)
	}
	defer ln.Close()
	port := ln.Addr().(*net.TCPAddr).Port

	var out, errOut bytes.Buffer
	code := runCLI([]string{
		"serve",
		"--host", "127.0.0.1",
		"--port", strconv.Itoa(port),
		"--config-dir", t.TempDir(),
	}, &out, &errOut)
	if code != cliExitRuntime {
		t.Fatalf("端口占用退出码 = %d，want %d（stderr=%s）", code, cliExitRuntime, errOut.String())
	}
	if !strings.Contains(errOut.String(), "cannot listen") {
		t.Fatalf("端口占用 stderr = %q", errOut.String())
	}
}

func TestOpenServerStore(t *testing.T) {
	dir := t.TempDir()
	store, path, err := openServerStore(dir)
	if err != nil {
		t.Fatalf("openServerStore: %v", err)
	}
	if store == nil {
		t.Fatal("openServerStore 返回 nil store")
	}
	want := filepath.Join(dir, "config.json")
	if path != want {
		t.Fatalf("配置路径 = %q，want %q", path, want)
	}
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("配置目录初始化后应存在 %s：%v", path, err)
	}

	// --config-dir 指向文件而不是目录：必须明确失败，不能把配置写进文件路径
	notDir := filepath.Join(t.TempDir(), "not-a-dir")
	if err := os.WriteFile(notDir, []byte("x"), 0o644); err != nil {
		t.Fatalf("写临时文件：%v", err)
	}
	if _, _, err := openServerStore(notDir); err == nil {
		t.Fatal("--config-dir 指向普通文件时应返回错误")
	}
}

func TestAccessURL(t *testing.T) {
	cases := []struct {
		host string
		port int
		want string
	}{
		{"127.0.0.1", 8080, "http://127.0.0.1:8080"},
		{"0.0.0.0", 9000, "http://127.0.0.1:9000"},
		{"::", 8080, "http://[::1]:8080"},
		{"::1", 8080, "http://[::1]:8080"},
		{"192.168.1.5", 7000, "http://192.168.1.5:7000"},
	}
	for _, tc := range cases {
		if got := accessURL(serveOptions{Host: tc.host, Port: tc.port}); got != tc.want {
			t.Fatalf("accessURL(%s:%d) = %q，want %q", tc.host, tc.port, got, tc.want)
		}
	}
}

func TestInjectServerMode(t *testing.T) {
	html := []byte("<!doctype html><html><head><title>x</title></head><body></body></html>")
	got := string(injectServerMode(html))
	if strings.Count(got, serverModeMarker) != 1 {
		t.Fatalf("标记应恰好注入一次：%s", got)
	}
	if !strings.HasPrefix(got, "<!doctype html><html><head><script>") {
		t.Fatalf("标记应紧跟 <head> 之后：%s", got)
	}

	noHead := string(injectServerMode([]byte("<html></html>")))
	if !strings.HasPrefix(noHead, serverModeMarker) {
		t.Fatalf("无 <head> 时应前置标记：%s", noHead)
	}
}

func TestServerModeGuards(t *testing.T) {
	prevLang := i18n.Current()
	t.Cleanup(func() { i18n.Set(prevLang) })

	store := configstore.NewAtPath(filepath.Join(t.TempDir(), "config.json"))
	svc := newAppService(store, appModeServer)

	info := svc.RuntimeInfo()
	if info.Mode != string(appModeServer) || info.Version != currentVersion {
		t.Fatalf("RuntimeInfo = %+v", info)
	}

	if st, err := svc.LaunchAtLogin(); err != nil || st.Supported {
		t.Fatalf("服务端 LaunchAtLogin 应返回 unsupported 且无错误：%+v, %v", st, err)
	}
	if st, err := svc.SetLaunchAtLogin(true); err == nil || st.Supported {
		t.Fatalf("服务端 SetLaunchAtLogin 应明确失败：%+v, %v", st, err)
	}

	upd := svc.UpdateInfo()
	if upd.State != updDisabled || upd.Message == "" {
		t.Fatalf("服务端 UpdateInfo 应为 disabled 并带说明：%+v", upd)
	}
	if _, err := svc.CheckForUpdates(); err == nil {
		t.Fatal("服务端 CheckForUpdates 应返回错误")
	}
	if err := svc.InstallUpdate(); err == nil {
		t.Fatal("服务端 InstallUpdate 应返回错误")
	}

	// 退出按钮必须是 no-op：不能把共享服务直接杀掉
	svc.QuitApp()

	// 防御性 guard：服务端 attachUI 不应装配任何桌面对象
	svc.attachUI(nil, nil)
	if svc.tray != nil || svc.wailsApp != nil {
		t.Fatal("服务端模式不应装配托盘/窗口")
	}
}

func TestServerLifecycle(t *testing.T) {
	if testing.Short() {
		t.Skip("跳过真实 HTTP 服务端生命周期测试")
	}
	prevLang := i18n.Current()
	t.Cleanup(func() { i18n.Set(prevLang) })

	// Wails 一个进程只维护一个全局 App：同一测试二进制不能先后启动两个真实服务。
	// 通过 FRPDESK_TEST_SERVE_HOST 分别验证 IPv4 与 IPv6 监听。
	host := os.Getenv("FRPDESK_TEST_SERVE_HOST")
	if host == "" {
		host = "127.0.0.1"
	}

	opts := newLifecycleServeOptions(t, host)
	store := configstore.NewAtPath(filepath.Join(t.TempDir(), "config.json"))
	// 预置一台「已启用隧道」的服务器：serve 启动时必须自动 reconcile（连接尝试），
	// 即使 frps 不可达也应离开 stopped 态（starting/failed），证明启动流程生效。
	cfg, err := store.CreateServer(configstore.ServerInput{
		Name:        "smoke",
		ServerAddr:  "127.0.0.1",
		ServerPort:  freeTCPPort(t, "127.0.0.1"),
		HTTPEnabled: false,
	})
	if err != nil {
		t.Fatalf("CreateServer: %v", err)
	}
	serverID := cfg.Servers[0].ID
	if _, err := store.CreateService(configstore.ServiceInput{
		ServerID:   serverID,
		Name:       "smoke",
		LocalHost:  "127.0.0.1",
		LocalPort:  12345,
		Enabled:    true,
		Type:       configstore.TypeTCP,
		RemotePort: 23456,
	}); err != nil {
		t.Fatalf("CreateService: %v", err)
	}
	app, svc := newServerApplication(opts, store)
	// 无头模式没有 attachUI，但事件出口必须已绑定（前端依赖 WebSocket 推送）
	if svc.wailsApp != app || svc.tray != nil {
		t.Fatal("服务端事件出口未绑定或误装配托盘")
	}

	errCh := make(chan error, 1)
	go func() { errCh <- runServerApp(app, opts, io.Discard) }()
	base := accessURL(opts)
	waitReachable(t, base+"/health", 8*time.Second)
	// 启动时已对 enabled 隧道执行 reconcile（frps 不可达 → starting/failed，而非 stopped）
	snap := svc.Snapshot()
	state := configstore.StateStopped
	for _, st := range snap.ServerStatuses {
		if st.ServerID == serverID {
			state = st.State
		}
	}
	if state == configstore.StateStopped {
		t.Fatalf("serve 启动后 enabled 隧道仍为 stopped：%+v", snap.ServerStatuses)
	}

	// HTML：注入服务端标记，前端据此隐藏桌面入口
	body := httpGet(t, base+"/")
	for _, want := range []string{"__FRPDESK_SERVER__", "Frpdesk"} {
		if !strings.Contains(string(body), want) {
			t.Fatalf("首页缺少 %q", want)
		}
	}

	// Wails runtime 与 RPC 健康：System.Flags 返回 server=true
	if runtimeJS := httpGet(t, base+"/wails/runtime.js"); len(runtimeJS) == 0 {
		t.Fatal("/wails/runtime.js 内容为空")
	}
	rpc := httpPostJSON(t, base+"/wails/runtime", `{"object":8,"method":3,"args":{}}`)
	if !strings.Contains(string(rpc), `"server":true`) {
		t.Fatalf("System.Flags RPC 未返回 server=true：%s", rpc)
	}

	app.Quit()
	select {
	case err := <-errCh:
		if err != nil {
			t.Fatalf("优雅退出返回错误：%v", err)
		}
	case <-time.After(8 * time.Second):
		t.Fatal("app.Quit 后服务未在 8s 内退出")
	}
}

func TestOpenServerStoreUsesPrivateDirectory(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "nested-config")
	if _, _, err := openServerStore(dir); err != nil {
		t.Fatalf("openServerStore(%q): %v", dir, err)
	}
	info, err := os.Stat(dir)
	if err != nil {
		t.Fatalf("检查配置目录失败：%v", err)
	}
	if got := info.Mode().Perm(); got != 0o700 {
		t.Fatalf("配置目录权限 = %04o，want 0700", got)
	}

	if err := os.Chmod(dir, 0o755); err != nil {
		t.Fatalf("预设宽松目录权限失败：%v", err)
	}
	if _, _, err := openServerStore(dir); err != nil {
		t.Fatalf("重复 openServerStore(%q): %v", dir, err)
	}
	info, err = os.Stat(dir)
	if err != nil {
		t.Fatalf("重新检查配置目录失败：%v", err)
	}
	if got := info.Mode().Perm(); got != 0o700 {
		t.Fatalf("已存在配置目录权限 = %04o，want 0700", got)
	}
}

func TestWaitHealthyIgnoresHTTPProxy(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/health" {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"status":"ok"}`))
	}))
	defer server.Close()

	proxyRequests := make(chan string, 1)
	proxy := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		proxyRequests <- r.URL.String()
		w.WriteHeader(http.StatusBadGateway)
	}))
	defer proxy.Close()

	t.Setenv("HTTP_PROXY", proxy.URL)
	t.Setenv("http_proxy", proxy.URL)
	t.Setenv("ALL_PROXY", proxy.URL)
	t.Setenv("all_proxy", proxy.URL)

	errCh := make(chan error, 1)
	if err := waitHealthy(errCh, server.URL, 2*time.Second); err != nil {
		t.Fatalf("waitHealthy 应绕过代理访问本地健康检查：%v", err)
	}
	select {
	case url := <-proxyRequests:
		t.Fatalf("健康检查不应访问代理：%s", url)
	default:
	}
}

func TestWaitHealthyTreatsNilExitAsGraceful(t *testing.T) {
	errCh := make(chan error, 1)
	errCh <- nil
	// 服务在健康检查完成前优雅退出是正常关闭，不能返回错误。
	if err := waitHealthy(errCh, "http://127.0.0.1:1", 100*time.Millisecond); err != nil {
		t.Fatalf("waitHealthy(nil) = %v，want nil", err)
	}
}

func newLifecycleServeOptions(t *testing.T, host string) serveOptions {
	t.Helper()
	port := freeTCPPort(t, host)
	opts := serveOptions{Host: host, Port: port}
	if err := preflightListen(host, port); err != nil {
		t.Skipf("测试环境不支持监听 %s：%v", host, err)
	}
	return opts
}

func freeTCPPort(t *testing.T, host string) int {
	t.Helper()
	addr := net.JoinHostPort(host, "0")
	ln, err := net.Listen("tcp", addr)
	if err != nil {
		t.Skipf("分配 %s 空闲端口失败：%v", host, err)
	}
	defer ln.Close()
	return ln.Addr().(*net.TCPAddr).Port
}

func waitReachable(t *testing.T, url string, timeout time.Duration) {
	t.Helper()
	client := &http.Client{Timeout: 500 * time.Millisecond}
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		resp, err := client.Get(url)
		if err == nil {
			resp.Body.Close()
			if resp.StatusCode == http.StatusOK {
				return
			}
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatalf("%s 在 %s 内未就绪", url, timeout)
}

func httpGet(t *testing.T, url string) []byte {
	t.Helper()
	resp, err := http.Get(url)
	if err != nil {
		t.Fatalf("GET %s: %v", url, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("GET %s 状态码 = %d", url, resp.StatusCode)
	}
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("读取 %s: %v", url, err)
	}
	return body
}

func httpPostJSON(t *testing.T, url, body string) []byte {
	t.Helper()
	resp, err := http.Post(url, "application/json", strings.NewReader(body))
	if err != nil {
		t.Fatalf("POST %s: %v", url, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("POST %s 状态码 = %d", url, resp.StatusCode)
	}
	data, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("读取 %s: %v", url, err)
	}
	return data
}
