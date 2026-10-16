package configstore

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"frpdesk/internal/i18n"
)

func newTestStore(t *testing.T) *Store {
	t.Helper()
	return newStore(filepath.Join(t.TempDir(), "config.json"))
}

func mustCreateServer(t *testing.T, s *Store, input ServerInput) ServerConfig {
	t.Helper()
	cfg, err := s.CreateServer(input)
	if err != nil {
		t.Fatalf("CreateServer(%+v) 失败: %v", input, err)
	}
	return cfg.Servers[len(cfg.Servers)-1]
}

func mustCreate(t *testing.T, s *Store, input ServiceInput) Service {
	t.Helper()
	cfg, err := s.CreateService(input)
	if err != nil {
		t.Fatalf("CreateService(%+v) 失败: %v", input, err)
	}
	return cfg.Services[len(cfg.Services)-1]
}

func testServerInput(name, addr string, port int, httpEnabled bool) ServerInput {
	return ServerInput{
		Name: name, ServerAddr: addr, ServerPort: port,
		AuthToken: "", HTTPEnabled: httpEnabled,
		BaseDomain: "example.com", Scheme: "https", PublicPort: 0,
	}
}

// ---------- 迁移 ----------

// TestMigrateLegacyGlobalToDefaultServer 存量单全局配置 → 稳定 ID 的默认服务器：
// 明确配置原样保留，全部旧隧道归属它，服务 ID 不变。
func TestMigrateLegacyGlobalToDefaultServer(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.json")
	legacy := `{
	  "global": {"serverAddr": "old.example.com", "serverPort": 9999, "authToken": "t", "baseDomain": "old.com", "scheme": "http", "publicPort": 8443},
	  "services": [
	    {"id": "svc-keep-1", "name": "web", "localHost": "127.0.0.1", "localPort": 8001, "subdomain": "web", "enabled": true, "type": "http"},
	    {"id": "svc-keep-2", "name": "ssh", "localHost": "127.0.0.1", "localPort": 22, "enabled": true, "type": "tcp", "remotePort": 36022}
	  ]
	}`
	if err := os.WriteFile(path, []byte(legacy), 0o644); err != nil {
		t.Fatal(err)
	}
	s := newStore(path)
	cfg := s.Config()

	if len(cfg.Servers) != 1 {
		t.Fatalf("应迁移出 1 台服务器: %+v", cfg.Servers)
	}
	sv := cfg.Servers[0]
	if sv.ID != legacyDefaultServerID || sv.Name != i18n.T("legacy.defaultServerName") {
		t.Fatalf("默认服务器 ID/名应为稳定值: %+v", sv)
	}
	if !sv.HTTPEnabled || sv.ServerAddr != "old.example.com" || sv.ServerPort != 9999 ||
		sv.AuthToken != "t" || sv.BaseDomain != "old.com" || sv.Scheme != "http" || sv.PublicPort != 8443 {
		t.Fatalf("原值应完整保留: %+v", sv)
	}
	if len(cfg.Services) != 2 {
		t.Fatalf("服务数应保留: %+v", cfg.Services)
	}
	for _, svc := range cfg.Services {
		if svc.ServerID != legacyDefaultServerID {
			t.Fatalf("旧隧道应归属默认服务器: %+v", svc)
		}
	}
	if cfg.Services[0].ID != "svc-keep-1" || cfg.Services[1].ID != "svc-keep-2" {
		t.Fatal("服务 ID 应保留")
	}
}

// TestMigrateLegacyGlobalKeepsMissingInfrastructureEmpty 检查缺少地址或根域名的旧配置时，
// 迁移保留隧道但不补内部地址，也不会自动指向任何服务端。
func TestMigrateLegacyGlobalKeepsMissingInfrastructureEmpty(t *testing.T) {
	for _, tc := range []struct {
		name string
		raw  string
	}{
		{
			name: "旧配置没有全局服务器信息",
			raw:  `{"global":{},"services":[{"id":"svc-empty","name":"web","localPort":8080,"enabled":true}]}`,
		},
		{
			name: "旧配置只有显式服务级公网入口",
			raw:  `{"global":{"scheme":"https","publicPort":8443},"services":[{"id":"svc-entry","name":"web","localPort":8080,"enabled":true,"scheme":"https","publicPort":9443}]}`,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cfg := migrateLegacyConfig([]byte(tc.raw))
			if len(cfg.Servers) != 1 {
				t.Fatalf("应保留旧服务归属的配置项: %+v", cfg.Servers)
			}
			s := cfg.Servers[0]
			if s.ServerAddr != "" {
				t.Fatalf("缺少明确地址时必须保持空，避免连接未知服务端: %q", s.ServerAddr)
			}
			if s.BaseDomain != "" {
				t.Fatalf("缺少明确根域名时必须保持空: %q", s.BaseDomain)
			}
			if s.ServerPort != newServerDefaultPort {
				t.Fatalf("未配置端口应使用 frp 标准端口 %d，got %d", newServerDefaultPort, s.ServerPort)
			}
		})
	}
}

