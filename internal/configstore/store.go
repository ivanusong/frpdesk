// Package configstore 负责 frpdesk 的配置模型、持久化与校验。
// 多 Frps 模型：config.servers 为服务器列表（每项含连接与公网入口元数据），
// config.services 逐隧道归属服务器（serverId）；旧版单全局配置在 load 时
// 幂等迁移为稳定 ID 的「默认服务器」，新版 fresh 安装不产生任何默认目标。
package configstore

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"time"

	"frpdesk/internal/i18n"
)

// legacyDefaultServerID 存量单全局配置迁移生成的默认服务器稳定 ID：
// 固定值保证迁移幂等（重复迁移不产生新 ID），且不依赖随机数。
const legacyDefaultServerID = "default"

// 语言偏好持久化字段（Config.Language）的合法取值。
const (
	LanguageSystem = i18n.PrefSystem
	LanguageZH     = i18n.PrefZH
	LanguageEN     = i18n.PrefEN
)

// 服务类型。
const (
	TypeHTTP = "http"
	TypeTCP  = "tcp"
	TypeUDP  = "udp"
)

// ServerConfig 单台 frps 服务器的连接配置与 HTTP 入口元数据。
// 与前端冻结契约：JSON 全部小驼峰。
type ServerConfig struct {
	ID         string `json:"id"`
	Name       string `json:"name"`
	ServerAddr string `json:"serverAddr"`
	ServerPort int    `json:"serverPort"`
	AuthToken  string `json:"authToken,omitempty"`
	// HTTPEnabled 该服务器是否提供 HTTP 隧道入口（frps 侧 vhostHTTP(S)Port）；
	// false 时该服务器只允许 TCP 隧道，但保留 HTTP 元数据（重新开启不丢配置）
	HTTPEnabled bool   `json:"httpEnabled"`
	BaseDomain  string `json:"baseDomain,omitempty"`
	Scheme      string `json:"scheme,omitempty"`
	PublicPort  int    `json:"publicPort,omitempty"`
}

// ServerInput 前端提交的服务器输入（新增/编辑共用；除 id 外与 ServerConfig 同构）。
type ServerInput struct {
	Name        string `json:"name"`
	ServerAddr  string `json:"serverAddr"`
	ServerPort  int    `json:"serverPort"`
	AuthToken   string `json:"authToken"`
	HTTPEnabled bool   `json:"httpEnabled"`
	BaseDomain  string `json:"baseDomain"`
	Scheme      string `json:"scheme"`
	PublicPort  int    `json:"publicPort"`
}

// Service 单个穿透隧道（归属一台服务器）。
type Service struct {
	ID         string `json:"id"`
	ServerID   string `json:"serverId"` // 所属服务器（归属在创建/编辑时确定）
	Name       string `json:"name"`
	LocalHost  string `json:"localHost"`
	LocalPort  int    `json:"localPort"`
	Subdomain  string `json:"subdomain"` // 仅 http 有意义（tcp 时保留但不使用）
	Enabled    bool   `json:"enabled"`
	Type       string `json:"type"`           // http（默认）| tcp | udp
	RemotePort int    `json:"remotePort"`     // 仅 tcp/udp：frps 侧监听端口
	Icon       string `json:"icon,omitempty"` // emoji 图标（≤8 rune；空=未设置，展示按类型兜底）
}

// ServiceInput 前端提交的隧道输入（新增/编辑共用）。
type ServiceInput struct {
	ServerID   string `json:"serverId"`
	Name       string `json:"name"`
	LocalHost  string `json:"localHost"`
	LocalPort  int    `json:"localPort"`
	Subdomain  string `json:"subdomain"`
	Enabled    bool   `json:"enabled"`
	Type       string `json:"type"`       // http（缺省）| tcp | udp
	RemotePort int    `json:"remotePort"` // 仅 tcp/udp：frps 侧监听端口，必填
	Icon       string `json:"icon"`       // emoji 图标（可选，≤8 rune；空=未设置）
}

// TunnelState 隧道状态机（单台服务器实例与全局聚合共用同一组取值）。
type TunnelState string

const (
	StateStopped  TunnelState = "stopped"
	StateStarting TunnelState = "starting" // 正在登录 frps（login/proxy 注册中）
	StateStopping TunnelState = "stopping" // 停止中（cancel 已发，等待 frp 收尾）
	StateRunning  TunnelState = "running"  // 已连接（代理注册完成）
	StateFailed   TunnelState = "failed"
)

// TunnelStatus 隧道状态：全局聚合视图（多服务器时为派生值，见 AggregateStatus）。
type TunnelStatus struct {
	State   TunnelState `json:"state"`
	Message string      `json:"message,omitempty"`
	// MessageEN 该状态消息的英文渲染（终端诊断日志通道用，不随界面语言；
	// JSON 不输出）。UI 展示仍用 Message（双语），两者在创建点成对生成。
	MessageEN string `json:"-"`
	// MessageKey 「状态说明类」消息的 i18n key（如 msg.connected）：语言切换后可按
	// 当前语言重渲染；错误详情不设 key，保留发生时的原文（含 FRP 原始错误）。
	MessageKey string `json:"messageKey,omitempty"`
	// StartedAt 最早一台 running 服务器的连接时刻（聚合展示「已启动时长」用）
	StartedAt string `json:"startedAt,omitempty"`
	// 多服务器聚合计数（与前端冻结契约）：
	// TotalCount = 有 enabled 隧道的服务器数；ActiveCount = starting/running/stopping 实例数；
	// RunningCount = 已连接数；FailedCount = failed 服务器数
	RunningCount int `json:"runningCount"`
	TotalCount   int `json:"totalCount"`
	ActiveCount  int `json:"activeCount"`
	FailedCount  int `json:"failedCount"`
}

