package stats

import (
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
	"unicode/utf8"
)

// fixedTime 用固定时间源替换收集器的 now，返回恢复函数。
func fixedTime(c *Collector, at time.Time) func() time.Time {
	old := c.now
	c.now = func() time.Time { return at }
	return old
}

// TestRecordAggregatesServices 基础聚合：total/服务维度/statusCodes/errors、
// 服务按 requests 降序、name 映射、空服务不出现在快照。
func TestRecordAggregatesServices(t *testing.T) {
	c := NewCollector()
	c.SetServiceNames(map[string]string{"s1": "my-api", "s2": "web"})

	c.Record(RecordInput{ServiceID: "s1", Method: "GET", Path: "/api/users", Status: 200, DurationMs: 3, BytesOut: 100})
	c.Record(RecordInput{ServiceID: "s1", Method: "POST", Path: "/api/users?page=2", Status: 500, DurationMs: 5, BytesIn: 10, BytesOut: 20})
	c.Record(RecordInput{ServiceID: "s2", Method: "GET", Path: "/", Status: 404, DurationMs: 1})

	snap := c.Snapshot()

	wantSince := c.since.Format(time.RFC3339)
	if snap.Since != wantSince {
		t.Fatalf("since 应为收集器创建时间 %s，得到 %s", wantSince, snap.Since)
	}
	if snap.Total.Requests != 3 || snap.Total.TrafficIn != 10 || snap.Total.TrafficOut != 120 || snap.Total.Errors != 1 {
		t.Fatalf("total 聚合错误: %+v", snap.Total)
	}
	if len(snap.Services) != 2 {
		t.Fatalf("应有 2 个服务，得到 %d", len(snap.Services))
	}
	// requests 降序：s1(2) 在前
	first, second := snap.Services[0], snap.Services[1]
	if first.ServiceID != "s1" || first.Name != "my-api" {
		t.Fatalf("第一个服务应为 s1/my-api，得到 %+v", first)
	}
	if first.Requests != 2 || first.TrafficIn != 10 || first.TrafficOut != 120 || first.Errors != 1 {
		t.Fatalf("s1 聚合错误: %+v", first)
	}
	if first.StatusCodes["200"] != 1 || first.StatusCodes["500"] != 1 {
		t.Fatalf("s1 statusCodes 错误: %+v", first.StatusCodes)
	}
	if second.ServiceID != "s2" || second.Name != "web" || second.StatusCodes["404"] != 1 {
		t.Fatalf("s2 聚合错误: %+v", second)
	}
	// 未映射服务名缺省空串
	c.Record(RecordInput{ServiceID: "unknown", Status: 200})
	for _, svc := range c.Snapshot().Services {
		if svc.ServiceID == "unknown" && svc.Name != "" {
			t.Fatalf("未映射服务名应为空串，得到 %q", svc.Name)
		}
	}
}

// TestRecordConcurrent 并发 Record + Snapshot（-race 下验证并发安全）：
// N 个 goroutine 各记 M 条，总请求数必须恰好 N*M，不丢不重。
func TestRecordConcurrent(t *testing.T) {
	c := NewCollector()
	const goroutines, perG = 32, 50

	var wg sync.WaitGroup
	for g := 0; g < goroutines; g++ {
		wg.Add(1)
		go func(g int) {
			defer wg.Done()
			for i := 0; i < perG; i++ {
				c.Record(RecordInput{ServiceID: "s", Method: "GET", Path: "/p", Status: 200})
				if i%10 == 0 {
					c.Snapshot() // 与 Record 并发读
				}
			}
		}(g)
	}
	wg.Wait()

	snap := c.Snapshot()
	if got := snap.Total.Requests; got != goroutines*perG {
		t.Fatalf("并发 Record 应精确计数 %d，得到 %d", goroutines*perG, got)
	}
	if len(snap.Services) != 1 || snap.Services[0].Requests != goroutines*perG {
		t.Fatalf("服务维度计数错误: %+v", snap.Services)
	}
}