// TestMigrationPersistsAndIdempotent 迁移必须真正落盘且幂等：
// 二次加载（新进程语义）不再迁移/不改写，磁盘上已是新格式。
func TestMigrationPersistsAndIdempotent(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.json")
	legacy := `{"global": {"serverAddr": "a.com", "serverPort": 9999, "baseDomain": "a.com"}, "services": []}`
	if err := os.WriteFile(path, []byte(legacy), 0o644); err != nil {
		t.Fatal(err)
	}
	first := newStore(path).Config()
	// 迁移结果落盘：文件应含 servers 键（新格式）
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var keys map[string]json.RawMessage
	if err := json.Unmarshal(raw, &keys); err != nil {
		t.Fatal(err)
	}
	if _, ok := keys["servers"]; !ok {
		t.Fatal("迁移结果必须落盘（文件应已是新格式）")
	}

	second := newStore(path).Config() // 二次加载
	if len(second.Servers) != 1 || second.Servers[0].ID != first.Servers[0].ID {
		t.Fatalf("二次加载应幂等: %+v vs %+v", second.Servers, first.Servers)
	}
}

// TestFreshInstallAndEmptyServersNoMigration fresh 安装（无文件）与新版空 servers
// 都不产生默认服务器（不偷偷写回个人基础设施默认目标）。
func TestFreshInstallAndEmptyServersNoMigration(t *testing.T) {
	fresh := newTestStore(t).Config()
	if len(fresh.Servers) != 0 || len(fresh.Services) != 0 {
		t.Fatalf("fresh 安装不应有服务器: %+v", fresh.Servers)
	}

	// 新版显式 servers:[]：绝不按旧 Global 迁移
	dir := t.TempDir()
	path := filepath.Join(dir, "config.json")
	empty := `{"servers": [], "services": []}`
	if err := os.WriteFile(path, []byte(empty), 0o644); err != nil {
		t.Fatal(err)
	}
	if got := newStore(path).Config(); len(got.Servers) != 0 {
		t.Fatalf("servers:[] 不得重新迁移: %+v", got.Servers)
	}
}

// ---------- 旧配置目录兼容 ----------

// TestCopyLegacyConfigCopiesAndIdempotent 首次启动：新配置不存在且旧配置存在时
// 原样复制（旧文件不删不改），复制结果可被正常加载；重复启动幂等不再复制。
func TestCopyLegacyConfigCopiesAndIdempotent(t *testing.T) {
	dir := t.TempDir()
	newPath := filepath.Join(dir, "frpdesk", "config.json")
	legacyPath := filepath.Join(dir, "frpcc", "config.json")
	legacyRaw := []byte(`{"global":{"serverAddr":"old.example.com","serverPort":9999,"baseDomain":"old.com"},"services":[]}`)
	if err := os.MkdirAll(filepath.Dir(legacyPath), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(legacyPath, legacyRaw, 0o644); err != nil {
		t.Fatal(err)
	}

	copied, err := copyLegacyConfigIfNeeded(newPath, legacyPath)
	if err != nil || !copied {
		t.Fatalf("首次应复制旧配置: copied=%v err=%v", copied, err)
	}
	got, err := os.ReadFile(newPath)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, legacyRaw) {
		t.Fatalf("复制应逐字节原样: %s", got)
	}
	// 旧文件保持原样（不改不删）
	if after, err := os.ReadFile(legacyPath); err != nil || !bytes.Equal(after, legacyRaw) {
		t.Fatalf("旧配置不得改动: err=%v content=%s", err, after)
	}
	// 复制结果可直接加载，并走既有旧格式迁移
	if cfg := newStore(newPath).Config(); len(cfg.Servers) != 1 || cfg.Servers[0].ServerAddr != "old.example.com" {
		t.Fatalf("复制后的配置应可正常加载: %+v", cfg.Servers)
	}

	// 重复启动（第二次调用）幂等：不再复制、两侧内容不变
	copied2, err := copyLegacyConfigIfNeeded(newPath, legacyPath)
	if err != nil || copied2 {
		t.Fatalf("二次启动不应再复制: copied=%v err=%v", copied2, err)
	}
}

