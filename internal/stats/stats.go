// Package stats HTTP 请求级流量统计：聚合经内置 loopback 反代（tunnel 包）的请求，
// 经 Wails 绑定 Stats() 暴露给前端。
//
// 统计生命周期语义：App 进程生命周期内存态——不随隧道启停清零、不持久化、
// 应用重启归零；since 为收集器创建时间。TCP 服务不经代理，本期无统计。
package stats

import (
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode/utf8"
)

const (
	bucketSeconds    = 60   // 全局分钟桶宽度
	bucketCount      = 1440 // 环形桶数量（24h），跨天回绕覆盖旧桶
	recentCap        = 100  // recent 环形缓冲条数（新的在前）
	maxDistinctPaths = 2000 // 单服务 distinct path 上限，超出后不再新增条目（总数照计）
	maxClients       = 1000 // 去重 UA 客户端上限，封顶后不再新增种类（计数冻结，防恶意 UA 撑爆内存）
	topPathsLimit    = 10   // 快照返回的 topPaths 条数
	pathLimit        = 200  // path 保留长度上限（字节）：recent 展示与聚合 key 通用，防异常超长路径撑大内存
)

// 设备类别 slug（中文标签由前端映射，后端只出分类标识）。
const (
	deviceDesktop = "desktop"
	deviceMobile  = "mobile"
	deviceTablet  = "tablet"
	deviceBot     = "bot"
	deviceOther   = "other"
)

// RecordInput 单次请求上报输入（由 tunnel 代理层在 handler 收集，经回调桥接进来）。
type RecordInput struct {
	ServiceID  string // 服务 ID（服务名经 SetServiceNames 另行映射，统计侧不感知配置）
	ServerID   string // 请求发生时所属服务器（按请求捕获，服务迁移后在途请求仍报旧值）
	ServerName string // 请求发生时所属服务器名（与 ServerID 同刻捕获，改名不回填历史）
	Method     string
	Path       string // 完整 path+query：recent 保留原文（超长截断），topPaths 聚合剥离 query
	UA         string // 请求 User-Agent：clients 去重 + devices 分类（空串→other，不计 clients）
	Status     int
	DurationMs int64 // handler 进入到响应写完
	BytesIn    int64 // 请求 body 实际读取字节
	BytesOut   int64 // 响应 body 实际写出字节（不含状态行/头）
}

// Totals 全局聚合。
type Totals struct {
	Requests   int64 `json:"requests"`
	TrafficIn  int64 `json:"trafficIn"`
	TrafficOut int64 `json:"trafficOut"`
	Errors     int64 `json:"errors"`
	Clients    int64 `json:"clients"` // 去重 UA 客户端数（封顶 1000，空 UA 不计）
}

// DeviceStat 设备类别分布单条。
type DeviceStat struct {
	Type  string `json:"type"` // desktop|mobile|tablet|bot|other（前端映射中文标签）
	Count int64  `json:"count"`
}

// PathStat 单条 top path。
type PathStat struct {
	Path   string `json:"path"`
	Count  int64  `json:"count"`
	Errors int64  `json:"errors"`
}

// ServiceStats 单服务聚合。
type ServiceStats struct {
	ServiceID   string           `json:"serviceID"`
	ServerID    string           `json:"serverId"`   // 当前归属服务器（展示用；历史计数不按此分组）
	ServerName  string           `json:"serverName"` // 归属服务器名（当前映射解析；缺失空串）
	Name        string           `json:"name"`
	Requests    int64            `json:"requests"`
	TrafficIn   int64            `json:"trafficIn"`
	TrafficOut  int64            `json:"trafficOut"`
	Errors      int64            `json:"errors"`
	StatusCodes map[string]int64 `json:"statusCodes"`
	TopPaths    []PathStat       `json:"topPaths"`
}

// Bucket 全局分钟桶。
type Bucket struct {
	T        string `json:"t"` // 桶起点 RFC3339
	Requests int64  `json:"requests"`
	In       int64  `json:"in"`
	Out      int64  `json:"out"`
}

// Timeline 分钟桶时间线（只含非空桶，时间升序）。
type Timeline struct {
	BucketSeconds int      `json:"bucketSeconds"`
	Buckets       []Bucket `json:"buckets"`
}

