//go:build server

// frpdesk 服务端 CLI 入口：`frpdesk serve` 在 Linux 服务器上提供与桌面版一致的
// 网页管理界面（静态前端 + Wails RPC/WebSocket），不创建窗口、托盘或自动更新。
//
// 与桌面入口（main.go，!server）互斥：两者都输出 bin/frpdesk 形态的可执行文件，
// 但构建标签不同（服务端额外使用 server tag；CGO_ENABLED=0，可交叉编译 Linux）。
package main

import (
	"bytes"
	"errors"
	"flag"
	"fmt"
	"io"
	"io/fs"
	"log/slog"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/wailsapp/wails/v3/pkg/application"

	"frpdesk/internal/configstore"
	"frpdesk/internal/i18n"
)

// 退出码：0 成功；1 运行时失败（配置/端口/服务异常）；2 参数或命令错误。
const (
	cliExitOK      = 0
	cliExitRuntime = 1
	cliExitUsage   = 2
)

const serveUsage = `frpdesk serve - run the Frpdesk web admin UI without a window or tray

Usage:
  frpdesk serve [--host ADDR] [--port PORT] [--config-dir DIR]
  frpdesk --version
  frpdesk --help

Options:
  --host         listen address (default 127.0.0.1; use 0.0.0.0 only on a trusted LAN)
  --port         listen port (default 8080; range 1-65535)
  --config-dir   config directory containing config.json (default: frpdesk under the
                 platform user config directory)
  -h, --help     show this help
  --version      print the version and exit

Notes:
  The web admin UI has no built-in authentication. Bind it to localhost or a trusted
  network only; for remote access use an SSH port forward.
  Do not run the desktop app and serve against the same config directory at the same
  time (concurrent writes are not supported).
  Enabled tunnels connect automatically on startup; Ctrl+C or SIGTERM shuts down
  gracefully.
`

// serveOptions 是 serve 子命令的有效参数（CLI 参数为唯一权威）。
type serveOptions struct {
	Host      string
	Port      int
	ConfigDir string
}

func main() {
	os.Exit(runCLI(os.Args[1:], os.Stdout, os.Stderr))
}

// runCLI 解析命令并返回退出码（不直接 os.Exit，便于单元测试）。
func runCLI(args []string, stdout, stderr io.Writer) int {
	if len(args) == 0 {
		fmt.Fprintln(stderr, `missing command: use "frpdesk serve" (see --help)`)
		fmt.Fprint(stderr, serveUsage)
		return cliExitUsage
	}
	switch args[0] {
	case "-h", "--help", "help":
		fmt.Fprint(stdout, serveUsage)
		return cliExitOK
	case "--version", "-version", "version":
		fmt.Fprintln(stdout, currentVersion)
		return cliExitOK
	case "serve":
		return runServe(args[1:], stdout, stderr)
	default:
		fmt.Fprintf(stderr, "unknown command %q (available: serve; see --help)\n", args[0])
		return cliExitUsage
	}
}

// parseServeOptions 解析 serve 参数；flag.ErrHelp 单独上抛供调用方以 0 退出。
func parseServeOptions(args []string) (serveOptions, error) {
	var opts serveOptions
	fs := flag.NewFlagSet("serve", flag.ContinueOnError)
	fs.SetOutput(io.Discard) // 错误由调用方统一渲染，避免重复输出
	fs.StringVar(&opts.Host, "host", "127.0.0.1", "listen address")
	fs.IntVar(&opts.Port, "port", 8080, "listen port")
	fs.StringVar(&opts.ConfigDir, "config-dir", "", "config directory")
	if err := fs.Parse(args); err != nil {
		return opts, err
	}
	if fs.NArg() > 0 {
		return opts, fmt.Errorf("serve does not accept positional arguments: %s", strings.Join(fs.Args(), " "))
	}
	opts.Host = strings.TrimSpace(opts.Host)
	// 允许传入 [::1] 形式，统一去掉方括号（net.JoinHostPort 会自行补）
	opts.Host = strings.TrimSuffix(strings.TrimPrefix(opts.Host, "["), "]")
	if opts.Host == "" {
		return opts, errors.New("--host must not be empty")
	}
	if opts.Port < 1 || opts.Port > 65535 {
		return opts, fmt.Errorf("--port must be between 1 and 65535: %d", opts.Port)
	}
	return opts, nil
}