// TestCopyLegacyConfigNewConfigWins 新配置已存在时永远优先：即使旧配置存在且内容不同，
// 也不复制、不覆盖新配置。
func TestCopyLegacyConfigNewConfigWins(t *testing.T) {
	dir := t.TempDir()
	newPath := filepath.Join(dir, "frpdesk", "config.json")
	legacyPath := filepath.Join(dir, "frpcc", "config.json")
	if err := os.MkdirAll(filepath.Dir(newPath), 0o755); err != nil {
		t.Fatal(err)
	}
	newRaw := []byte(`{"servers":[],"services":[]}`)
	if err := os.WriteFile(newPath, newRaw, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Dir(legacyPath), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(legacyPath, []byte(`{"global":{"serverAddr":"should-not-win.com"},"services":[]}`), 0o644); err != nil {
		t.Fatal(err)
	}

	copied, err := copyLegacyConfigIfNeeded(newPath, legacyPath)
	if err != nil || copied {
		t.Fatalf("新配置存在时不得复制: copied=%v err=%v", copied, err)
	}
	if got, err := os.ReadFile(newPath); err != nil || !bytes.Equal(got, newRaw) {
		t.Fatalf("新配置不得被覆盖: err=%v content=%s", err, got)
	}
}

// TestCopyLegacyConfigNoFiles 新旧配置都不存在：不复制，也不创建任何文件
// （后续由 load 正常落新品配置，迁移路径不越权）。
func TestCopyLegacyConfigNoFiles(t *testing.T) {
	dir := t.TempDir()
	newPath := filepath.Join(dir, "frpdesk", "config.json")
	legacyPath := filepath.Join(dir, "frpcc", "config.json")

	copied, err := copyLegacyConfigIfNeeded(newPath, legacyPath)
	if err != nil || copied {
		t.Fatalf("无旧配置不应复制: copied=%v err=%v", copied, err)
	}
	if _, err := os.Stat(newPath); !os.IsNotExist(err) {
		t.Fatalf("迁移不得预创建新配置: %v", err)
	}
}

// TestCopyLegacyConfigErrorReported 旧配置存在但不可读时上报错误，
// 且绝不创建一个空的新配置冒充成功。
func TestCopyLegacyConfigErrorReported(t *testing.T) {
	dir := t.TempDir()
	newPath := filepath.Join(dir, "frpdesk", "config.json")
	legacyPath := filepath.Join(dir, "frpcc", "config.json")
	// 用同名目录制造稳定的读取失败（不依赖权限位，root 下也成立）
	if err := os.MkdirAll(legacyPath, 0o755); err != nil {
		t.Fatal(err)
	}

	copied, err := copyLegacyConfigIfNeeded(newPath, legacyPath)
	if err == nil || copied {
		t.Fatalf("旧配置不可读必须报错: copied=%v err=%v", copied, err)
	}
	if _, statErr := os.Stat(newPath); !os.IsNotExist(statErr) {
		t.Fatalf("失败后不得写出空配置: %v", statErr)
	}
}

// ---------- 服务器 CRUD 与校验 ----------

func TestCreateServerValidation(t *testing.T) {
	s := newTestStore(t)

	// 名称必填
	if _, err := s.CreateServer(ServerInput{Name: "  ", ServerAddr: "a.com"}); err == nil || !strings.Contains(err.Error(), "名称") {
		t.Fatalf("名称必填: %v", err)
	}
	// 地址必填
	if _, err := s.CreateServer(ServerInput{Name: "a", ServerAddr: " "}); err == nil || !strings.Contains(err.Error(), "地址") {
		t.Fatalf("地址必填: %v", err)
	}
	// 名称唯一
	mustCreateServer(t, s, testServerInput("srv", "a.com", 7000, false))
	if _, err := s.CreateServer(testServerInput("srv", "b.com", 7000, false)); err == nil || !strings.Contains(err.Error(), "已存在") {
		t.Fatalf("名称应唯一: %v", err)
	}
	// 端口非法默认 7000
	sv := mustCreateServer(t, s, testServerInput("p0", "p0.com", 0, false))
	if sv.ServerPort != 7000 {
		t.Fatalf("端口非法应默认 7000: %+v", sv)
	}
	// httpEnabled=true 根域名必填
	if _, err := s.CreateServer(ServerInput{Name: "h", ServerAddr: "h.com", HTTPEnabled: true, BaseDomain: " "}); err == nil || !strings.Contains(err.Error(), "根域名") {
		t.Fatalf("HTTP 开启时根域名必填: %v", err)
	}
	// scheme 缺省 https；publicPort 与控制端口同口径（越界拒绝，0=协议默认）
	if _, err := s.CreateServer(ServerInput{Name: "h2", ServerAddr: "h2.com", HTTPEnabled: true, BaseDomain: "x.com", Scheme: "https", PublicPort: 99999}); err == nil || !strings.Contains(err.Error(), "0-65535") {
		t.Fatalf("协议端口越界应拒绝: %v", err)
	}
	if _, err := s.CreateServer(ServerInput{Name: "h2", ServerAddr: "h2.com", HTTPEnabled: true, BaseDomain: "x.com", Scheme: "ftp", PublicPort: -1}); err == nil {
		t.Fatal("协议端口负数应拒绝")
	}
	sv2 := mustCreateServer(t, s, ServerInput{Name: "h2", ServerAddr: "h2.com", HTTPEnabled: true, BaseDomain: "x.com", Scheme: "ftp", PublicPort: 0})
	if sv2.Scheme != "https" || sv2.PublicPort != 0 {
		t.Fatalf("scheme 缺省 https / 0=协议默认: %+v", sv2)
	}
}

// TestCreateServerEndpointConflict 禁止相同规范化 host+port（大小写不敏感；不做 DNS）。
func TestCreateServerEndpointConflict(t *testing.T) {
	s := newTestStore(t)
	mustCreateServer(t, s, testServerInput("a", "FRP.example.COM", 7000, false))
	// 大小写不同的同一 host+port：拒绝
	if _, err := s.CreateServer(testServerInput("b", "frp.example.com", 7000, false)); err == nil || !strings.Contains(err.Error(), "相同地址") {
		t.Fatalf("同端点应拒绝: %v", err)
	}
	// 同 host 不同端口：允许
	if _, err := s.CreateServer(testServerInput("c", "frp.example.com", 7100, false)); err != nil {
		t.Fatalf("不同端口应允许: %v", err)
	}
	// 编辑排除自身：原样保存应成功
	cfg := s.Config()
	self := cfg.Servers[0]
	_, err := s.UpdateServer(self.ID, ServerInput{
		Name: self.Name, ServerAddr: "frp.EXAMPLE.com", ServerPort: 7000, HTTPEnabled: false,
		BaseDomain: self.BaseDomain, Scheme: self.Scheme, PublicPort: self.PublicPort,
	})
	if err != nil {
		t.Fatalf("编辑排除自身应成功: %v", err)
	}
}

// TestUpdateServerDisableHTTPBlockedByRefCount 关闭 HTTP 支持被引用阻止，错误含数量。
func TestUpdateServerDisableHTTPBlockedByRefCount(t *testing.T) {
	s := newTestStore(t)
	sv := mustCreateServer(t, s, testServerInput("a", "a.com", 7000, true))
	mustCreate(t, s, ServiceInput{ServerID: sv.ID, Name: "w1", LocalPort: 8001, Subdomain: "web1", Enabled: true, Type: TypeHTTP})
	mustCreate(t, s, ServiceInput{ServerID: sv.ID, Name: "w2", LocalPort: 8002, Subdomain: "web2", Enabled: false, Type: TypeHTTP}) // disabled 也算

	_, err := s.UpdateServer(sv.ID, ServerInput{Name: "a", ServerAddr: "a.com", ServerPort: 7000, HTTPEnabled: false})
	if err == nil || !strings.Contains(err.Error(), "2") {
		t.Fatalf("关闭 HTTP 应被 2 条引用阻止（错误含数量）: %v", err)
	}
	// 删除掉引用后可关闭
	cfg := s.Config()
	for _, svc := range cfg.Services {
		if _, err := s.DeleteService(svc.ID); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := s.UpdateServer(sv.ID, ServerInput{Name: "a", ServerAddr: "a.com", ServerPort: 7000, HTTPEnabled: false}); err != nil {
		t.Fatalf("无引用后应可关闭: %v", err)
	}
	// 关闭保留原 HTTP 元数据
	sv2 := s.Config().Servers[0]
	if sv2.BaseDomain != "example.com" || sv2.Scheme != "https" {
		t.Fatalf("关闭 HTTP 应保留原元数据: %+v", sv2)
	}
}

// TestDeleteServerBlockedByReference 任何引用（含 disabled）阻止删除。
func TestDeleteServerBlockedByReference(t *testing.T) {
	s := newTestStore(t)
	sv := mustCreateServer(t, s, testServerInput("a", "a.com", 7000, false))
	mustCreate(t, s, ServiceInput{ServerID: sv.ID, Name: "ssh", LocalPort: 22, Enabled: false, Type: TypeTCP, RemotePort: 36022})

	if _, err := s.DeleteServer(sv.ID); err == nil || !strings.Contains(err.Error(), "1") {
		t.Fatalf("disabled 引用也应阻止删除: %v", err)
	}
	// 无引用可删
	empty := mustCreateServer(t, s, testServerInput("b", "b.com", 7000, false))
	if _, err := s.DeleteServer(empty.ID); err != nil {
		t.Fatalf("无引用应可删除: %v", err)
	}
	if cfg := s.Config(); len(cfg.Servers) != 1 {
		t.Fatalf("删除应生效: %+v", cfg.Servers)
	}
}

// ---------- 隧道 CRUD：归属/能力/冲突域 ----------

// TestServiceConflictsScopedPerServer subdomain 与 RemotePort 冲突域 = 同 serverId；
// 跨服务器复用允许；编辑排除自己且同服务器他人冲突不误放过。
func TestServiceConflictsScopedPerServer(t *testing.T) {
	s := newTestStore(t)
	sa := mustCreateServer(t, s, testServerInput("a", "a.com", 7000, true))
	sb := mustCreateServer(t, s, testServerInput("b", "b.com", 7000, true))

	mustCreate(t, s, ServiceInput{ServerID: sa.ID, Name: "w1", LocalPort: 8001, Subdomain: "dupsub", Enabled: true, Type: TypeHTTP})

	// 同服务器同 subdomain：拒绝
	if _, err := s.CreateService(ServiceInput{ServerID: sa.ID, Name: "w2", LocalPort: 8002, Subdomain: "dupsub", Enabled: true, Type: TypeHTTP}); err == nil {
		t.Fatal("同服务器 subdomain 冲突应拒绝")
	}
	// 跨服务器同 subdomain：允许
	if _, err := s.CreateService(ServiceInput{ServerID: sb.ID, Name: "w3", LocalPort: 8003, Subdomain: "dupsub", Enabled: true, Type: TypeHTTP}); err != nil {
		t.Fatalf("跨服务器 subdomain 复用应允许: %v", err)
	}

	// TCP RemotePort 同理
	mustCreate(t, s, ServiceInput{ServerID: sa.ID, Name: "ssh1", LocalPort: 22, Enabled: true, Type: TypeTCP, RemotePort: 36022})
	if _, err := s.CreateService(ServiceInput{ServerID: sa.ID, Name: "ssh2", LocalPort: 22, Enabled: true, Type: TypeTCP, RemotePort: 36022}); err == nil {
		t.Fatal("同服务器 RemotePort 冲突应拒绝")
	}
	if _, err := s.CreateService(ServiceInput{ServerID: sb.ID, Name: "ssh3", LocalPort: 22, Enabled: true, Type: TypeTCP, RemotePort: 36022}); err != nil {
		t.Fatalf("跨服务器 RemotePort 复用应允许: %v", err)
	}

	// 编辑排除自己：原样保留自己的 subdomain 应成功；改成同服务器他人的仍拒绝
	svc := s.Config().Services[0]
	if _, err := s.UpdateService(svc.ID, ServiceInput{ServerID: sa.ID, Name: "w1", LocalPort: 8001, Subdomain: "dupsub", Enabled: true, Type: TypeHTTP}); err != nil {
		t.Fatalf("保留自己 subdomain 应成功: %v", err)
	}
	// sa 上再建一个他人占用的前缀，改过去应拒绝
	w4 := mustCreate(t, s, ServiceInput{ServerID: sa.ID, Name: "w4", LocalPort: 8004, Subdomain: "other-sub", Enabled: true, Type: TypeHTTP})
	if _, err := s.UpdateService(svc.ID, ServiceInput{ServerID: sa.ID, Name: "w1", LocalPort: 8001, Subdomain: w4.Subdomain, Enabled: true, Type: TypeHTTP}); err == nil {
		t.Fatal("改成同服务器他人占用应拒绝")
	}
}

// TestUDPServiceTypeAndPortUniqueness UDP 服务：与 TCP 同规则（远程端口必填、
// 同服务器同协议冲突、跨服务器复用允许），且 TCP/UDP 端口空间独立——同服务器
// 同数字远程端口允许各一条；持久化往返保留 udp 类型；非法类型拒绝。
func TestUDPServiceTypeAndPortUniqueness(t *testing.T) {
	s := newTestStore(t)
	sa := mustCreateServer(t, s, testServerInput("a", "a.com", 7000, true))
	sb := mustCreateServer(t, s, testServerInput("b", "b.com", 7000, false))

	udp := mustCreate(t, s, ServiceInput{ServerID: sa.ID, Name: "dns", LocalPort: 53, Enabled: true, Type: TypeUDP, RemotePort: 36053})
	if udp.Type != TypeUDP || udp.Subdomain != "" || udp.RemotePort != 36053 {
		t.Fatalf("UDP 服务字段错误: %+v", udp)
	}

	// 同服务器同协议冲突：拒绝
	if _, err := s.CreateService(ServiceInput{ServerID: sa.ID, Name: "dns2", LocalPort: 53, Enabled: true, Type: TypeUDP, RemotePort: 36053}); err == nil {
		t.Fatal("同服务器同协议 RemotePort 冲突应拒绝")
	}
	// TCP/UDP 端口空间独立：同服务器同数字允许（TCP 占用同一数字不拒 UDP）
	if _, err := s.CreateService(ServiceInput{ServerID: sa.ID, Name: "tcp-same", LocalPort: 53, Enabled: true, Type: TypeTCP, RemotePort: 36053}); err != nil {
		t.Fatalf("同数字远程端口跨协议应允许: %v", err)
	}
	if _, err := s.CreateService(ServiceInput{ServerID: sa.ID, Name: "udp2", LocalPort: 54, Enabled: true, Type: TypeUDP, RemotePort: 36053}); err == nil {
		t.Fatal("同协议再冲突仍应拒绝")
	}
	// 跨服务器复用允许
	if _, err := s.CreateService(ServiceInput{ServerID: sb.ID, Name: "dns-b", LocalPort: 53, Enabled: true, Type: TypeUDP, RemotePort: 36053}); err != nil {
		t.Fatalf("跨服务器 UDP RemotePort 复用应允许: %v", err)
	}

	// 非法类型 / UDP 缺远程端口：拒绝
	if _, err := s.CreateService(ServiceInput{ServerID: sa.ID, Name: "bad", LocalPort: 1, Enabled: true, Type: "grpc"}); err == nil {
		t.Fatal("非法类型应拒绝")
	}
	if _, err := s.CreateService(ServiceInput{ServerID: sa.ID, Name: "noport", LocalPort: 53, Enabled: true, Type: TypeUDP}); err == nil {
		t.Fatal("UDP 缺远程端口应拒绝")
	}

	// 持久化往返：新 Store 读同路径，udp 类型保留（不因旧缺省补 http 而丢失）
	reloaded := newStore(s.path)
	types := map[string]string{}
	for _, svc := range reloaded.Config().Services {
		types[svc.Name] = svc.Type
	}
	if types["dns"] != TypeUDP || types["tcp-same"] != TypeTCP {
		t.Fatalf("持久化往返应保留协议类型: %v", types)
	}

	// 编辑互转：UDP 改 TCP 成功；再改回时 TCP 自身占用同端口应拒绝（同协议冲突）
	if _, err := s.UpdateService(udp.ID, ServiceInput{ServerID: sa.ID, Name: "dns", LocalPort: 53, Enabled: true, Type: TypeTCP, RemotePort: 36053}); err == nil {
		t.Fatal("UDP 改 TCP 撞同协议占用应拒绝")
	}
	if _, err := s.UpdateService(udp.ID, ServiceInput{ServerID: sa.ID, Name: "dns", LocalPort: 53, Enabled: true, Type: TypeTCP, RemotePort: 36054}); err != nil {
		t.Fatalf("UDP 改 TCP 合法端口应成功: %v", err)
	}
}

// TestMoveServiceValidatesTargetBeforeSave 移动归属：目标校验全部通过才保存；
// 目标服务器不支持 HTTP 或冲突时拒绝且不落盘。
func TestMoveServiceValidatesTargetBeforeSave(t *testing.T) {
	s := newTestStore(t)
	sa := mustCreateServer(t, s, testServerInput("a", "a.com", 7000, true))  // HTTP 支持
	sb := mustCreateServer(t, s, testServerInput("b", "b.com", 7000, false)) // 不支持
	svc := mustCreate(t, s, ServiceInput{ServerID: sa.ID, Name: "web", LocalPort: 8001, Subdomain: "web", Enabled: true, Type: TypeHTTP})

	// 移动到不支持 HTTP 的服务器：拒绝
	if _, err := s.UpdateService(svc.ID, ServiceInput{ServerID: sb.ID, Name: "web", LocalPort: 8001, Subdomain: "web", Enabled: true, Type: TypeHTTP}); err == nil || !strings.Contains(err.Error(), "HTTP") {
		t.Fatalf("移动到不支持 HTTP 的服务器应拒绝: %v", err)
	}
	// 归属未知服务器：拒绝
	if _, err := s.UpdateService(svc.ID, ServiceInput{ServerID: "ghost", Name: "web", LocalPort: 8001, Subdomain: "web", Enabled: true, Type: TypeHTTP}); err == nil {
		t.Fatal("归属未知服务器应拒绝")
	}
	// 数据未变（失败不落盘）
	if got := s.Config().Services[0].ServerID; got != sa.ID {
		t.Fatalf("失败编辑不应落盘: %s", got)
	}
	// 合法移动成功
	if _, err := s.UpdateService(svc.ID, ServiceInput{ServerID: sa.ID, Name: "web2", LocalPort: 8002, Subdomain: "web", Enabled: true, Type: TypeHTTP}); err != nil {
		t.Fatalf("同服务器合法编辑应成功: %v", err)
	}
}

// TestServiceUnknownServerRejected 新建服务必须归属已配置服务器。
func TestServiceUnknownServerRejected(t *testing.T) {
	s := newTestStore(t)
	if _, err := s.CreateService(ServiceInput{ServerID: "ghost", Name: "x", LocalPort: 1, Enabled: true}); err == nil {
		t.Fatal("归属未知服务器应拒绝")
	}
}

// TestReorderServices 隧道拖拽排序：合法 ids 重排并落盘（新 Store 读回仍按新序，
// 刷新/重启后保持）；缺 ID、多 ID、重复、未知 ID 一律拒绝且不落盘（无半更新）。
func TestReorderServices(t *testing.T) {
	s := newTestStore(t)
	sa := mustCreateServer(t, s, testServerInput("a", "a.com", 7000, true))
	s1 := mustCreate(t, s, ServiceInput{ServerID: sa.ID, Name: "w1", LocalPort: 8001, Subdomain: "web1", Enabled: true, Type: TypeHTTP})
	s2 := mustCreate(t, s, ServiceInput{ServerID: sa.ID, Name: "w2", LocalPort: 8002, Subdomain: "web2", Enabled: true, Type: TypeHTTP})
	s3 := mustCreate(t, s, ServiceInput{ServerID: sa.ID, Name: "w3", LocalPort: 8003, Subdomain: "web3", Enabled: false, Type: TypeHTTP})

	want := []string{s3.ID, s1.ID, s2.ID}
	cfg, err := s.ReorderServices(want)
	if err != nil {
		t.Fatalf("合法重排应成功: %v", err)
	}
	if got := idsOf(cfg.Services); strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("应按给定次序重排: %v", got)
	}
	// 服务字段原样搬运（顺序之外的任何内容不得变化）
	if cfg.Services[0].Name != "w3" || cfg.Services[0].Enabled || cfg.Services[0].Subdomain != "web3" {
		t.Fatalf("重排不得改动服务字段: %+v", cfg.Services[0])
	}

	// 持久化：新 Store 读同路径仍为新序
	reloaded := newStore(s.path)
	if got := idsOf(reloaded.Config().Services); strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("重排结果应落盘: %v", got)
	}

	before := idsOf(s.Config().Services)
	// 缺 ID
	if _, err := s.ReorderServices([]string{s1.ID, s2.ID}); err == nil {
		t.Fatal("缺 ID 应拒绝")
	}
	// 多 ID
	if _, err := s.ReorderServices([]string{s1.ID, s2.ID, s3.ID, s1.ID}); err == nil {
		t.Fatal("多 ID 应拒绝")
	}
	// 重复 ID
	if _, err := s.ReorderServices([]string{s1.ID, s1.ID, s3.ID}); err == nil {
		t.Fatal("重复 ID 应拒绝")
	}
	// 未知 ID
	if _, err := s.ReorderServices([]string{s1.ID, s2.ID, "ghost"}); err == nil {
		t.Fatal("未知 ID 应拒绝")
	}
	// 失败不得产生半更新：内存顺序保持失败前状态
	if got := idsOf(s.Config().Services); strings.Join(got, ",") != strings.Join(before, ",") {
		t.Fatalf("失败重排不得改动顺序: %v", got)
	}

	// 原样顺序（no-op）与空配置空列表：允许
	if _, err := s.ReorderServices(before); err != nil {
		t.Fatalf("原样重排应成功: %v", err)
	}
	if _, err := newTestStore(t).ReorderServices(nil); err != nil {
		t.Fatalf("空配置空排序应成功: %v", err)
	}
}