// TestTimelineBucketsRolloverAndWrap 分钟桶：跨桶分裂、只含非空桶且升序、
// 24h 回绕覆盖旧桶（桶数组下标复用时按新起点重置）。
func TestTimelineBucketsRolloverAndWrap(t *testing.T) {
	c := NewCollector()
	base := time.Date(2025, 6, 1, 12, 0, 30, 0, time.Local) // 桶起点 12:00:00
	old := fixedTime(c, base)
	defer func() { c.now = old }()

	c.Record(RecordInput{ServiceID: "s", Status: 200, BytesIn: 1, BytesOut: 2})

	// +61s：进入下一个桶（必须先换时间源再记，否则同桶累加）
	c.now = func() time.Time { return base.Add(61 * time.Second) }
	c.Record(RecordInput{ServiceID: "s", Status: 200, BytesIn: 3, BytesOut: 4})

	snap := c.Snapshot()
	if len(snap.Timeline.Buckets) != 2 || snap.Timeline.BucketSeconds != 60 {
		t.Fatalf("跨桶应产出 2 个非空桶: %+v", snap.Timeline)
	}
	b0, b1 := snap.Timeline.Buckets[0], snap.Timeline.Buckets[1]
	if !strings.Contains(b0.T, "12:00:00") || b0.Requests != 1 || b0.In != 1 || b0.Out != 2 {
		t.Fatalf("第一个桶错误: %+v", b0)
	}
	if !strings.Contains(b1.T, "12:01:00") || b1.Requests != 1 || b1.In != 3 || b1.Out != 4 {
		t.Fatalf("第二个桶错误: %+v", b1)
	}

	// 跨天回绕：+25h 后再记，环形下标与第一个桶相同，但起点应是新桶（旧数据被覆盖）
	c.now = func() time.Time { return base.Add(25 * time.Hour) }
	c.Record(RecordInput{ServiceID: "s", Status: 200, BytesIn: 100, BytesOut: 200})

	snap = c.Snapshot()
	if len(snap.Timeline.Buckets) != 3 {
		t.Fatalf("回绕后应有 3 个非空桶: %+v", snap.Timeline)
	}
	last := snap.Timeline.Buckets[2]
	if !strings.Contains(last.T, "13:00:00") || last.Requests != 1 || last.In != 100 {
		t.Fatalf("回绕桶应为次日 13:00 新起点: %+v", last)
	}
	// 次日 13:00 桶内再记一次（13:00:45，同桶）：不重置，累加计数
	c.now = func() time.Time { return base.Add(25*time.Hour + 15*time.Second) }
	c.Record(RecordInput{ServiceID: "s", Status: 200})
	snap = c.Snapshot()
	last = snap.Timeline.Buckets[2]
	if last.Requests != 2 || last.In != 100 || last.Out != 200 {
		t.Fatalf("同桶内应累加: %+v", last)
	}
}

// TestTopPathsCapDistinctLimit topPaths：distinct path 上限 2000——打满后不再新增
// 条目，但已有条目照常累加、请求总数照计；快照只取 count 降序前 10。
func TestTopPathsCapDistinctLimit(t *testing.T) {
	c := NewCollector()
	// 打满上限：2000 条 distinct
	for i := 0; i < maxDistinctPaths; i++ {
		c.Record(RecordInput{ServiceID: "s", Status: 200, Path: "/p" + strconv.Itoa(i)})
	}
	// 再记 2001 次：1 条已存在（/p0，计数应累加）+ 2000 条超限（不新增条目）
	for i := 0; i <= maxDistinctPaths; i++ {
		c.Record(RecordInput{ServiceID: "s", Status: 200, Path: "/p" + strconv.Itoa(i%maxDistinctPaths)})
	}

	agg := c.services["s"]
	c.mu.Lock()
	distinct := len(agg.paths)
	hits0 := agg.paths["/p0"].count
	c.mu.Unlock()

	if distinct != maxDistinctPaths {
		t.Fatalf("distinct path 应封顶 %d，得到 %d", maxDistinctPaths, distinct)
	}
	if hits0 != 3 { // 初始 1 次 + 第二轮 i=0 与 i=2000 两次
		t.Fatalf("已存在条目应照常累加（count=3），得到 %d", hits0)
	}
	if got := c.Snapshot().Total.Requests; got != int64(2*maxDistinctPaths+1) {
		t.Fatalf("总数应照计 %d，得到 %d", 2*maxDistinctPaths+1, got)
	}

	// 快照 topPaths：降序前 10，且每个服务各自封顶
	c.Record(RecordInput{ServiceID: "s2", Status: 200, Path: "/a"})
	for _, svc := range c.Snapshot().Services {
		if len(svc.TopPaths) > topPathsLimit {
			t.Fatalf("topPaths 应取前 %d，得到 %d", topPathsLimit, len(svc.TopPaths))
		}
	}
	svcS := c.Snapshot().Services[1] // requests 降序：s(4001) 在前，s2(1) 在后
	for i := 1; i < len(svcS.TopPaths); i++ {
		if svcS.TopPaths[i-1].Count < svcS.TopPaths[i].Count {
			t.Fatalf("topPaths 应按 count 降序: %+v", svcS.TopPaths)
		}
	}
}