// runServe 执行 serve：加载配置、预检端口、启动 Wails server 并等待退出。
func runServe(args []string, stdout, stderr io.Writer) int {
	opts, err := parseServeOptions(args)
	if errors.Is(err, flag.ErrHelp) {
		fmt.Fprint(stdout, serveUsage)
		return cliExitOK
	}
	if err != nil {
		fmt.Fprintf(stderr, "invalid arguments: %v\n\n", err)
		fmt.Fprint(stderr, serveUsage)
		return cliExitUsage
	}

	store, configPath, err := openServerStore(opts.ConfigDir)
	if err != nil {
		fmt.Fprintf(stderr, "[serve] failed to initialize config: %v\n", err)
		return cliExitRuntime
	}

	// Wails server 支持 WAILS_SERVER_HOST/PORT 环境变量并会覆盖 Options；
	// CLI 参数是唯一权威，清掉这两个隐藏覆盖，保证打印地址与实际监听一致。
	_ = os.Unsetenv("WAILS_SERVER_HOST")
	_ = os.Unsetenv("WAILS_SERVER_PORT")

	// 预检端口：绑定失败在创建 Wails App 之前以非零退出，避免半初始化状态。
	if err := preflightListen(opts.Host, opts.Port); err != nil {
		fmt.Fprintf(stderr, "[serve] cannot listen on %s: %v\n", listenAddress(opts), err)
		return cliExitRuntime
	}

	app, _ := newServerApplication(opts, store)
	fmt.Fprintf(stdout, "[serve] config: %s\n", configPath)
	fmt.Fprintf(stdout, "[serve] listening on: %s\n", listenAddress(opts))
	if err := runServerApp(app, opts, stdout); err != nil {
		fmt.Fprintf(stderr, "[serve] server exited with an error: %v\n", err)
		return cliExitRuntime
	}
	fmt.Fprintln(stdout, "[serve] stopped")
	return cliExitOK
}

// openServerStore 加载配置存储：默认路径复用桌面规则，--config-dir 使用独立目录。
// 返回的 path 用于启动日志（config.json 完整路径）。
func openServerStore(configDir string) (*configstore.Store, string, error) {
	if strings.TrimSpace(configDir) == "" {
		path, err := configstore.DefaultConfigPath()
		if err != nil {
			return nil, "", err
		}
		store, err := configstore.New()
		if err != nil {
			return nil, path, err
		}
		return store, path, nil
	}
	path, err := configstore.ConfigPathInDir(configDir)
	if err != nil {
		return nil, "", err
	}
	dir := filepath.Dir(path)
	if err := ensurePrivateDir(dir); err != nil {
		return nil, path, fmt.Errorf("create config directory %s: %w", dir, err)
	}
	return configstore.NewAtPath(path), path, nil
}

// ensurePrivateDir 创建或收紧服务端配置目录权限。
// 配置中包含明文 frp token，默认只允许运行 frpdesk 的用户访问。
func ensurePrivateDir(dir string) error {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	return os.Chmod(dir, 0o700)
}

// preflightListen 尝试绑定后立即释放，用于尽早发现端口占用/地址非法。
// 真正的监听仍由 Wails server 完成；极小概率的抢占窗口由 Wails 启动错误兜底。
func preflightListen(host string, port int) error {
	ln, err := net.Listen("tcp", net.JoinHostPort(host, strconv.Itoa(port)))
	if err != nil {
		return err
	}
	return ln.Close()
}

// listenAddress 返回便于阅读的监听地址（IPv6 自动加方括号）。
func listenAddress(opts serveOptions) string {
	return net.JoinHostPort(opts.Host, strconv.Itoa(opts.Port))
}

// accessURL 返回管理界面访问地址：通配监听地址显示为回环地址。
func accessURL(opts serveOptions) string {
	host := opts.Host
	switch host {
	case "0.0.0.0":
		host = "127.0.0.1"
	case "::":
		host = "::1"
	}
	return "http://" + net.JoinHostPort(host, strconv.Itoa(opts.Port))
}