// idsOf 提取服务 ID 序列（顺序断言用）。
func idsOf(svcs []Service) []string {
	out := make([]string, 0, len(svcs))
	for _, svc := range svcs {
		out = append(out, svc.ID)
	}
	return out
}

// TestTCPClearsSubdomainOnEdit TCP 编辑不参与子域名逻辑（置空）。
func TestTCPClearsSubdomainOnEdit(t *testing.T) {
	s := newTestStore(t)
	sa := mustCreateServer(t, s, testServerInput("a", "a.com", 7000, true))
	svc := mustCreate(t, s, ServiceInput{ServerID: sa.ID, Name: "web", LocalPort: 8001, Subdomain: "web", Enabled: true, Type: TypeHTTP})
	got, err := s.UpdateService(svc.ID, ServiceInput{ServerID: sa.ID, Name: "web", LocalPort: 8001, Enabled: true, Type: TypeTCP, RemotePort: 36001})
	if err != nil {
		t.Fatal(err)
	}
	if got.Services[0].Type != TypeTCP || got.Services[0].Subdomain != "" {
		t.Fatalf("切 TCP 后 subdomain 应置空: %+v", got.Services[0])
	}
}

// TestServiceIcon 图标长度校验（按 rune）。
func TestServiceIcon(t *testing.T) {
	s := newTestStore(t)
	sa := mustCreateServer(t, s, testServerInput("a", "a.com", 7000, true))
	if _, err := s.CreateService(ServiceInput{ServerID: sa.ID, Name: "x", LocalPort: 1, Icon: "一二三四五六七八九", Enabled: true}); err == nil {
		t.Fatal("超 8 rune 图标应拒绝")
	}
	if _, err := s.CreateService(ServiceInput{ServerID: sa.ID, Name: "x", LocalPort: 1, Icon: "🌐🌐", Enabled: true}); err != nil {
		t.Fatalf("合法图标应通过: %v", err)
	}
}