// ServerStatus 单台服务器的连接状态（含未运行的 stopped 项，前端逐台渲染）。
type ServerStatus struct {
	ServerID string      `json:"serverId"`
	State    TunnelState `json:"state"`
	Message  string      `json:"message,omitempty"`
	// MessageEN 同 TunnelStatus.MessageEN：英文渲染（日志通道，JSON 不输出）
	MessageEN string `json:"-"`
	// MessageKey 同 TunnelStatus：可重渲染的状态说明 key（错误详情为空）
	MessageKey string `json:"messageKey,omitempty"`
	StartedAt  string `json:"startedAt,omitempty"`
}

// LogMessage 返回该状态用于终端诊断日志的英文消息（固定英文，不随界面语言）：
// 优先 MessageEN（错误详情等无 key 场景在创建点算好的英文），
// 其次按 MessageKey 取英文词典文案；两者皆空返回空串。UI 展示仍用 Message（双语）。
func (s TunnelStatus) LogMessage() string {
	return statusLogMessage(s.MessageEN, s.MessageKey)
}

// LogMessage 同 TunnelStatus.LogMessage：失败原因等服务器级状态的日志英文渲染。
func (s ServerStatus) LogMessage() string {
	return statusLogMessage(s.MessageEN, s.MessageKey)
}

// statusLogMessage 日志英文消息渲染共用实现（MessageEN 优先，MessageKey 回退）。
func statusLogMessage(messageEN, messageKey string) string {
	if messageEN != "" {
		return messageEN
	}
	if messageKey != "" {
		return i18n.TLEn(messageKey)
	}
	return ""
}

// LogEntry 单条日志。能准确归属实例的条目带 ServerID/ServerName；
// frp 全局日志流无法归属实例，不标（宁缺勿错）。
type LogEntry struct {
	ID         uint64 `json:"id"`
	Level      string `json:"level"` // "info" | "error"
	Message    string `json:"message"`
	Timestamp  string `json:"timestamp"`
	ServerID   string `json:"serverId,omitempty"`
	ServerName string `json:"serverName,omitempty"`
}

// Snapshot 前端全量快照。
type Snapshot struct {
	Config         Config         `json:"config"`
	Status         TunnelStatus   `json:"status"`
	ServerStatuses []ServerStatus `json:"serverStatuses"`
	// Language 后端解析后的生效语言（"zh-CN"/"en"）：system 偏好由桌面端（后端）
	// 统一探测，前端以该值为准——避免 WebView 与原生各自判定造成英文 UI + 中文托盘。
	Language string `json:"language,omitempty"`
}

// Config 完整配置。servers 是连接目标的唯一有效来源；
// 旧版全局配置仅存在于迁移路径（load 时一次性转为默认服务器）。
type Config struct {
	Servers  []ServerConfig `json:"servers"`
	Services []Service      `json:"services"`
	// Language 界面语言偏好："system"（默认，缺省/空串同义）| "zh-CN" | "en"。
	// 旧配置无此字段（空串 = system），加载与保存均兼容，不影响服务/服务器语义。
	Language string `json:"language,omitempty"`
}

// IsHTTP 服务是否走 HTTP 通道：显式 http 或旧数据缺省类型（空串视为 http）。
// 仅此两类接入内置本地代理；tcp/udp 始终直连。
func (s Service) IsHTTP() bool {
	return s.Type == TypeHTTP || s.Type == ""
}

// ServerByID 按 ID 查服务器配置（副本值），不存在返回 false。
func (c Config) ServerByID(id string) (ServerConfig, bool) {
	for _, s := range c.Servers {
		if s.ID == id {
			return s, true
		}
	}
	return ServerConfig{}, false
}

// EnabledCount 统计某服务器下 enabled 隧道数（「最后一个 enabled 隧道撤销即停止」判定用）。
func (c Config) EnabledCount(serverID string) int {
	n := 0
	for _, svc := range c.Services {
		if svc.ServerID == serverID && svc.Enabled {
			n++
		}
	}
	return n
}

// AggregateStatus 由各服务器状态派生全局聚合状态（纯函数，单测覆盖）。
// 消息按当前语言渲染（状态栏/托盘 UI 用）；规则（与前端冻结）：totalCount 为有
// enabled 隧道的服务器数（调用方按配置计算传入）；
// 存在 starting → starting（登录中优先展示）；存在 stopping → stopping；
// active 全为 running（无 starting/stopping，允许夹杂 failed）→ running（部分失败不阻塞）；
// 无 active 但有 failed → failed；否则 stopped。
// startedAt 取最早一台 running 的连接时刻。
func AggregateStatus(statuses []ServerStatus, totalCount int) TunnelStatus {
	return aggregateStatus(statuses, totalCount, i18n.T, func(s ServerStatus) string { return s.Message })
}