// TestRecentRingAndTruncation recent 环形缓冲：超过 100 条后旧的被覆盖、新的在前；
// 完整 path+query 保留、超长截断到 200 字节内且不产生非法 UTF-8。
func TestRecentRingAndTruncation(t *testing.T) {
	c := NewCollector()
	for i := 0; i < recentCap+50; i++ {
		c.Record(RecordInput{ServiceID: "s", Method: "GET", Path: "/r?i=" + strconv.Itoa(i), Status: 200})
	}
	snap := c.Snapshot()
	if len(snap.Recent) != recentCap {
		t.Fatalf("recent 应封顶 %d 条，得到 %d", recentCap, len(snap.Recent))
	}
	if got := snap.Recent[0].Path; got != "/r?i="+strconv.Itoa(recentCap+49) {
		t.Fatalf("最新一条应为 i=%d，得到 %q", recentCap+49, got)
	}
	if got := snap.Recent[recentCap-1].Path; got != "/r?i="+strconv.Itoa(50) {
		t.Fatalf("最旧一条应为 i=50（前 50 条被覆盖），得到 %q", got)
	}

	// 超长 path：截断 ≤200 字节且是合法 UTF-8（多字节 rune 不截半个）
	long := "/中文/" + strings.Repeat("汉", 150)
	c.Record(RecordInput{ServiceID: "s", Method: "GET", Path: long, Status: 200})
	got := c.Snapshot().Recent[0].Path
	if len(got) > pathLimit {
		t.Fatalf("超长 path 应截断到 %d 字节内，得到 %d", pathLimit, len(got))
	}
	if !strings.HasPrefix(long, got) || !utf8.ValidString(got) {
		t.Fatalf("截断应是合法前缀且 UTF-8 完整: %q", got)
	}

	// query 保留在 recent
	c.Record(RecordInput{ServiceID: "s", Method: "GET", Path: "/api/users?page=2&size=10", Status: 200})
	if got := c.Snapshot().Recent[0].Path; got != "/api/users?page=2&size=10" {
		t.Fatalf("recent 应保留完整 path+query，得到 %q", got)
	}
}

// TestTopPathsStripsQuery topPaths 聚合剥离 query：同 path 不同 query 归并为一条。
func TestTopPathsStripsQuery(t *testing.T) {
	c := NewCollector()
	c.Record(RecordInput{ServiceID: "s", Status: 200, Path: "/api/users"})
	c.Record(RecordInput{ServiceID: "s", Status: 200, Path: "/api/users?page=2"})
	c.Record(RecordInput{ServiceID: "s", Status: 500, Path: "/api/users?tab=a"})
	c.Record(RecordInput{ServiceID: "s", Status: 200, Path: "/api/orders"})

	got := c.Snapshot().Services[0].TopPaths
	if len(got) != 2 {
		t.Fatalf("剥离 query 后应聚合为 2 条，得到 %d: %+v", len(got), got)
	}
	if got[0].Path != "/api/users" || got[0].Count != 3 || got[0].Errors != 1 {
		t.Fatalf("/api/users 应 count=3 errors=1，得到 %+v", got[0])
	}
	if got[1].Path != "/api/orders" || got[1].Count != 1 {
		t.Fatalf("/api/orders 应 count=1，得到 %+v", got[1])
	}
}

// TestSetServiceNamesReplace 整体替换映射：旧条目名字变为空串（服务统计本身
// 是 App 级累计，不随映射替换消失）、新条目生效。
func TestSetServiceNamesReplace(t *testing.T) {
	c := NewCollector()
	c.SetServiceNames(map[string]string{"a": "A", "b": "B"})
	c.Record(RecordInput{ServiceID: "a", Status: 200})
	c.Record(RecordInput{ServiceID: "b", Status: 200})
	c.SetServiceNames(map[string]string{"b": "B2"})

	snap := c.Snapshot()
	for _, svc := range snap.Services {
		switch svc.ServiceID {
		case "a":
			if svc.Name != "" {
				t.Fatalf("a 不在新映射中，name 应为空串，得到 %q", svc.Name)
			}
		case "b":
			if svc.Name != "B2" {
				t.Fatalf("b 应映射到 B2，得到 %q", svc.Name)
			}
		}
	}
}