// ---------- 启动校验 / PublicURL / 聚合 ----------

func TestValidateServerStart(t *testing.T) {
	sv := testServerInput("a", "a.com", 7000, true)
	server := ServerConfig{ID: "a", Name: "a", ServerAddr: "a.com", ServerPort: 7000, HTTPEnabled: true}
	if msg := ValidateServerStart(server, nil); msg == "" {
		t.Fatal("无隧道应报错")
	}
	serverNoHTTP := ServerConfig{ID: "a", Name: "a", ServerAddr: "a.com", ServerPort: 7000}
	if msg := ValidateServerStart(serverNoHTTP, []Service{
		{ServerID: "a", Enabled: true, Type: TypeHTTP},
	}); msg == "" || !strings.Contains(msg, "HTTP") {
		t.Fatalf("HTTP 隧道目标不支持 HTTP 应报错: %s", msg)
	}
	if msg := ValidateServerStart(server, []Service{
		{ServerID: "a", Enabled: true, Type: TypeHTTP, Subdomain: "w"},
		{ServerID: "a", Enabled: false, Type: TypeTCP},
	}); msg != "" {
		t.Fatalf("合法配置应通过: %s", msg)
	}
	_ = sv
}

func TestPublicURLPerServer(t *testing.T) {
	server := ServerConfig{ID: "a", BaseDomain: "a.com", Scheme: "https", PublicPort: 8443}
	svc := Service{Subdomain: "web"}
	if got := PublicURL(server, svc); got != "https://web.a.com:8443" {
		t.Fatalf("URL 应按归属服务器拼接: %s", got)
	}
	server2 := ServerConfig{ID: "b", BaseDomain: "", Scheme: "https"}
	if got := PublicURL(server2, svc); !strings.Contains(got, "未配置根域名") {
		t.Fatalf("未配置根域名应兜底提示: %s", got)
	}
}