// AggregateStatusEN 聚合状态的英文渲染（终端诊断日志通道用，不随界面语言；
// Message 字段填英文，UI 不得使用）。失败原因取各状态的 LogMessage()
// （MessageEN/英文词典，严格英文通道；FRP 原始错误类状态本就英文）。
func AggregateStatusEN(statuses []ServerStatus, totalCount int) TunnelStatus {
	return aggregateStatus(statuses, totalCount, i18n.TLEn, func(s ServerStatus) string { return s.LogMessage() })
}

// aggregateStatus 聚合实现：tr 选渲染语言，reasonOf 选失败原因来源
// （本地化 Message 或英文 MessageEN），两通道共用同一套状态归并规则。
func aggregateStatus(statuses []ServerStatus, totalCount int, tr func(string, ...any) string, reasonOf func(ServerStatus) string) TunnelStatus {
	agg := TunnelStatus{TotalCount: totalCount}
	var started []time.Time
	for _, s := range statuses {
		switch s.State {
		case StateRunning:
			agg.RunningCount++
			agg.ActiveCount++
			if t, err := time.Parse(time.RFC3339, s.StartedAt); err == nil {
				started = append(started, t)
			}
		case StateStarting, StateStopping:
			agg.ActiveCount++
		case StateFailed:
			agg.FailedCount++
		}
	}
	switch {
	case agg.ActiveCount == 0 && agg.FailedCount == 0:
		agg.State = StateStopped
	case agg.ActiveCount == 0:
		agg.State = StateFailed
	default:
		// 有 active：按占比细分；失败夹杂在 running 中不改变「已连接」主态
		starting, stopping := 0, 0
		for _, s := range statuses {
			switch s.State {
			case StateStarting:
				starting++
			case StateStopping:
				stopping++
			}
		}
		switch {
		case stopping > 0:
			agg.State = StateStopping
		case starting > 0:
			agg.State = StateStarting
		default:
			agg.State = StateRunning
			if len(started) > 0 {
				earliest := started[0]
				for _, t := range started[1:] {
					if t.Before(earliest) {
						earliest = t
					}
				}
				agg.StartedAt = earliest.Format(time.RFC3339)
			}
		}
	}
	// 失败摘要：不遮蔽主态，附一条可读消息（托盘/状态栏提示用，随语言变化）
	if agg.FailedCount > 0 {
		msg := tr("msg.serversFailed", agg.FailedCount)
		for _, s := range statuses {
			if s.State == StateFailed && reasonOf(s) != "" {
				msg = tr("msg.serversFailedReason", agg.FailedCount, BriefMessage(reasonOf(s), 80))
				break
			}
		}
		agg.Message = msg
	}
	return agg
}

// Store 配置存储，JSON 持久化到 os.UserConfigDir()/frpdesk/config.json。
type Store struct {
	mu   sync.Mutex
	path string
	cfg  Config
}

// 配置目录名：新路径用 frpdesk；legacyConfigDirName 仅用于旧版配置的一次性兼容迁移。
const (
	configDirName       = "frpdesk"
	legacyConfigDirName = "frpcc"
	configFileName      = "config.json"
)

// userConfigPath 返回新配置文件路径：os.UserConfigDir()/frpdesk/config.json。
func userConfigPath() (string, error) {
	return appConfigPath(configDirName)
}

// appConfigPath 拼接指定配置目录下的 config.json 路径。
func appConfigPath(dirName string) (string, error) {
	dir, err := os.UserConfigDir()
	if err != nil {
		return "", i18n.LErrWrap("err.userConfigDir", err)
	}
	return filepath.Join(dir, dirName, configFileName), nil
}

// New 创建存储并加载配置（含旧版单全局配置的幂等迁移）。
// 兼容旧配置目录（legacyConfigDirName）：仅当新配置不存在且旧配置存在时原样复制到新路径，
// 旧文件不改不删；复制失败直接返回错误，绝不落一份空配置冒充成功。
func New() (*Store, error) {
	path, err := userConfigPath()
	if err != nil {
		return nil, err
	}
	legacyPath, err := appConfigPath(legacyConfigDirName)
	if err != nil {
		return nil, err
	}
	if _, err := copyLegacyConfigIfNeeded(path, legacyPath); err != nil {
		return nil, err
	}
	return newStore(path), nil
}