// RecentRequest 最近请求单条。
type RecentRequest struct {
	T          string `json:"t"`
	ServiceID  string `json:"serviceID"`
	ServerID   string `json:"serverId"`   // 请求发生时所属服务器（捕获于上报时刻）
	ServerName string `json:"serverName"` // 请求发生时所属服务器名（同刻捕获）
	Method     string `json:"method"`
	Path       string `json:"path"` // 完整 path+query（超长截断）
	Status     int    `json:"status"`
	DurationMs int64  `json:"durationMs"`
	BytesIn    int64  `json:"bytesIn"`
	BytesOut   int64  `json:"bytesOut"`
}

// Snapshot Stats() 的完整快照（JSON 契约与前端冻结，全部 camelCase）。
type Snapshot struct {
	Since    string          `json:"since"`
	Total    Totals          `json:"total"`
	Devices  []DeviceStat    `json:"devices"`
	Services []ServiceStats  `json:"services"`
	Timeline Timeline        `json:"timeline"`
	Recent   []RecentRequest `json:"recent"`
}

// pathAgg 单条 distinct path 的聚合。
type pathAgg struct {
	count  int64
	errors int64
}

// serviceAgg 单服务累计聚合。
type serviceAgg struct {
	requests    int64
	trafficIn   int64
	trafficOut  int64
	errors      int64
	statusCodes map[string]int64
	paths       map[string]*pathAgg
}

// minuteBucket 全局分钟桶（start 零值 = 空桶）。
type minuteBucket struct {
	start    time.Time
	requests int64
	in, out  int64
}

// Collector 内存态统计收集器。并发安全：多请求并发 Record 与 Snapshot 并发读
// 共用一把锁——Record 只做 O(1) 累加（排序/截取都在 Snapshot 侧），
// 统计频率远低于请求转发本身，锁不是瓶颈。
type Collector struct {
	mu             sync.Mutex
	now            func() time.Time // 时间源（测试桩替控制分钟桶回绕）
	since          time.Time        // 收集器创建时间（快照的 since）
	names          map[string]string
	serviceServers map[string]string // serviceID → 当前归属 serverID（展示解析用）
	serverNames    map[string]string // serverID → 服务器名（展示解析用）
	total          Totals
	clients        map[string]struct{} // 去重 UA（非空）；封顶后不再新增
	devices        map[string]int64    // 设备类别 → 请求次数（含空 UA→other）
	services       map[string]*serviceAgg
	buckets        [bucketCount]minuteBucket
	recent         [recentCap]RecentRequest
	recentHead     int // 最新一条的下标（环形写指针）
	recentLen      int // 有效条数（未写满前 < recentCap）
}

// NewCollector 创建收集器，since 即创建时刻。
func NewCollector() *Collector {
	return &Collector{
		now:            time.Now,
		since:          time.Now(),
		names:          map[string]string{},
		serviceServers: map[string]string{},
		serverNames:    map[string]string{},
		clients:        map[string]struct{}{},
		devices:        map[string]int64{},
		services:       map[string]*serviceAgg{},
	}
}