// TestAggregateStatus 全局聚合状态派生（与前端冻结口径）。
func TestAggregateStatus(t *testing.T) {
	mk := func(id string, st TunnelState, startedAt string) ServerStatus {
		return ServerStatus{ServerID: id, State: st, StartedAt: startedAt}
	}

	// 全 stopped
	got := AggregateStatus([]ServerStatus{mk("a", StateStopped, ""), mk("b", StateStopped, "")}, 0)
	if got.State != StateStopped || got.TotalCount != 0 {
		t.Fatalf("全 stopped: %+v", got)
	}
	// 有 starting → starting
	got = AggregateStatus([]ServerStatus{mk("a", StateRunning, "2025-01-01T10:00:00+08:00"), mk("b", StateStarting, "")}, 2)
	if got.State != StateStarting || got.ActiveCount != 2 || got.RunningCount != 1 {
		t.Fatalf("有 starting 应 starting: %+v", got)
	}
	// 全 running → running + startedAt 取最早
	got = AggregateStatus([]ServerStatus{
		mk("a", StateRunning, "2025-01-01T10:00:00+08:00"),
		mk("b", StateRunning, "2025-01-01T09:00:00+08:00"),
	}, 2)
	if got.State != StateRunning || got.RunningCount != 2 || got.ActiveCount != 2 || got.FailedCount != 0 {
		t.Fatalf("全 running: %+v", got)
	}
	if got.StartedAt != "2025-01-01T09:00:00+08:00" {
		t.Fatalf("startedAt 应取最早: %+v", got)
	}
	// 部分失败不阻塞：running+failed → running（失败计数可见）
	got = AggregateStatus([]ServerStatus{mk("a", StateRunning, ""), mk("b", StateFailed, "")}, 2)
	if got.State != StateRunning || got.FailedCount != 1 || got.Message == "" {
		t.Fatalf("部分失败应保持 running 且消息可见: %+v", got)
	}
	// 无 active 有 failed → failed
	got = AggregateStatus([]ServerStatus{mk("a", StateFailed, "")}, 1)
	if got.State != StateFailed || got.FailedCount != 1 {
		t.Fatalf("无 active 有 failed 应 failed: %+v", got)
	}
	// stopping 优先展示
	got = AggregateStatus([]ServerStatus{mk("a", StateRunning, ""), mk("b", StateStopping, "")}, 2)
	if got.State != StateStopping {
		t.Fatalf("有 stopping 应 stopping: %+v", got)
	}
}