// copyLegacyConfigIfNeeded 旧版配置目录兼容：newPath 不存在且 legacyPath 存在时，
// 把旧文件逐字节复制到 newPath。新配置永远优先（已存在直接返回）；复制用 O_EXCL，
// 不覆盖且重复启动幂等；legacyPath 只读，不修改、不删除。返回是否发生了复制。
func copyLegacyConfigIfNeeded(newPath, legacyPath string) (bool, error) {
	if _, err := os.Stat(newPath); err == nil {
		return false, nil
	} else if !os.IsNotExist(err) {
		return false, i18n.LErrWrap("err.statConfig", err)
	}
	if _, err := os.Stat(legacyPath); os.IsNotExist(err) {
		return false, nil
	} else if err != nil {
		return false, i18n.LErrWrap("err.statLegacy", err)
	}

	raw, err := os.ReadFile(legacyPath)
	if err != nil {
		return false, i18n.LErrWrap("err.readLegacy", err)
	}
	if err := os.MkdirAll(filepath.Dir(newPath), 0o755); err != nil {
		return false, i18n.LErrWrap("err.configDir", err)
	}

	// O_EXCL：并发/重复启动时绝不覆盖已生成的新配置
	f, err := os.OpenFile(newPath, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o644)
	if err != nil {
		if os.IsExist(err) {
			return false, nil // 竞争下已有新配置：按「新配置优先」继续
		}
		return false, i18n.LErrWrap("err.createLegacyCopy", err)
	}
	if _, err := f.Write(raw); err != nil {
		f.Close()
		_ = os.Remove(newPath) // 不留下写了一半的配置
		return false, i18n.LErrWrap("err.writeConfig", err)
	}
	if err := f.Close(); err != nil {
		_ = os.Remove(newPath)
		return false, i18n.LErrWrap("err.writeConfig", err)
	}
	return true, nil
}

// NewAtPath 在指定路径创建存储（测试/工具注入用，绝不触碰用户真实配置）。
func NewAtPath(path string) *Store {
	return newStore(path)
}

// newStore 从指定路径创建存储（测试注入用）。
func newStore(path string) *Store {
	s := &Store{path: path}
	s.load()
	return s
}

// load 从磁盘读取配置：识别新格式（含 servers 键）与旧格式（单全局配置）。
// 迁移只发生在「文件存在但无 servers 键」的旧格式上；新格式 servers:[] 是
// 用户显式清空，绝不重新迁移（fresh 安装也不偷偷产生默认服务器）。
func (s *Store) load() {
	s.cfg = Config{Servers: []ServerConfig{}, Services: []Service{}}
	raw, err := os.ReadFile(s.path)
	if err == nil {
		// 先应用已持久化的语言偏好（迁移生成的默认服务器名等在加载期就
		// 需要正确语言）；仅当文件确实带 language 键时才设置，测试造的
		// 旧格式/空配置不触发系统探测。
		var langPeek struct {
			Language string `json:"language"`
		}
		if json.Unmarshal(raw, &langPeek) == nil && langPeek.Language != "" {
			i18n.SetPreference(langPeek.Language)
		}
		// 再看顶层有哪些键：区分「旧格式」与「新格式空 servers」
		var keys map[string]json.RawMessage
		if json.Unmarshal(raw, &keys) == nil {
			if _, hasServers := keys["servers"]; hasServers {
				s.loadNewFormat(raw)
			} else if _, hasGlobal := keys["global"]; hasGlobal {
				s.cfg = migrateLegacyConfig(raw)
			}
		}
	}
	// 无论首次加载还是读取失败都回写一次，保证（迁移结果/默认值）落盘
	_ = s.save()
}

// loadNewFormat 解析新格式配置；服务 serverId 悬空（指向不存在的服务器）保留原样，
// 由启动校验报错提示，不静默改写用户数据。
func (s *Store) loadNewFormat(raw []byte) {
	var loaded Config
	if err := json.Unmarshal(raw, &loaded); err != nil {
		return
	}
	for i := range loaded.Services {
		if loaded.Services[i].Type == "" {
			loaded.Services[i].Type = TypeHTTP // 旧数据缺 type 补 http
		}
	}
	if loaded.Servers == nil {
		loaded.Servers = []ServerConfig{}
	}
	if loaded.Services == nil {
		loaded.Services = []Service{}
	}
	s.cfg = loaded
}

// migrateLegacyConfig 把旧版单全局配置迁移为多服务器模型：
// 生成稳定 ID 的「默认服务器」（httpEnabled=true），全部旧隧道归属它；
// 旧 global/scheme/publicPort 的兼容挖掘沿用（服务级旧值上收）；空地址/根域名保持为空，
// 不再补入维护者的默认基础设施。
func migrateLegacyConfig(raw []byte) Config {
	cfg := Config{Servers: []ServerConfig{}, Services: []Service{}}

	// 挖旧全局（含服务级 scheme/publicPort 上收兼容：直接反序列化会丢服务级旧值）
	var legacy struct {
		Global   GlobalConfig `json:"global"`
		Services []struct {
			Service
			Scheme     string `json:"scheme"`
			PublicPort int    `json:"publicPort"`
		} `json:"services"`
	}
	if err := json.Unmarshal(raw, &legacy); err != nil {
		return cfg
	}
	g := legacy.Global
	for _, ls := range legacy.Services {
		if ls.Type == TypeTCP {
			continue
		}
		if g.Scheme == "" && ls.Scheme != "" {
			g.Scheme = ls.Scheme
		}
		if g.PublicPort == 0 && ls.PublicPort > 0 {
			g.PublicPort = ls.PublicPort
		}
		if g.Scheme != "" && g.PublicPort != 0 {
			break
		}
	}
	g = normalizeLegacyGlobal(g)

	server := ServerConfig{
		ID:          legacyDefaultServerID,
		Name:        i18n.T("legacy.defaultServerName"),
		ServerAddr:  g.ServerAddr,
		ServerPort:  g.ServerPort,
		AuthToken:   g.AuthToken,
		HTTPEnabled: true,
		BaseDomain:  g.BaseDomain,
		Scheme:      g.Scheme,
		PublicPort:  g.PublicPort,
	}
	cfg.Servers = append(cfg.Servers, server)
	for _, ls := range legacy.Services {
		svc := ls.Service
		if svc.Type == "" {
			svc.Type = TypeHTTP
		}
		svc.ServerID = legacyDefaultServerID
		cfg.Services = append(cfg.Services, svc)
	}
	return cfg
}