// ---------- UA 客户端 / 设备类别 ----------

// TestClassifyUA 分类规则表驱动：子串匹配、大小写不敏感、按序短路
// （bot > tablet > mobile > desktop > other）。
func TestClassifyUA(t *testing.T) {
	cases := []struct {
		ua   string
		want string
	}{
		// bot：先于一切设备形态判定（Chrome UA 里没有这些词，Googlebot 命中）
		{"Mozilla/5.0 (compatible; Googlebot/2.1; +http://www.google.com/bot.html)", deviceBot},
		{"Mozilla/5.0 (Windows NT 10.0) AppleWebKit/537.36 Spider/1.0", deviceBot},
		{"SomeCrawler/2.0", deviceBot},
		// tablet：iPad；Android 无 Mobile 后缀多为平板
		{"Mozilla/5.0 (iPad; CPU OS 17_0) AppleWebKit/605.1.15 Safari/604.1", deviceTablet},
		{"Mozilla/5.0 (Linux; Android 13; SM-X710) AppleWebKit/537.36 Chrome/126.0 Safari/537.36", deviceTablet},
		// mobile：iPhone/iPod/Android 手机（含 Mobile 后缀）/HarmonyOS
		{"Mozilla/5.0 (iPhone; CPU iPhone OS 17_0) AppleWebKit/605.1.15 Safari/604.1", deviceMobile},
		{"Mozilla/5.0 (iPod touch; CPU iPhone OS 15_0)", deviceMobile},
		{"Mozilla/5.0 (Linux; Android 14; Pixel 8; Mobile) Chrome/126.0 Mobile Safari/537.36", deviceMobile},
		{"Mozilla/5.0 (Phone; HarmonyOS 5.0; HBD-AL00) HuaweiBrowser/5.0", deviceMobile},
		{"curl/8.0, mobile", deviceMobile}, // mobile 子串命中（人工构造，验证规则本身）
		// desktop：Windows/Mac/X11/Linux x86
		{"Mozilla/5.0 (Windows NT 10.0; Win64; x64) Chrome/126.0", deviceDesktop},
		{"Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) Safari/17.0", deviceDesktop},
		{"Mozilla/5.0 (X11; Linux x86_64) Firefox/127.0", deviceDesktop},
		// other：无法归类与空 UA
		{"curl/8.4.0", deviceOther},
		{"Python-urllib/3.11", deviceOther},
		{"", deviceOther},
	}
	for _, tc := range cases {
		if got := classifyUA(tc.ua); got != tc.want {
			t.Errorf("classifyUA(%q) = %q, want %q", tc.ua, got, tc.want)
		}
	}
}

// TestClientAndDevicesAggregation clients 去重（空 UA 不计）与 devices 累加
// （含空 UA→other）；快照 devices 按 count 降序、空数组非 null。
func TestClientAndDevicesAggregation(t *testing.T) {
	c := NewCollector()
	chrome := "Mozilla/5.0 (Windows NT 10.0) Chrome/126.0"
	iphone := "Mozilla/5.0 (iPhone; CPU iPhone OS 17_0) Safari/604.1"
	ipad := "Mozilla/5.0 (iPad; CPU OS 17_0) Safari/604.1"

	// chrome×3（同 UA 只算 1 个 client）、iphone×2、ipad×1、空 UA×2
	for i := 0; i < 3; i++ {
		c.Record(RecordInput{ServiceID: "s", UA: chrome, Status: 200})
	}
	c.Record(RecordInput{ServiceID: "s", UA: iphone, Status: 200})
	c.Record(RecordInput{ServiceID: "s", UA: iphone, Status: 200})
	c.Record(RecordInput{ServiceID: "s", UA: ipad, Status: 200})
	c.Record(RecordInput{ServiceID: "s", UA: "", Status: 200})
	c.Record(RecordInput{ServiceID: "s", UA: "", Status: 200})

	snap := c.Snapshot()
	if snap.Total.Clients != 3 {
		t.Fatalf("去重 clients 应为 3（空 UA 不计），得到 %d", snap.Total.Clients)
	}
	want := []DeviceStat{
		{Type: deviceDesktop, Count: 3}, // chrome×3
		{Type: deviceMobile, Count: 2},  // iphone×2
		{Type: deviceOther, Count: 2},   // 空 UA×2
		{Type: deviceTablet, Count: 1},  // ipad×1
	}
	if len(snap.Devices) != len(want) {
		t.Fatalf("devices 应有 %d 类，得到 %+v", len(want), snap.Devices)
	}
	for i, d := range want {
		if snap.Devices[i] != d {
			t.Fatalf("devices[%d] 应为 %+v，得到 %+v", i, d, snap.Devices[i])
		}
	}

	// 空集合（新收集器）：devices 为空数组而非 null
	if fresh := NewCollector().Snapshot(); fresh.Devices == nil || len(fresh.Devices) != 0 {
		t.Fatalf("无请求时 devices 应为空数组，得到 %#v", fresh.Devices)
	}
}