// TestCreateServerPortValidation 端口校验：仅 0 视为未填（默认 7000），
// 负数与超 65535 是明确输入错误，拒绝而非猜测。
func TestCreateServerPortValidation(t *testing.T) {
	s := newTestStore(t)
	if _, err := s.CreateServer(testServerInput("neg", "neg.com", -1, false)); err == nil || !strings.Contains(err.Error(), "1-65535") {
		t.Fatalf("负数端口应拒绝: %v", err)
	}
	if _, err := s.CreateServer(testServerInput("big", "big.com", 65536, false)); err == nil || !strings.Contains(err.Error(), "1-65535") {
		t.Fatalf("超界端口应拒绝: %v", err)
	}
	sv := mustCreateServer(t, s, testServerInput("zero", "zero.com", 0, false))
	if sv.ServerPort != 7000 {
		t.Fatalf("0 应默认 7000: %+v", sv)
	}
	sv2 := mustCreateServer(t, s, testServerInput("ok", "ok.com", 7100, false))
	if sv2.ServerPort != 7100 {
		t.Fatalf("合法端口应保留: %+v", sv2)
	}
	// 编辑同样拒绝越界
	if _, err := s.UpdateServer(sv2.ID, testServerInput("ok", "ok.com", -5, false)); err == nil {
		t.Fatal("编辑负数端口应拒绝")
	}
}