// GlobalConfig 旧版单全局配置（legacy 桥）：仅用于迁移默认服务器与
// tunnel 连接参数构造，不再是存储的有效来源。
type GlobalConfig struct {
	ServerAddr string `json:"serverAddr"`
	ServerPort int    `json:"serverPort"`
	AuthToken  string `json:"authToken"`
	BaseDomain string `json:"baseDomain"`
	Scheme     string `json:"scheme"`
	PublicPort int    `json:"publicPort"`
}

// normalizeLegacyGlobal 旧全局字段归一（迁移路径用）。缺少服务器地址/根域名时保留为空，
// 不注入项目维护者的服务端；ServerPort 仅回退到 FRP 标准端口。
func normalizeLegacyGlobal(g GlobalConfig) GlobalConfig {
	if g.ServerPort == 0 {
		g.ServerPort = newServerDefaultPort
	}
	g.Scheme = normalizeScheme(g.Scheme)
	if g.PublicPort < 0 || g.PublicPort > 65535 {
		g.PublicPort = 0
	}
	return g
}

// newServerDefaultPort 新建/编辑及旧全局迁移时的 FRP 标准端口。
const newServerDefaultPort = 7000

// normalizeScheme 协议归一：空/非法一律 https。
func normalizeScheme(scheme string) string {
	scheme = strings.ToLower(trimSpace(scheme))
	if scheme != "http" && scheme != "https" {
		return "https"
	}
	return scheme
}

func (s *Store) save() error {
	if err := os.MkdirAll(filepath.Dir(s.path), 0o755); err != nil {
		return i18n.LErrWrap("err.configDir", err)
	}
	raw, err := json.MarshalIndent(s.cfg, "", "  ")
	if err != nil {
		return i18n.LErrWrap("err.marshalConfig", err)
	}
	return os.WriteFile(s.path, append(raw, '\n'), 0o644)
}

// Config 返回配置副本。
func (s *Store) Config() Config {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.cloneConfig()
}

// Language 返回持久化的语言偏好（"system"/"zh-CN"/"en"；旧配置无此字段返 system）。
func (s *Store) Language() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.cfg.Language == "" {
		return i18n.PrefSystem
	}
	return i18n.NormalizePref(s.cfg.Language)
}

// SetLanguage 持久化语言偏好（system/zh-CN/en）。仅写配置，不推进隧道状态；
// 调用方（AppService.SetLanguage）负责应用 i18n 当前语言并刷新托盘。
func (s *Store) SetLanguage(pref string) error {
	pref = i18n.NormalizePref(pref)
	s.mu.Lock()
	defer s.mu.Unlock()
	// system 为缺省语义：持久化时显式存值，保证「跟随系统」也是用户明确选择
	s.cfg.Language = pref
	if err := s.save(); err != nil {
		return err
	}
	return nil
}

func (s *Store) cloneConfig() Config {
	cfg := s.cfg
	cfg.Servers = append([]ServerConfig(nil), s.cfg.Servers...)
	cfg.Services = append([]Service(nil), s.cfg.Services...)
	return cfg
}

// normalizeServerHost 服务器地址规范化（用于重复判定）：trim + 小写；不做 DNS 解析。
func normalizeServerHost(addr string) string {
	return strings.ToLower(trimSpace(addr))
}

// serverEndpointKey 规范化 host:port 端点键（同键 = 同一连接目标，禁止重复配置）。
func serverEndpointKey(addr string, port int) string {
	return normalizeServerHost(addr) + ":" + itoa(port)
}

func itoa(n int) string { return fmt.Sprintf("%d", n) }