// TestClientsCappedAtLimit clients 封顶：distinct UA 到 1000 后不再新增种类
// （计数冻结），已记录的重复请求不影响总数。
func TestClientsCappedAtLimit(t *testing.T) {
	c := NewCollector()
	for i := 0; i < maxClients; i++ {
		c.Record(RecordInput{ServiceID: "s", UA: "ua-" + strconv.Itoa(i), Status: 200})
	}
	if got := c.Snapshot().Total.Clients; got != maxClients {
		t.Fatalf("封顶前 clients 应为 %d，得到 %d", maxClients, got)
	}
	// 封顶后再来新种类 + 旧种类重复：数量冻结在上限
	for i := 0; i < 50; i++ {
		c.Record(RecordInput{ServiceID: "s", UA: "new-" + strconv.Itoa(i), Status: 200})
	}
	c.Record(RecordInput{ServiceID: "s", UA: "ua-0", Status: 200})
	if got := c.Snapshot().Total.Clients; got != maxClients {
		t.Fatalf("封顶后 clients 应冻结在 %d，得到 %d", maxClients, got)
	}
	// 空 UA 永不计入
	c.Record(RecordInput{ServiceID: "s", UA: "", Status: 200})
	if got := c.Snapshot().Total.Clients; got != maxClients {
		t.Fatalf("空 UA 不应影响 clients，得到 %d", got)
	}
}

// ---------- 服务器归属 ----------

// TestServerAttribution recent 的 serverId 按请求发生时捕获（服务迁移后在途
// 请求仍报旧归属）；ServiceStats 的 serverId/serverName 指当前归属。
func TestServerAttribution(t *testing.T) {
	c := NewCollector()
	c.SetServiceNames(map[string]string{"svc": "my-api"})
	c.SetServiceServers(map[string]string{"svc": "srv-a"})
	c.SetServerNames(map[string]string{"srv-a": "甲服务器", "srv-b": "乙服务器"})

	// 请求发生时归属 srv-a
	c.Record(RecordInput{ServiceID: "svc", ServerID: "srv-a", ServerName: "甲服务器", Status: 200})
	// 服务迁移到 srv-b 后，新请求捕获 srv-b
	c.SetServiceServers(map[string]string{"svc": "srv-b"})
	c.Record(RecordInput{ServiceID: "svc", ServerID: "srv-b", ServerName: "乙服务器", Status: 200})

	snap := c.Snapshot()
	// recent：按请求捕获（第一条仍是 srv-a，不被当前归属污染）
	if snap.Recent[1].ServerID != "srv-a" || snap.Recent[0].ServerID != "srv-b" {
		t.Fatalf("recent serverId 应按请求捕获: %+v", snap.Recent)
	}
	// serverName 同刻捕获：请求发生时的名字，改名不回填历史
	if snap.Recent[1].ServerName != "甲服务器" || snap.Recent[0].ServerName != "乙服务器" {
		t.Fatalf("recent serverName 应按请求捕获: %+v", snap.Recent)
	}
	// ServiceStats：当前归属 srv-b + 服务器名解析
	if len(snap.Services) != 1 {
		t.Fatalf("应只有 1 个服务: %+v", snap.Services)
	}
	st := snap.Services[0]
	if st.ServerID != "srv-b" || st.ServerName != "乙服务器" {
		t.Fatalf("ServiceStats 应指当前归属: %+v", st)
	}
}