// Record 记一次请求。每个进入代理 handler 的 HTTP 请求上报一次（含预检、含失败、
// 含 101 升级），口径由调用方（tunnel 代理层）保证。
func (c *Collector) Record(in RecordInput) {
	now := c.now()
	isError := in.Status >= 500
	aggPath := splitPath(in.Path)

	c.mu.Lock()
	defer c.mu.Unlock()

	c.total.Requests++
	c.total.TrafficIn += in.BytesIn
	c.total.TrafficOut += in.BytesOut
	if isError {
		c.total.Errors++
	}

	// clients：非空 UA 去重；封顶后不再新增种类（计数冻结在上限，不逐出已有条目）。
	// 空 UA 不计入（无客户端可识别），但照常进 devices 的 other。
	if in.UA != "" {
		if _, ok := c.clients[in.UA]; !ok && len(c.clients) < maxClients {
			c.clients[in.UA] = struct{}{}
		}
	}
	c.total.Clients = int64(len(c.clients))
	c.devices[classifyUA(in.UA)]++

	agg := c.services[in.ServiceID]
	if agg == nil {
		agg = &serviceAgg{statusCodes: map[string]int64{}, paths: map[string]*pathAgg{}}
		c.services[in.ServiceID] = agg
	}
	agg.requests++
	agg.trafficIn += in.BytesIn
	agg.trafficOut += in.BytesOut
	if isError {
		agg.errors++
	}
	agg.statusCodes[strconv.Itoa(in.Status)]++

	// topPaths：已存在的 path 照常累加；distinct 上限打满后不再新增条目，总数不丢
	if pa, ok := agg.paths[aggPath]; ok {
		pa.count++
		if isError {
			pa.errors++
		}
	} else if len(agg.paths) < maxDistinctPaths {
		pa := &pathAgg{count: 1}
		if isError {
			pa.errors = 1
		}
		agg.paths[aggPath] = pa
	}

	// 分钟桶：按 epoch 分桶 + 环形取模；桶起点变化即覆盖（跨天回绕同路径）
	idx := int((now.Unix() / bucketSeconds) % bucketCount)
	b := &c.buckets[idx]
	start := now.Truncate(bucketSeconds * time.Second)
	if !b.start.Equal(start) {
		*b = minuteBucket{start: start}
	}
	b.requests++
	b.in += in.BytesIn
	b.out += in.BytesOut

	// recent：环形 100 条，新的在前；展示用完整 path+query，超长截断
	c.recentHead = (c.recentHead + 1) % recentCap
	c.recent[c.recentHead] = RecentRequest{
		T:          now.Format(time.RFC3339),
		ServiceID:  in.ServiceID,
		ServerID:   in.ServerID,
		ServerName: in.ServerName,
		Method:     in.Method,
		Path:       truncate(in.Path, pathLimit),
		Status:     in.Status,
		DurationMs: in.DurationMs,
		BytesIn:    in.BytesIn,
		BytesOut:   in.BytesOut,
	}
	if c.recentLen < recentCap {
		c.recentLen++
	}
}

// SetServiceNames 整体替换 serviceID → 服务名映射（快照的 name 展示；缺失记空串）。
// 由宿主在配置创建/变更后喂入，统计侧不读配置存储。
func (c *Collector) SetServiceNames(names map[string]string) {
	m := make(map[string]string, len(names))
	for k, v := range names {
		m[k] = v
	}
	c.mu.Lock()
	c.names = m
	c.mu.Unlock()
}

// SetServiceServers 整体替换 serviceID → 当前归属 serverID 映射（ServiceStats
// 的 serverId 展示）。RecentRequest 的 serverId 不来自此映射（按请求捕获）。
func (c *Collector) SetServiceServers(m map[string]string) {
	c.mu.Lock()
	c.serviceServers = cloneStrings(m)
	c.mu.Unlock()
}

// SetServerNames 整体替换 serverID → 服务器名映射（serverName 展示解析）。
func (c *Collector) SetServerNames(m map[string]string) {
	c.mu.Lock()
	c.serverNames = cloneStrings(m)
	c.mu.Unlock()
}

func cloneStrings(m map[string]string) map[string]string {
	out := make(map[string]string, len(m))
	for k, v := range m {
		out[k] = v
	}
	return out
}