// buildServer 校验并补全服务器字段（Create/Update 共用）。
// 规则：名称必填 trim 且唯一；地址必填；端口非法默认 7000；同一 host+port 只允许一份；
// httpEnabled=true 时根域名必填、scheme 默认 https、publicPort 0=协议默认端口；
// httpEnabled=false 保留旧配置中的 HTTP 元数据（编辑时 input 为空的字段回填旧值）。
func buildServer(input ServerInput, existing []ServerConfig, excludeID string) (ServerConfig, error) {
	name := trimSpace(input.Name)
	if name == "" {
		return ServerConfig{}, i18n.LErr("err.nameRequired")
	}
	for _, s := range existing {
		if s.ID != excludeID && s.Name == name {
			return ServerConfig{}, i18n.LErr("err.nameExists", name)
		}
	}

	addr := trimSpace(input.ServerAddr)
	if addr == "" {
		return ServerConfig{}, i18n.LErr("err.addrRequired")
	}
	// 端口：仅 0 视为未填（默认 7000）；负数/超界是明确输入错误，拒绝而非猜测
	port := input.ServerPort
	switch {
	case port == 0:
		port = newServerDefaultPort
	case port < 1 || port > 65535:
		return ServerConfig{}, i18n.LErr("err.portRange")
	}

	// 端点查重：规范化 host+port 相同即视为同一目标（不做 DNS 解析）
	key := serverEndpointKey(addr, port)
	for _, s := range existing {
		if s.ID != excludeID && serverEndpointKey(s.ServerAddr, s.ServerPort) == key {
			return ServerConfig{}, i18n.LErr("err.endpointExists", s.ServerAddr, s.ServerPort, s.Name)
		}
	}

	out := ServerConfig{
		ID:          newID(),
		Name:        name,
		ServerAddr:  addr,
		ServerPort:  port,
		AuthToken:   input.AuthToken,
		HTTPEnabled: input.HTTPEnabled,
	}

	// HTTP 入口元数据：开启时校验必填；关闭时保留旧值（重新开启不丢配置）
	if input.HTTPEnabled {
		out.BaseDomain = trimSpace(input.BaseDomain)
		if out.BaseDomain == "" {
			return ServerConfig{}, i18n.LErr("err.baseDomainRequired")
		}
		out.Scheme = normalizeScheme(input.Scheme)
		// 协议端口与控制端口同口径：仅 0 视为未填（协议默认端口）；负数/越界明确拒绝
		if input.PublicPort < 0 || input.PublicPort > 65535 {
			return ServerConfig{}, i18n.LErr("err.publicPortRange")
		}
		out.PublicPort = input.PublicPort
	} else if old, ok := findServer(existing, excludeID); ok {
		out.BaseDomain = old.BaseDomain
		out.Scheme = old.Scheme
		out.PublicPort = old.PublicPort
	}
	return out, nil
}

func findServer(existing []ServerConfig, id string) (ServerConfig, bool) {
	for _, s := range existing {
		if s.ID == id {
			return s, true
		}
	}
	return ServerConfig{}, false
}

// CreateServer 新增服务器（仅落盘；连接由 StartServer 显式触发）。
func (s *Store) CreateServer(input ServerInput) (Config, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	server, err := buildServer(input, s.cfg.Servers, "")
	if err != nil {
		return s.cloneConfig(), err
	}
	s.cfg.Servers = append(s.cfg.Servers, server)
	if err := s.save(); err != nil {
		return s.cloneConfig(), err
	}
	return s.cloneConfig(), nil
}

// UpdateServer 编辑服务器。httpEnabled true→false 时若有 HTTP 隧道引用
// （含 disabled）则阻止，错误包含引用数量。
func (s *Store) UpdateServer(id string, input ServerInput) (Config, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	old, ok := findServer(s.cfg.Servers, id)
	if !ok {
		return s.cloneConfig(), i18n.LErr("err.serverNotFound")
	}
	updated, err := buildServer(input, s.cfg.Servers, id)
	if err != nil {
		return s.cloneConfig(), err
	}
	updated.ID = id
	// 能力收回校验：仍有 HTTP 隧道引用（含停用）时禁止关闭 HTTP 支持
	if old.HTTPEnabled && !updated.HTTPEnabled {
		if n := s.httpRefCountLocked(id); n > 0 {
			return s.cloneConfig(), i18n.LErr("err.httpRefCount", n)
		}
	}
	for i := range s.cfg.Servers {
		if s.cfg.Servers[i].ID == id {
			s.cfg.Servers[i] = updated
			break
		}
	}
	if err := s.save(); err != nil {
		return s.cloneConfig(), err
	}
	return s.cloneConfig(), nil
}

// DeleteServer 删除服务器：任何引用（含 disabled 隧道）都阻止删除。
func (s *Store) DeleteServer(id string) (Config, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	if _, ok := findServer(s.cfg.Servers, id); !ok {
		return s.cloneConfig(), i18n.LErr("err.serverNotFound")
	}
	refs := 0
	for _, svc := range s.cfg.Services {
		if svc.ServerID == id {
			refs++
		}
	}
	if refs > 0 {
		return s.cloneConfig(), i18n.LErr("err.tunnelRefCount", refs)
	}
	kept := s.cfg.Servers[:0]
	for _, sv := range s.cfg.Servers {
		if sv.ID != id {
			kept = append(kept, sv)
		}
	}
	s.cfg.Servers = kept
	if err := s.save(); err != nil {
		return s.cloneConfig(), err
	}
	return s.cloneConfig(), nil
}

// httpRefCountLocked 引用某服务器的 HTTP 隧道数（含 disabled）。
func (s *Store) httpRefCountLocked(serverID string) int {
	n := 0
	for _, svc := range s.cfg.Services {
		if svc.ServerID == serverID && svc.Type == TypeHTTP {
			n++
		}
	}
	return n
}

// CreateService 新增隧道：归属校验（服务器存在、HTTP 能力）+ 同服务器内冲突查重。
func (s *Store) CreateService(input ServiceInput) (Config, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	svc, err := s.buildServiceLocked(input, "")
	if err != nil {
		return s.cloneConfig(), err
	}
	svc.ID = newID() // 新隧道生成全局唯一 ID（编辑时保留原 ID）
	s.cfg.Services = append(s.cfg.Services, svc)
	if err := s.save(); err != nil {
		return s.cloneConfig(), err
	}
	return s.cloneConfig(), nil
}