// TestStatusLogMessageEN 终端日志英文通道：LogMessage 优先 MessageEN（错误详情
// 在创建点算好的英文），其次按 MessageKey 取英文词典，二者皆空返回空串；
// AggregateStatusEN 的 Message 为英文（失败原因取 MessageEN），UI 版仍为双语。
func TestStatusLogMessageEN(t *testing.T) {
	defer i18n.Set(i18n.ZH) // 恢复包默认（其余用例按中文断言）
	i18n.SetPreference(i18n.PrefZH)

	ts := TunnelStatus{State: StateFailed, Message: "失败中文", MessageEN: "english failure"}
	if got := ts.LogMessage(); got != "english failure" {
		t.Fatalf("LogMessage 应优先 MessageEN: %q", got)
	}
	byKey := TunnelStatus{State: StateStopping, Message: "停止中", MessageKey: "msg.stopping"}
	if got := byKey.LogMessage(); got != "Stopping" {
		t.Fatalf("LogMessage 应按 key 取英文: %q", got)
	}
	if got := (TunnelStatus{}).LogMessage(); got != "" {
		t.Fatalf("无消息应返回空串: %q", got)
	}

	list := []ServerStatus{
		{ServerID: "a", State: StateFailed, Message: "服务失败中文原因", MessageEN: "proxy registration failed"},
	}
	en := AggregateStatusEN(list, 1)
	if en.State != StateFailed || !strings.Contains(en.Message, "1 server(s) failed") || !strings.Contains(en.Message, "proxy registration failed") {
		t.Fatalf("AggregateStatusEN 应为英文且含 MessageEN 原因: %q", en.Message)
	}
	zh := AggregateStatus(list, 1)
	if !strings.Contains(zh.Message, "1 台服务器失败") || !strings.Contains(zh.Message, "服务失败中文原因") {
		t.Fatalf("AggregateStatus 应保持当前语言: %q", zh.Message)
	}
}