// Snapshot 返回当前聚合快照：服务按 requests 降序、topPaths 按 count 降序取前 10、
// 分钟桶只含非空桶升序、recent 新的在前。所有数据深拷贝，与内部状态脱钩。
func (c *Collector) Snapshot() Snapshot {
	c.mu.Lock()
	defer c.mu.Unlock()

	services := make([]ServiceStats, 0, len(c.services))
	for id, agg := range c.services {
		svID := c.serviceServers[id]
		st := ServiceStats{
			ServiceID:   id,
			ServerID:    svID,
			ServerName:  c.serverNames[svID],
			Name:        c.names[id],
			Requests:    agg.requests,
			TrafficIn:   agg.trafficIn,
			TrafficOut:  agg.trafficOut,
			Errors:      agg.errors,
			StatusCodes: make(map[string]int64, len(agg.statusCodes)),
			TopPaths:    topPaths(agg.paths),
		}
		for code, n := range agg.statusCodes {
			st.StatusCodes[code] = n
		}
		services = append(services, st)
	}
	// requests 降序；同量按 serviceID 稳定排序，避免 map 遍历顺序导致前端列表抖动
	sort.Slice(services, func(i, j int) bool {
		if services[i].Requests != services[j].Requests {
			return services[i].Requests > services[j].Requests
		}
		return services[i].ServiceID < services[j].ServiceID
	})

	// 时间线：只收非空桶，按桶起点升序输出
	buckets := make([]Bucket, 0, len(c.buckets))
	for _, b := range c.buckets {
		if b.requests == 0 {
			continue
		}
		buckets = append(buckets, Bucket{
			T:        b.start.Format(time.RFC3339),
			Requests: b.requests,
			In:       b.in,
			Out:      b.out,
		})
	}
	sort.Slice(buckets, func(i, j int) bool { return buckets[i].T < buckets[j].T })

	// recent：从写指针倒序回放（新的在前）；正数取模避免负下标
	recent := make([]RecentRequest, 0, c.recentLen)
	for i := 0; i < c.recentLen; i++ {
		idx := ((c.recentHead-i)%recentCap + recentCap) % recentCap
		recent = append(recent, c.recent[idx])
	}

	// devices：count 降序；同量按 type 稳定排序；无请求时返回空数组（非 null）
	devices := make([]DeviceStat, 0, len(c.devices))
	for typ, n := range c.devices {
		devices = append(devices, DeviceStat{Type: typ, Count: n})
	}
	sort.Slice(devices, func(i, j int) bool {
		if devices[i].Count != devices[j].Count {
			return devices[i].Count > devices[j].Count
		}
		return devices[i].Type < devices[j].Type
	})

	return Snapshot{
		Since:    c.since.Format(time.RFC3339),
		Total:    c.total,
		Devices:  devices,
		Services: services,
		Timeline: Timeline{BucketSeconds: bucketSeconds, Buckets: buckets},
		Recent:   recent,
	}
}

// classifyUA 按 User-Agent 子串判定设备类别：小写化后按序短路匹配，首个命中即返回。
// 顺序语义：先 bot（爬虫无设备形态可言）；tablet 先于 mobile——
// Android 无 "Mobile" 后缀的多为平板，iPad UA 里反而没有 iphone 字样；
// desktop 的标记串更具体（windows nt / macintosh 等），放最后兜底判定。
// 空 UA（如 curl 以外的裸客户端）无法归类，归 other。
func classifyUA(ua string) string {
	u := strings.ToLower(ua)
	switch {
	case containsAny(u, "bot", "spider", "crawl"):
		return deviceBot
	case strings.Contains(u, "ipad"), strings.Contains(u, "android") && !strings.Contains(u, "mobile"):
		return deviceTablet
	case containsAny(u, "iphone", "ipod", "android", "mobile", "harmonyos"):
		return deviceMobile
	case containsAny(u, "windows nt", "macintosh", "mac os x", "x11", "linux x86"):
		return deviceDesktop
	default:
		return deviceOther
	}
}

// containsAny 任一子串命中即真。
func containsAny(s string, subs ...string) bool {
	for _, sub := range subs {
		if strings.Contains(s, sub) {
			return true
		}
	}
	return false
}

// topPaths 按 count 降序取前 10（内部存 2000）；同量按 path 稳定排序。
func topPaths(paths map[string]*pathAgg) []PathStat {
	out := make([]PathStat, 0, len(paths))
	for p, agg := range paths {
		out = append(out, PathStat{Path: p, Count: agg.count, Errors: agg.errors})
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Count != out[j].Count {
			return out[i].Count > out[j].Count
		}
		return out[i].Path < out[j].Path
	})
	if len(out) > topPathsLimit {
		out = out[:topPathsLimit]
	}
	return out
}

// splitPath 取不含 query 的 path（topPaths 聚合口径），与 recent 同长度上限截断
// （超长 path 截断后视为同一条目，防异常路径撑大聚合表）。
func splitPath(full string) string {
	if i := strings.IndexByte(full, '?'); i >= 0 {
		full = full[:i]
	}
	return truncate(full, pathLimit)
}

// truncate 按字节截断到上限；URL path 基本 ASCII，仅回退被截断的多字节 rune 尾部，
// 避免产出非法 UTF-8。
func truncate(s string, limit int) string {
	if len(s) <= limit {
		return s
	}
	cut := s[:limit]
	for len(cut) > 0 {
		if r, size := utf8.DecodeLastRuneInString(cut); r != utf8.RuneError || size != 1 {
			break // 末尾是完整合法 rune：前缀字节完整，无需再回退
		}
		cut = cut[:len(cut)-1] // 末尾 rune 被截断（起始字节或续字节）：整段丢弃
	}
	return cut
}