// UpdateService 编辑隧道：全部本地校验通过才保存（移动归属时按目标服务器查重，
// 排除自身；原样保留自己的 subdomain/端口不算重复，改成同服务器他人占用仍报错）。
func (s *Store) UpdateService(id string, input ServiceInput) (Config, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	idx := -1
	for i, svc := range s.cfg.Services {
		if svc.ID == id {
			idx = i
			break
		}
	}
	if idx < 0 {
		return s.cloneConfig(), i18n.LErr("err.serviceNotFound")
	}
	svc, err := s.buildServiceLocked(input, id)
	if err != nil {
		return s.cloneConfig(), err
	}
	svc.ID = s.cfg.Services[idx].ID
	s.cfg.Services[idx] = svc
	if err := s.save(); err != nil {
		return s.cloneConfig(), err
	}
	return s.cloneConfig(), nil
}

// buildServiceLocked 校验并构造服务（锁内调用）：
//   - 归属：serverId 必须指向存在的服务器；
//   - 能力：HTTP 隧道要求目标服务器 httpEnabled（含移动到不支持的服务器）；
//   - 冲突域：subdomain（http）与 RemotePort（tcp/udp，同协议内）都在同一 serverId 内查重，
//     跨服务器允许复用；excludeID 非空时排除被编辑服务自身。
func (s *Store) buildServiceLocked(input ServiceInput, excludeID string) (Service, error) {
	name := trimSpace(input.Name)
	if name == "" {
		return Service{}, i18n.LErr("err.svcNameRequired")
	}

	localHost := trimSpace(input.LocalHost)
	if localHost == "" {
		localHost = "127.0.0.1"
	}
	if input.LocalPort < 1 || input.LocalPort > 65535 {
		return Service{}, i18n.LErr("err.localPortRange")
	}

	server, ok := findServer(s.cfg.Servers, trimSpace(input.ServerID))
	if !ok {
		return Service{}, i18n.LErr("err.serverRequired")
	}

	icon := trimSpace(input.Icon)
	if len([]rune(icon)) > 8 {
		return Service{}, i18n.LErr("err.iconTooLong")
	}

	svcType := strings.ToLower(trimSpace(input.Type))
	if svcType == "" {
		svcType = TypeHTTP
	}
	if svcType != TypeHTTP && svcType != TypeTCP && svcType != TypeUDP {
		return Service{}, i18n.LErr("err.typeInvalid")
	}
	if svcType == TypeHTTP && !server.HTTPEnabled {
		return Service{}, i18n.LErr("err.httpNotSupported", server.Name)
	}

	out := Service{
		ServerID:  server.ID,
		Name:      name,
		LocalHost: localHost,
		LocalPort: input.LocalPort,
		Enabled:   input.Enabled,
		Type:      svcType,
		Icon:      icon,
	}

	switch svcType {
	case TypeTCP, TypeUDP:
		if input.RemotePort < 1 || input.RemotePort > 65535 {
			return Service{}, i18n.LErr("err.remotePortRange")
		}
		// RemotePort 冲突域 = 同服务器 + 同协议（TCP/UDP 端口空间独立，同数字允许；
		// 跨服务器允许复用）
		for _, svc := range s.cfg.Services {
			if svc.ID != excludeID && svc.ServerID == server.ID && svc.Type == svcType && svc.RemotePort == input.RemotePort {
				return Service{}, i18n.LErr("err.remotePortTaken", strings.ToUpper(svcType), input.RemotePort)
			}
		}
		out.RemotePort = input.RemotePort
		out.Subdomain = ""

	case TypeHTTP:
		sub := trimSpace(input.Subdomain)
		if sub == "" {
			sub = generateSubdomain(s.subdomainsOfLocked(server.ID))
		} else {
			if err := validateSubdomain(sub); err != nil {
				return Service{}, err
			}
			// subdomainOwnedByLocked 已排除被编辑服务自身：保留自己的前缀不算重复
			if s.subdomainOwnedByLocked(sub, server.ID, excludeID) {
				return Service{}, i18n.LErr("err.subdomainExists", sub)
			}
		}
		out.Subdomain = sub
	}
	return out, nil
}

// subdomainsOfLocked 收集某服务器下 http 服务的子域名前缀（自动生成时避让）。
func (s *Store) subdomainsOfLocked(serverID string) map[string]bool {
	set := map[string]bool{}
	for _, svc := range s.cfg.Services {
		if svc.ServerID == serverID && svc.Type == TypeHTTP {
			set[svc.Subdomain] = true
		}
	}
	return set
}

// subdomainOwnedByLocked 指定服务器上该前缀是否被「其他」服务占用
// （excludeID 为被编辑服务自身：保留自己的前缀不算重复）。
func (s *Store) subdomainOwnedByLocked(sub, serverID, excludeID string) bool {
	for _, svc := range s.cfg.Services {
		if svc.ServerID == serverID && svc.Type == TypeHTTP && svc.Subdomain == sub && svc.ID != excludeID {
			return true
		}
	}
	return false
}

// SetServiceEnabled 只切换 enabled 开关。
func (s *Store) SetServiceEnabled(id string, enabled bool) (Config, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	idx := -1
	for i, svc := range s.cfg.Services {
		if svc.ID == id {
			idx = i
			break
		}
	}
	if idx < 0 {
		return s.cloneConfig(), i18n.LErr("err.serviceNotFound")
	}
	s.cfg.Services[idx].Enabled = enabled
	if err := s.save(); err != nil {
		return s.cloneConfig(), err
	}
	return s.cloneConfig(), nil
}