// newServerApplication 创建无头 Wails App：同一 AppService/前端资源，无窗口/托盘/更新器。
// newServerApplication 返回 App 与其服务实例（后者供测试断言事件出口已绑定）。
func newServerApplication(opts serveOptions, store *configstore.Store) (*application.App, *AppService) {
	svc := newAppService(store, appModeServer)
	app := application.New(application.Options{
		Name:        "Frpdesk",
		Description: i18n.T(i18n.KeyAppDescription),
		Services: []application.Service{
			application.NewService(svc),
		},
		Assets: application.AssetOptions{
			Handler: serverAssets(),
		},
		Server: application.ServerOptions{
			Host: opts.Host,
			Port: opts.Port,
		},
		Logger: slog.Default(),
	})
	// 无头模式没有 attachUI，但仍需事件出口：状态/日志/代理/快照经
	// app.Event.Emit 广播到浏览器 WebSocket，前端才能实时更新。
	svc.wailsApp = app
	return app, svc
}

// serverAssets 在静态资源前注入服务端标记（首帧前生效），其余路径交给 Wails 资源服务。
// 前端据此隐藏退出/登录自启/桌面更新入口，并取消 macOS 标题栏红绿灯留白。
func serverAssets() http.Handler {
	base := application.AssetFileServerFS(assets)
	raw, err := fs.ReadFile(assets, "frontend/dist/index.html")
	if err != nil {
		return base
	}
	page := injectServerMode(raw)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/" || r.URL.Path == "/index.html" {
			w.Header().Set("Content-Type", "text/html; charset=utf-8")
			w.Header().Set("Content-Length", strconv.Itoa(len(page)))
			if r.Method == http.MethodHead {
				return
			}
			_, _ = w.Write(page)
			return
		}
		base.ServeHTTP(w, r)
	})
}

// serverModeMarker 在浏览器首帧前写入服务端模式与“非 macOS 桌面”标记。
const serverModeMarker = `<script>window.__FRPDESK_SERVER__=true;` +
	`document.documentElement.dataset.runtimeMode="server";` +
	`document.documentElement.dataset.noMac="";</script>`

// injectServerMode 把标记插入 <head> 之后；找不到 head 时前置（保持可服务性）。
func injectServerMode(html []byte) []byte {
	marker := []byte(serverModeMarker)
	if bytes.Contains(html, []byte("<head>")) {
		return bytes.Replace(html, []byte("<head>"), append([]byte("<head>"), marker...), 1)
	}
	return append(marker, html...)
}

// runServerApp 启动 Wails server，等待健康检查通过后打印访问地址，并阻塞到退出。
func runServerApp(app *application.App, opts serveOptions, stdout io.Writer) error {
	errCh := make(chan error, 1)
	go func() { errCh <- app.Run() }()

	url := accessURL(opts)
	if err := waitHealthy(errCh, url, 10*time.Second); err != nil {
		return err
	}
	fmt.Fprintf(stdout, "[serve] admin UI: %s\n", url)
	fmt.Fprintf(stdout, "[serve] press Ctrl+C or send SIGTERM to stop\n")
	return <-errCh
}

// waitHealthy 轮询 /health 直到 200 或 App 提前退出/超时。
// 本地健康检查绝不走 HTTP_PROXY：服务端可能绑定内网地址，而代理环境
// 通常只对 localhost/回环地址自动绕过代理。
func waitHealthy(errCh <-chan error, baseURL string, timeout time.Duration) error {
	client := &http.Client{
		Timeout: 500 * time.Millisecond,
		Transport: &http.Transport{
			Proxy: nil,
		},
	}
	deadline := time.Now().Add(timeout)
	for {
		select {
		case err := <-errCh:
			// 健康检查完成前收到优雅退出是合法关闭，不能误报为启动失败。
			return err
		default:
		}
		resp, err := client.Get(baseURL + "/health")
		if err == nil {
			resp.Body.Close()
			if resp.StatusCode == http.StatusOK {
				return nil
			}
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("timed out waiting for server readiness (%s)", timeout)
		}
		time.Sleep(50 * time.Millisecond)
	}
}