// DeleteService 按 ID 删除隧道。
func (s *Store) DeleteService(id string) (Config, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	kept := s.cfg.Services[:0]
	for _, svc := range s.cfg.Services {
		if svc.ID != id {
			kept = append(kept, svc)
		}
	}
	s.cfg.Services = kept
	if err := s.save(); err != nil {
		return s.cloneConfig(), err
	}
	return s.cloneConfig(), nil
}

// ReorderServices 按给定 ID 顺序重排隧道（仅配置数组顺序变化）：服务字段一律不动，
// 不涉及启停/重连语义。ids 必须与当前服务集合严格一一对应——数量一致、无重复、
// 无未知 ID，任一不满足立即拒绝，绝不落盘，避免出现半更新状态。
func (s *Store) ReorderServices(ids []string) (Config, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	if len(ids) != len(s.cfg.Services) {
		return s.cloneConfig(), i18n.LErr("err.reorderCount", len(s.cfg.Services), len(ids))
	}
	pos := make(map[string]int, len(ids))
	for i, id := range ids {
		if _, dup := pos[id]; dup {
			return s.cloneConfig(), i18n.LErr("err.reorderDup", id)
		}
		pos[id] = i
	}
	reordered := make([]Service, len(s.cfg.Services))
	for _, svc := range s.cfg.Services {
		p, ok := pos[svc.ID]
		if !ok {
			return s.cloneConfig(), i18n.LErr("err.reorderUnknown", svc.ID)
		}
		reordered[p] = svc // 按目标位次落座：字段原样搬运，不改任何服务内容
	}
	s.cfg.Services = reordered
	if err := s.save(); err != nil {
		return s.cloneConfig(), err
	}
	return s.cloneConfig(), nil
}

var subdomainRe = regexp.MustCompile(`^[a-z0-9](?:[a-z0-9-]*[a-z0-9])?$`)

// validateSubdomain 校验子域名前缀（3-63 位小写字母/数字/短横线）。
func validateSubdomain(sub string) error {
	if len(sub) < 3 || len(sub) > 63 {
		return i18n.LErr("err.subdomainLen")
	}
	if !subdomainRe.MatchString(sub) {
		return i18n.LErr("err.subdomainChars")
	}
	return nil
}

// generateSubdomain 生成 8 字节 hex 子域名前缀，不与避让集合重复。
func generateSubdomain(existing map[string]bool) string {
	for {
		buf := make([]byte, 8)
		if _, err := rand.Read(buf); err != nil {
			// crypto/rand 失败几乎不可能发生，兜底用时间戳
			return fmt.Sprintf("%016x", time.Now().UnixNano())
		}
		candidate := hex.EncodeToString(buf)
		if !existing[candidate] {
			return candidate
		}
	}
}

// ValidateServerStart 单台服务器启动前校验，返回用户可读错误消息（当前语言）或空串。
// 地址必填；enabled 隧道数必须 ≥1（无隧道不空连）；
// enabled 的 HTTP 隧道要求目标服务器支持 HTTP（归属悬空在 Config 层已挡）。
func ValidateServerStart(server ServerConfig, svcs []Service) string {
	msg, _, _ := ValidateServerStartKeyed(server, svcs)
	return msg
}

// ValidateServerStartKeyed 校验并返回（当前语言消息， 稳定 key， key 参数）：
// 校验通过返回空 key。msg 供状态/错误等 UI（继续双语），key+args 供日志通道
// 经 i18n.TL(i18n.EN, ...) 固定英文重渲染（环形日志不随界面语言翻译）。
func ValidateServerStartKeyed(server ServerConfig, svcs []Service) (msg, key string, args []any) {
	if trimSpace(server.ServerAddr) == "" {
		return i18n.T("msg.noAddr"), "msg.noAddr", nil
	}
	enabled := 0
	for _, svc := range svcs {
		if svc.ServerID != server.ID || !svc.Enabled {
			continue
		}
		if svc.Type == TypeHTTP && !server.HTTPEnabled {
			return i18n.T("msg.httpDisabledSvc", server.Name, svc.Name), "msg.httpDisabledSvc", []any{server.Name, svc.Name}
		}
		enabled++
	}
	if enabled == 0 {
		return i18n.T("msg.noEnabled", server.Name), "msg.noEnabled", []any{server.Name}
	}
	return "", "", nil
}

// PublicURL 拼接 HTTP 隧道的公网访问地址：
// ${scheme}://${svc.Subdomain}.${server.BaseDomain}[:${server.PublicPort}]。
// 入口三要素全部取自隧道归属的服务器；scheme 空/非法兜底 https。
func PublicURL(server ServerConfig, svc Service) string {
	domain := trimSpace(server.BaseDomain)
	if domain == "" {
		return svc.Subdomain + "." + i18n.T("err.serverNoBaseDomain")
	}
	scheme := normalizeScheme(server.Scheme)
	port := ""
	if server.PublicPort > 0 {
		port = fmt.Sprintf(":%d", server.PublicPort)
	}
	return fmt.Sprintf("%s://%s.%s%s", scheme, svc.Subdomain, domain, port)
}
