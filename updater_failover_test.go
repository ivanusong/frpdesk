package main

// 更新源故障转移测试：镜像优先、主源兜底的 Check 合并与 Download 换源。
// 全部使用 httptest/桩实现，不访问真实网络：
//   - Check：同版本合并、镜像"无更新"不短路、来源错误回退、版本滞后、
//     整体取消、镜像超时不拖垮主源；
//   - Download：镜像 HTTP 失败/截断连接/摘要不一致时换源、目标文件不被拼接污染、
//     进度单调且收敛、全源失败保持目标为空、取消及时、
//     主源同版本懒查询与版本不一致拒绝；
//   - 真实路径：镜像立即返回新版本而主源挂起时，最终仍返回镜像版本；
//     body 挂起到时换源、父 ctx 取消不重试。
//   - 构造：镜像未配置保持原版 endpoint Provider，镜像配置非法回退主源，
//     各来源独立短超时与下载尝试上限的默认值。

import (
	"bytes"
	"context"
	"crypto/sha512"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/wailsapp/wails/v3/pkg/updater"
	"github.com/wailsapp/wails/v3/pkg/updater/providers/endpoint"
)

const (
	testMirrorBase      = "https://mirror.example.com"
	testPrimaryManifest = "https://primary.example.com/stable.json"
	testArtifactName    = "frpdesk-0.2.11-darwin-arm64.zip"
)

// --- 桩与工具 ---

// fakeManifestSource 可编程的清单来源：固定返回值/错误，可延迟（尊重 ctx 取消）。
type fakeManifestSource struct {
	mu    sync.Mutex
	rel   *updater.Release
	err   error
	delay time.Duration
	calls int
}

func (f *fakeManifestSource) Check(ctx context.Context, req updater.CheckRequest) (*updater.Release, error) {
	f.mu.Lock()
	f.calls++
	rel, err, delay := f.rel, f.err, f.delay
	f.mu.Unlock()
	if delay > 0 {
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(delay):
		}
	}
	return rel, err
}

func (f *fakeManifestSource) callCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.calls
}

// manifestRelease 构造带 endpoint 下载地址的清单 release。
func manifestRelease(version, filename, artifactURL string) *updater.Release {
	return &updater.Release{
		Version:  version,
		Artifact: updater.Artifact{Filename: filename, Platform: "darwin", Arch: "arm64"},
		Metadata: map[string]any{"endpoint.artifact.url": artifactURL},
	}
}

func checkRequest() updater.CheckRequest {
	return updater.CheckRequest{CurrentVersion: "0.2.10", Platform: "darwin", Arch: "arm64"}
}

// newFailoverForTest 走真实工厂构造（覆盖 source 布局），再替换为桩来源。
func newFailoverForTest(t *testing.T, mirror, primary manifestChecker) (*failoverProvider, *logRecorder) {
	t.Helper()
	logs := &logRecorder{}
	prov, err := newUpdateProvider(updateProviderConfig{
		manifestURL:   testPrimaryManifest,
		mirrorBaseURL: testMirrorBase,
		log:           logs.logf,
	})
	if err != nil {
		t.Fatalf("newUpdateProvider: %v", err)
	}
	fp := prov.(*failoverProvider)
	if mirror != nil {
		fp.sources[0].provider = mirror
	}
	if primary != nil {
		fp.sources[1].provider = primary
	}
	return fp, logs
}

// countingHandler 计录命中次数的静态响应服务器。
type countingHandler struct {
	status int
	body   []byte
	hits   atomic.Int64
}

func (h *countingHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	h.hits.Add(1)
	w.WriteHeader(h.status)
	_, _ = w.Write(h.body)
}

func newByteServer(t *testing.T, status int, body []byte) (*httptest.Server, *countingHandler) {
	t.Helper()
	h := &countingHandler{status: status, body: body}
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)
	return srv, h
}

// truncatingHandler 写出 prefixLen 字节后直接掐断连接（模拟传输中断）。
type truncatingHandler struct {
	body      []byte
	prefixLen int
	hits      atomic.Int64
}

func (h *truncatingHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	h.hits.Add(1)
	w.Header().Set("Content-Length", fmt.Sprint(len(h.body)))
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(h.body[:h.prefixLen])
	w.(http.Flusher).Flush()
	panic(http.ErrAbortHandler) // 掐断连接：客户端读到 unexpected EOF
}

// slowHandler 分块慢速发送（模拟慢速下载，可被 ctx 取消打断）。
type slowHandler struct {
	body  []byte
	chunk int
	pause time.Duration
}

func (h *slowHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Length", fmt.Sprint(len(h.body)))
	w.WriteHeader(http.StatusOK)
	flusher := w.(http.Flusher)
	for off := 0; off < len(h.body); off += h.chunk {
		end := off + h.chunk
		if end > len(h.body) {
			end = len(h.body)
		}
		if _, err := w.Write(h.body[off:end]); err != nil {
			return
		}
		flusher.Flush()
		select {
		case <-r.Context().Done():
			return
		case <-time.After(h.pause):
		}
	}
}

// manifestHandler 同时提供清单与制品（懒查询主源场景）。
type manifestHandler struct {
	mu          sync.Mutex
	manifest    []byte
	artifact    []byte
	manifestHit atomic.Int64
	artifactHit atomic.Int64
}

func (h *manifestHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if strings.HasSuffix(r.URL.Path, "/stable.json") {
		h.manifestHit.Add(1)
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write(h.manifest)
		return
	}
	h.artifactHit.Add(1)
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(h.artifact)
}

// buildManifest 生成 Wails updater manifest JSON（懒查询场景用）。
func buildManifest(t *testing.T, version, filename string, payload []byte) []byte {
	t.Helper()
	sum := sha512.Sum512(payload)
	doc := map[string]any{
		"schemaVersion": 1,
		"version":       version,
		"channel":       "stable",
		"artifacts": []map[string]any{{
			"url":        filename,
			"platform":   "darwin",
			"arch":       "arm64",
			"size":       len(payload),
			"digestAlgo": "sha512",
			"digest":     base64.StdEncoding.EncodeToString(sum[:]),
		}},
	}
	data, err := json.Marshal(doc)
	if err != nil {
		t.Fatalf("buildManifest: %v", err)
	}
	return data
}

// downloadRelease 构造带元数据候选与摘要的待安装 release。
func downloadRelease(payload []byte, mirrorURL, primaryURL string) *updater.Release {
	sum := sha512.Sum512(payload)
	rel := &updater.Release{
		Version: "0.2.11",
		Artifact: updater.Artifact{
			Filename: testArtifactName,
			Platform: "darwin",
			Arch:     "arm64",
			Size:     int64(len(payload)),
		},
		Verification: &updater.Verification{DigestAlgo: "sha512", Digest: sum[:]},
		Metadata:     map[string]any{},
	}
	if mirrorURL != "" {
		rel.Metadata[metaMirrorURL] = mirrorURL
	}
	if primaryURL != "" {
		rel.Metadata[metaPrimaryURL] = primaryURL
	}
	return rel
}

type progressRecorder struct {
	mu      sync.Mutex
	written []int64
	totals  []int64
}

func (p *progressRecorder) fn(written, total int64) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.written = append(p.written, written)
	p.totals = append(p.totals, total)
}

// --- Check 合并 ---

func TestFailoverCheck_SameVersionPrimaryAuthoritativeMirrorDownloadFirst(t *testing.T) {
	primaryURL := "https://github.example/releases/download/v0.2.11/" + testArtifactName
	fp, _ := newFailoverForTest(t,
		&fakeManifestSource{rel: manifestRelease("0.2.11", testArtifactName, testMirrorBase+"/releases/0.2.11/"+testArtifactName)},
		&fakeManifestSource{rel: manifestRelease("0.2.11", testArtifactName, primaryURL)},
	)

	rel, err := fp.Check(context.Background(), checkRequest())
	if err != nil {
		t.Fatalf("Check: %v", err)
	}
	if rel.Version != "0.2.11" {
		t.Errorf("version = %q, want 0.2.11", rel.Version)
	}
	// 同版本以主源清单为准（元数据/摘要权威）
	if got := metadataString(rel, metaPrimaryURL); got != primaryURL {
		t.Errorf("primary 候选 = %q, want %q", got, primaryURL)
	}
	// 镜像地址按约定拼接，且是下载首选
	wantMirror := testMirrorBase + "/releases/0.2.11/" + testArtifactName
	if got := metadataString(rel, metaMirrorURL); got != wantMirror {
		t.Errorf("mirror 候选 = %q, want %q", got, wantMirror)
	}
	if cands := candidatesFromMetadata(rel); len(cands) != 2 || cands[0].name != "mirror" || cands[1].name != "primary" {
		t.Errorf("下载候选顺序 = %+v, want mirror→primary", cands)
	}
}

func TestFailoverCheck_MirrorNoUpdateDoesNotBlockPrimary(t *testing.T) {
	primaryURL := "https://github.example/releases/download/v0.2.11/" + testArtifactName
	fp, _ := newFailoverForTest(t,
		&fakeManifestSource{rel: nil}, // 镜像可访问但无更新
		&fakeManifestSource{rel: manifestRelease("0.2.11", testArtifactName, primaryURL)},
	)

	rel, err := fp.Check(context.Background(), checkRequest())
	if err != nil {
		t.Fatalf("镜像无更新不得短路主源：%v", err)
	}
	if rel == nil || rel.Version != "0.2.11" {
		t.Fatalf("rel = %+v, want 0.2.11", rel)
	}
	if got := metadataString(rel, metaPrimaryURL); got != primaryURL {
		t.Errorf("primary 候选 = %q, want %q", got, primaryURL)
	}
}

func TestFailoverCheck_MirrorErrorFallsBackToPrimary(t *testing.T) {
	primaryURL := "https://github.example/releases/download/v0.2.11/" + testArtifactName
	fp, logs := newFailoverForTest(t,
		&fakeManifestSource{err: errors.New("mirror down")},
		&fakeManifestSource{rel: manifestRelease("0.2.11", testArtifactName, primaryURL)},
	)

	rel, err := fp.Check(context.Background(), checkRequest())
	if err != nil {
		t.Fatalf("镜像失败时应回退主源：%v", err)
	}
	if rel == nil || rel.Version != "0.2.11" {
		t.Fatalf("rel = %+v, want 0.2.11", rel)
	}
	if !logs.contains("mirror 更新源检查失败") {
		t.Errorf("应记录镜像错误日志：%v", logs.line)
	}
}

func TestFailoverCheck_PrimaryNewerThanMirrorWins(t *testing.T) {
	primaryURL := "https://github.example/releases/download/v0.2.12/frpdesk-0.2.12-darwin-arm64.zip"
	fp, _ := newFailoverForTest(t,
		&fakeManifestSource{rel: manifestRelease("0.2.11", testArtifactName, testMirrorBase+"/releases/0.2.11/"+testArtifactName)},
		&fakeManifestSource{rel: manifestRelease("0.2.12", "frpdesk-0.2.12-darwin-arm64.zip", primaryURL)},
	)

	rel, err := fp.Check(context.Background(), checkRequest())
	if err != nil {
		t.Fatalf("Check: %v", err)
	}
	if rel.Version != "0.2.12" {
		t.Errorf("version = %q, want 0.2.12（镜像滞后不得阻止更新）", rel.Version)
	}
	if got := metadataString(rel, metaPrimaryURL); got != primaryURL {
		t.Errorf("primary 候选 = %q, want %q", got, primaryURL)
	}
	// 镜像地址按所选新版本拼接（可能 404，由下载阶段快速回退）
	wantMirror := testMirrorBase + "/releases/0.2.12/frpdesk-0.2.12-darwin-arm64.zip"
	if got := metadataString(rel, metaMirrorURL); got != wantMirror {
		t.Errorf("mirror 候选 = %q, want %q", got, wantMirror)
	}
}

func TestFailoverCheck_MirrorNewerThanPrimaryHasNoPrimaryCandidate(t *testing.T) {
	fp, _ := newFailoverForTest(t,
		&fakeManifestSource{rel: manifestRelease("0.2.12", "frpdesk-0.2.12-darwin-arm64.zip", testMirrorBase+"/releases/0.2.12/frpdesk-0.2.12-darwin-arm64.zip")},
		&fakeManifestSource{rel: manifestRelease("0.2.11", testArtifactName, "https://github.example/old.zip")},
	)

	rel, err := fp.Check(context.Background(), checkRequest())
	if err != nil {
		t.Fatalf("Check: %v", err)
	}
	if rel.Version != "0.2.12" {
		t.Errorf("version = %q, want 0.2.12", rel.Version)
	}
	// 主源清单与所选版本不同：不得作为回退地址（绝不跨版本回退）
	if got := metadataString(rel, metaPrimaryURL); got != "" {
		t.Errorf("primary 候选 = %q, want 空", got)
	}
}

func TestFailoverCheck_AllSourcesFail(t *testing.T) {
	fp, _ := newFailoverForTest(t,
		&fakeManifestSource{err: errors.New("mirror down")},
		&fakeManifestSource{err: errors.New("primary down")},
	)
	rel, err := fp.Check(context.Background(), checkRequest())
	if err == nil {
		t.Fatalf("全部来源失败必须报错，rel = %+v", rel)
	}
	if !strings.Contains(err.Error(), "primary down") || !strings.Contains(err.Error(), "mirror down") {
		t.Errorf("错误应包含各来源原因：%v", err)
	}
}

func TestFailoverCheck_AllUpToDate(t *testing.T) {
	fp, _ := newFailoverForTest(t, &fakeManifestSource{}, &fakeManifestSource{})
	rel, err := fp.Check(context.Background(), checkRequest())
	if err != nil || rel != nil {
		t.Fatalf("双源无更新 = (%v, %v), want (nil, nil)", rel, err)
	}
}

func TestFailoverCheck_UpToDateWinsOverOtherSourceError(t *testing.T) {
	// 镜像（或主源）确认无更新时，另一来源的网络错误只记日志不阻断结论
	fp, logs := newFailoverForTest(t,
		&fakeManifestSource{},
		&fakeManifestSource{err: errors.New("primary unreachable")},
	)
	rel, err := fp.Check(context.Background(), checkRequest())
	if err != nil || rel != nil {
		t.Fatalf("无更新 + 他源错误 = (%v, %v), want (nil, nil)", rel, err)
	}
	if !logs.contains("primary unreachable") {
		t.Errorf("应记录另一来源的错误日志：%v", logs.line)
	}
}

func TestFailoverCheck_ContextCancelReturnsPromptly(t *testing.T) {
	fp, _ := newFailoverForTest(t,
		&fakeManifestSource{delay: 5 * time.Second},
		&fakeManifestSource{delay: 5 * time.Second},
	)
	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		time.Sleep(50 * time.Millisecond)
		cancel()
	}()
	start := time.Now()
	_, err := fp.Check(ctx, checkRequest())
	elapsed := time.Since(start)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v, want context.Canceled", err)
	}
	if elapsed > 3*time.Second {
		t.Errorf("取消后 %v 才返回，响应不及时", elapsed)
	}
}

func TestFailoverCheck_PrimaryHangsMirrorResultWins(t *testing.T) {
	// 真实 httptest + endpoint provider：镜像立即返回新版本，主源挂起直到
	// 自己的独立尝试截止；最终必须返回镜像版本，而不是整体 ctx 超时失败。
	payload := bytes.Repeat([]byte{0xAB}, 1024)
	mirrorSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write(buildManifest(t, "0.2.11", testArtifactName, payload))
	}))
	t.Cleanup(mirrorSrv.Close)
	hangSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		<-r.Context().Done() // 主源挂起，直到客户端请求 ctx 结束
	}))
	t.Cleanup(hangSrv.Close)

	prov, err := newUpdateProvider(updateProviderConfig{
		manifestURL:   hangSrv.URL + "/stable.json",
		mirrorBaseURL: mirrorSrv.URL,
		log:           (&logRecorder{}).logf,
	})
	if err != nil {
		t.Fatalf("newUpdateProvider: %v", err)
	}
	fp := prov.(*failoverProvider)
	// 压缩生产默认（5s）以加速测试：机制不变——挂起来源只耗自己的独立上限
	fp.sources[1].timeout = 300 * time.Millisecond

	start := time.Now()
	rel, err := fp.Check(context.Background(), checkRequest())
	elapsed := time.Since(start)
	if err != nil {
		t.Fatalf("主源挂起时镜像结果必须生效：%v", err)
	}
	if rel == nil || rel.Version != "0.2.11" {
		t.Fatalf("rel = %+v, want 0.2.11", rel)
	}
	if elapsed > 5*time.Second {
		t.Errorf("耗时 %v：不应等待挂起主源耗尽整体 30s 预算", elapsed)
	}
}

func TestFailoverCheck_MirrorTimeoutDoesNotBlockPrimary(t *testing.T) {
	fp, _ := newFailoverForTest(t,
		&fakeManifestSource{delay: 500 * time.Millisecond}, // 镜像慢
		&fakeManifestSource{rel: manifestRelease("0.2.11", testArtifactName, "https://github.example/x.zip")},
	)
	fp.sources[0].timeout = 50 * time.Millisecond // 压缩镜像单次尝试上限

	start := time.Now()
	rel, err := fp.Check(context.Background(), checkRequest())
	elapsed := time.Since(start)
	if err != nil {
		t.Fatalf("镜像超时应回退主源：%v", err)
	}
	if rel == nil || rel.Version != "0.2.11" {
		t.Fatalf("rel = %+v, want 0.2.11", rel)
	}
	if elapsed >= 500*time.Millisecond {
		t.Errorf("镜像超时后 %v 才返回，应快速回退", elapsed)
	}
}

// --- Download 换源 ---

func TestFailoverDownload_MirrorFirst(t *testing.T) {
	payload := bytes.Repeat([]byte{0xAB}, 100_000)
	mirrorSrv, mirrorHits := newByteServer(t, http.StatusOK, payload)
	primarySrv, primaryHits := newByteServer(t, http.StatusOK, payload)
	rel := downloadRelease(payload, mirrorSrv.URL+"/"+testArtifactName, primarySrv.URL+"/"+testArtifactName)
	fp := &failoverProvider{client: newUpdateHTTPClient(), log: (&logRecorder{}).logf}

	var dst bytes.Buffer
	progress := &progressRecorder{}
	if err := fp.Download(context.Background(), rel, &dst, progress.fn); err != nil {
		t.Fatalf("Download: %v", err)
	}
	if !bytes.Equal(dst.Bytes(), payload) {
		t.Fatalf("下载内容不一致：got %d 字节", dst.Len())
	}
	if mirrorHits.hits.Load() != 1 || primaryHits.hits.Load() != 0 {
		t.Errorf("应只请求镜像：mirror=%d primary=%d", mirrorHits.hits.Load(), primaryHits.hits.Load())
	}
	lastWritten, lastTotal := progress.written[len(progress.written)-1], progress.totals[len(progress.totals)-1]
	if lastWritten != int64(len(payload)) || lastTotal != int64(len(payload)) {
		t.Errorf("进度末值 = (%d, %d), want (%d, %d)", lastWritten, lastTotal, len(payload), len(payload))
	}
	for i := 1; i < len(progress.written); i++ {
		if progress.written[i] < progress.written[i-1] {
			t.Fatalf("进度回退：%v", progress.written)
		}
	}
}

func TestFailoverDownload_MirrorHTTPErrorFallsBackToPrimary(t *testing.T) {
	payload := bytes.Repeat([]byte{0xAB}, 50_000)
	mirrorSrv, mirrorHits := newByteServer(t, http.StatusInternalServerError, nil)
	primarySrv, primaryHits := newByteServer(t, http.StatusOK, payload)
	rel := downloadRelease(payload, mirrorSrv.URL+"/"+testArtifactName, primarySrv.URL+"/"+testArtifactName)
	fp := &failoverProvider{client: newUpdateHTTPClient(), log: (&logRecorder{}).logf}

	var dst bytes.Buffer
	if err := fp.Download(context.Background(), rel, &dst, nil); err != nil {
		t.Fatalf("镜像 HTTP 失败应换源成功：%v", err)
	}
	if !bytes.Equal(dst.Bytes(), payload) {
		t.Fatalf("换源后内容不一致：got %d 字节", dst.Len())
	}
	if mirrorHits.hits.Load() != 1 || primaryHits.hits.Load() != 1 {
		t.Errorf("命中次数 mirror=%d primary=%d, want 1/1", mirrorHits.hits.Load(), primaryHits.hits.Load())
	}
}

func TestFailoverDownload_TruncatedMirrorNeverSplices(t *testing.T) {
	// 镜像写出一半（且是不同内容的前缀）后掐断连接；主源给完整正确内容。
	// 期望：目标文件等于主源完整内容——既没有镜像前缀残留，也没有两源拼接。
	good := bytes.Repeat([]byte{0xAB}, 60_000)
	bad := bytes.Repeat([]byte{0xCD}, 60_000)

	truncSrv := httptest.NewServer(&truncatingHandler{body: bad, prefixLen: len(bad) / 2})
	t.Cleanup(truncSrv.Close)
	primarySrv, primaryHits := newByteServer(t, http.StatusOK, good)

	rel := downloadRelease(good, truncSrv.URL+"/"+testArtifactName, primarySrv.URL+"/"+testArtifactName)
	fp := &failoverProvider{client: newUpdateHTTPClient(), log: (&logRecorder{}).logf}

	var dst bytes.Buffer
	if err := fp.Download(context.Background(), rel, &dst, nil); err != nil {
		t.Fatalf("镜像截断应换源成功：%v", err)
	}
	if !bytes.Equal(dst.Bytes(), good) {
		t.Fatalf("目标文件被污染：got %d 字节，want 恰好主源完整 %d 字节（不得拼接）", dst.Len(), len(good))
	}
	if primaryHits.hits.Load() != 1 {
		t.Errorf("主源命中 %d 次, want 1", primaryHits.hits.Load())
	}
}

func TestFailoverDownload_MirrorDigestMismatchFallsBackToPrimary(t *testing.T) {
	// 镜像完整返回了错误内容：摘要预校验失败后必须换源，而不是把坏包交给框架。
	good := bytes.Repeat([]byte{0xAB}, 40_000)
	bad := bytes.Repeat([]byte{0xCD}, 40_000)
	mirrorSrv, mirrorHits := newByteServer(t, http.StatusOK, bad)
	primarySrv, primaryHits := newByteServer(t, http.StatusOK, good)
	rel := downloadRelease(good, mirrorSrv.URL+"/"+testArtifactName, primarySrv.URL+"/"+testArtifactName)
	fp := &failoverProvider{client: newUpdateHTTPClient(), log: (&logRecorder{}).logf}

	var dst bytes.Buffer
	if err := fp.Download(context.Background(), rel, &dst, nil); err != nil {
		t.Fatalf("镜像摘要不一致应换源成功：%v", err)
	}
	if !bytes.Equal(dst.Bytes(), good) {
		t.Fatalf("目标内容不是主源正确包：got %d 字节", dst.Len())
	}
	if mirrorHits.hits.Load() != 1 || primaryHits.hits.Load() != 1 {
		t.Errorf("命中次数 mirror=%d primary=%d, want 1/1", mirrorHits.hits.Load(), primaryHits.hits.Load())
	}
}

func TestFailoverDownload_BodyHangFallsBackToPrimary(t *testing.T) {
	// 镜像 headers 成功后 body 挂起：单次尝试到时必须失败并换源，
	// 而不是永远等待。
	good := bytes.Repeat([]byte{0xAB}, 30_000)
	hangSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Length", fmt.Sprint(len(good)))
		w.WriteHeader(http.StatusOK)
		w.(http.Flusher).Flush() // headers 已送达
		<-r.Context().Done()     // body 挂起
	}))
	t.Cleanup(hangSrv.Close)
	primarySrv, primaryHits := newByteServer(t, http.StatusOK, good)
	rel := downloadRelease(good, hangSrv.URL+"/"+testArtifactName, primarySrv.URL+"/"+testArtifactName)
	fp := &failoverProvider{
		client:                 newUpdateHTTPClient(),
		downloadAttemptTimeout: 300 * time.Millisecond,
		log:                    (&logRecorder{}).logf,
	}

	var dst bytes.Buffer
	start := time.Now()
	if err := fp.Download(context.Background(), rel, &dst, nil); err != nil {
		t.Fatalf("body 挂起到时后应换源成功：%v", err)
	}
	if elapsed := time.Since(start); elapsed > 5*time.Second {
		t.Errorf("耗时 %v：body 挂起应尽快到时换源", elapsed)
	}
	if !bytes.Equal(dst.Bytes(), good) {
		t.Fatalf("换源后内容不一致：got %d 字节", dst.Len())
	}
	if primaryHits.hits.Load() != 1 {
		t.Errorf("主源命中 %d 次, want 1", primaryHits.hits.Load())
	}
}

func TestFailoverDownload_ParentCancelDoesNotRetry(t *testing.T) {
	// 父 ctx（用户/应用退出）取消：立即终止，绝不再换源重试。
	payload := bytes.Repeat([]byte{0xAB}, 80_000)
	slowSrv := httptest.NewServer(&slowHandler{body: payload, chunk: 8_000, pause: 40 * time.Millisecond})
	t.Cleanup(slowSrv.Close)
	primarySrv, primaryHits := newByteServer(t, http.StatusOK, payload)
	rel := downloadRelease(payload, slowSrv.URL+"/"+testArtifactName, primarySrv.URL+"/"+testArtifactName)
	fp := &failoverProvider{
		client:                 newUpdateHTTPClient(),
		downloadAttemptTimeout: 5 * time.Minute,
		log:                    (&logRecorder{}).logf,
	}

	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		time.Sleep(100 * time.Millisecond)
		cancel()
	}()
	var dst bytes.Buffer
	start := time.Now()
	err := fp.Download(ctx, rel, &dst, nil)
	elapsed := time.Since(start)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v, want context.Canceled", err)
	}
	if elapsed > 3*time.Second {
		t.Errorf("取消后 %v 才返回，响应不及时", elapsed)
	}
	if primaryHits.hits.Load() != 0 {
		t.Errorf("父 ctx 取消后不得换源重试，主源命中 %d 次", primaryHits.hits.Load())
	}
	if dst.Len() != 0 {
		t.Errorf("取消后目标必须为空，实际 %d 字节", dst.Len())
	}
}

func TestFailoverDownload_AllFailKeepsDestinationClean(t *testing.T) {
	payload := bytes.Repeat([]byte{0xAB}, 1024)
	mirrorSrv, _ := newByteServer(t, http.StatusInternalServerError, nil)
	primarySrv, _ := newByteServer(t, http.StatusInternalServerError, nil)
	rel := downloadRelease(payload, mirrorSrv.URL+"/"+testArtifactName, primarySrv.URL+"/"+testArtifactName)
	fp := &failoverProvider{client: newUpdateHTTPClient(), log: (&logRecorder{}).logf}

	var dst bytes.Buffer
	err := fp.Download(context.Background(), rel, &dst, nil)
	if err == nil {
		t.Fatal("全源失败必须报错")
	}
	if dst.Len() != 0 {
		t.Errorf("全源失败后目标必须为空，实际 %d 字节", dst.Len())
	}
}

func TestFailoverDownload_NoCandidatesFails(t *testing.T) {
	rel := downloadRelease([]byte{1, 2, 3}, "", "")
	fp := &failoverProvider{client: newUpdateHTTPClient(), log: (&logRecorder{}).logf}
	var dst bytes.Buffer
	if err := fp.Download(context.Background(), rel, &dst, nil); err == nil {
		t.Fatal("无候选地址必须报错")
	}
}

func TestFailoverDownload_ContextCancelKeepsDestinationClean(t *testing.T) {
	payload := bytes.Repeat([]byte{0xAB}, 80_000)
	slow := httptest.NewServer(&slowHandler{body: payload, chunk: 8_000, pause: 40 * time.Millisecond})
	t.Cleanup(slow.Close)
	rel := downloadRelease(payload, slow.URL+"/"+testArtifactName, "")
	fp := &failoverProvider{client: newUpdateHTTPClient(), log: (&logRecorder{}).logf}

	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		time.Sleep(100 * time.Millisecond)
		cancel()
	}()
	var dst bytes.Buffer
	start := time.Now()
	err := fp.Download(ctx, rel, &dst, nil)
	elapsed := time.Since(start)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v, want context.Canceled", err)
	}
	if elapsed > 3*time.Second {
		t.Errorf("取消后 %v 才返回，响应不及时", elapsed)
	}
	if dst.Len() != 0 {
		t.Errorf("取消后目标必须为空（半途字节留在临时文件），实际 %d 字节", dst.Len())
	}
}

// --- Download：主源同版本懒查询 ---

// lazyFixture 主源清单/制品同服务器：镜像失败后走实时懒查询回退。
type lazyFixture struct {
	fp          *failoverProvider
	primary     *manifestHandler
	artifactSrv *httptest.Server
}

func newLazyFixture(t *testing.T, manifestVersion, manifestFilename string, artifact []byte) *lazyFixture {
	t.Helper()
	primary := &manifestHandler{manifest: buildManifest(t, manifestVersion, manifestFilename, artifact), artifact: artifact}
	primarySrv := httptest.NewServer(primary)
	t.Cleanup(primarySrv.Close)

	failMirror, _ := newByteServer(t, http.StatusInternalServerError, nil)
	logs := &logRecorder{}
	prov, err := newUpdateProvider(updateProviderConfig{
		manifestURL:   primarySrv.URL + "/stable.json",
		mirrorBaseURL: failMirror.URL, // 镜像清单失败 → Check 期没有主源候选也拿不到镜像制品
		log:           logs.logf,
	})
	if err != nil {
		t.Fatalf("newUpdateProvider: %v", err)
	}
	return &lazyFixture{fp: prov.(*failoverProvider), primary: primary, artifactSrv: primarySrv}
}

func TestFailoverDownload_LazyPrimaryLookupSameVersion(t *testing.T) {
	payload := bytes.Repeat([]byte{0xAB}, 30_000)
	fx := newLazyFixture(t, "0.2.11", testArtifactName, payload)
	// Check 期元数据里只有镜像候选且镜像失败（500），主源候选缺失：
	// 下载失败后必须实时查询主源清单补同版本地址
	mirrorFail, _ := newByteServer(t, http.StatusInternalServerError, nil)
	rel := downloadRelease(payload, mirrorFail.URL+"/"+testArtifactName, "")
	fp := fx.fp

	var dst bytes.Buffer
	if err := fp.Download(context.Background(), rel, &dst, nil); err != nil {
		t.Fatalf("懒查询主源应完成回退下载：%v", err)
	}
	if !bytes.Equal(dst.Bytes(), payload) {
		t.Fatalf("回退下载内容不一致：got %d 字节", dst.Len())
	}
	if fx.primary.manifestHit.Load() < 1 {
		t.Error("应实时查询主源清单获取同版本地址")
	}
	if fx.primary.artifactHit.Load() != 1 {
		t.Errorf("主源制品命中 %d 次, want 1", fx.primary.artifactHit.Load())
	}
}

func TestFailoverDownload_LazyPrimaryLookupRejectsNewerVersion(t *testing.T) {
	payload := bytes.Repeat([]byte{0xAB}, 30_000)
	newerName := "frpdesk-0.2.12-darwin-arm64.zip"
	fx := newLazyFixture(t, "0.2.12", newerName, payload)
	mirrorFail, _ := newByteServer(t, http.StatusInternalServerError, nil)
	rel := downloadRelease(payload, mirrorFail.URL+"/"+testArtifactName, "")
	fp := fx.fp

	var dst bytes.Buffer
	err := fp.Download(context.Background(), rel, &dst, nil)
	if err == nil {
		t.Fatal("主源已推进到更新版本时不得作为回退地址")
	}
	if !strings.Contains(err.Error(), "0.2.12") || !strings.Contains(err.Error(), "0.2.11") {
		t.Errorf("错误应说明版本不一致：%v", err)
	}
	if fx.primary.artifactHit.Load() != 0 {
		t.Errorf("不得下载主源其他版本的制品，实际命中 %d 次", fx.primary.artifactHit.Load())
	}
	if dst.Len() != 0 {
		t.Errorf("失败后目标必须为空，实际 %d 字节", dst.Len())
	}
}

// --- 构造与纯函数 ---

func TestNewUpdateProvider_NoMirrorKeepsOriginalBehavior(t *testing.T) {
	prov, err := newUpdateProvider(updateProviderConfig{
		manifestURL: testPrimaryManifest,
		log:         (&logRecorder{}).logf,
	})
	if err != nil {
		t.Fatalf("newUpdateProvider: %v", err)
	}
	if _, isEndpoint := prov.(*endpoint.Provider); !isEndpoint {
		t.Fatalf("镜像未配置时应返回原版 endpoint Provider，实际 %T", prov)
	}
	if prov.Name() != "endpoint" {
		t.Errorf("Name = %q, want endpoint（镜像未配置保持原行为）", prov.Name())
	}
}

func TestNewUpdateProvider_InvalidMirrorFallsBackToPrimary(t *testing.T) {
	logs := &logRecorder{}
	prov, err := newUpdateProvider(updateProviderConfig{
		manifestURL:   testPrimaryManifest,
		mirrorBaseURL: "https://mirror.example.com/bad path", // 含空白
		log:           logs.logf,
	})
	if err != nil {
		t.Fatalf("newUpdateProvider: %v", err)
	}
	if prov.Name() != "endpoint" {
		t.Errorf("Name = %q, want endpoint（镜像配置非法时回退主源）", prov.Name())
	}
	if !logs.contains("镜像更新源配置无效") {
		t.Errorf("应记录镜像配置无效日志：%v", logs.line)
	}
}

func TestNewUpdateProvider_MirrorOnly(t *testing.T) {
	prov, err := newUpdateProvider(updateProviderConfig{
		manifestURL:   "",
		mirrorBaseURL: testMirrorBase,
		log:           (&logRecorder{}).logf,
	})
	if err != nil {
		t.Fatalf("newUpdateProvider: %v", err)
	}
	if prov.Name() != failoverProviderName {
		t.Errorf("Name = %q, want %q", prov.Name(), failoverProviderName)
	}
	fp := prov.(*failoverProvider)
	if len(fp.sources) != 1 || fp.primary != nil || fp.mirror == nil {
		t.Errorf("sources=%d primary=%v mirror=%v, want 仅镜像", len(fp.sources), fp.primary != nil, fp.mirror != nil)
	}
	if fp.sources[0].provider == nil {
		t.Error("镜像来源必须带 Check")
	}
}

func TestNewUpdateProvider_SetsIndependentShortTimeouts(t *testing.T) {
	prov, err := newUpdateProvider(updateProviderConfig{
		manifestURL:   testPrimaryManifest,
		mirrorBaseURL: testMirrorBase,
		log:           (&logRecorder{}).logf,
	})
	if err != nil {
		t.Fatalf("newUpdateProvider: %v", err)
	}
	fp := prov.(*failoverProvider)
	for _, src := range fp.sources {
		if src.timeout <= 0 || src.timeout >= updateCheckTimeout {
			t.Errorf("来源 %s 超时 = %v，必须大于 0 且小于整体预算 %v",
				src.name, src.timeout, updateCheckTimeout)
		}
	}
	// 下载尝试上限与检查预算是不同维度的预算：只要求取到生产默认值
	if fp.downloadAttemptTimeout != updateDownloadAttemptTimeout {
		t.Errorf("下载尝试上限 = %v, want 默认 %v", fp.downloadAttemptTimeout, updateDownloadAttemptTimeout)
	}
}

func TestNormalizeMirrorBaseURL(t *testing.T) {
	cases := []struct {
		in      string
		want    string
		wantErr bool
	}{
		{in: "", want: ""},
		{in: "  https://mirror.example.com  ", want: "https://mirror.example.com"},
		{in: "https://mirror.example.com/frpdesk", want: "https://mirror.example.com/frpdesk"},
		{in: "https://mirror.example.com/", wantErr: true},
		{in: "ftp://mirror.example.com", wantErr: true},
		{in: "mirror.example.com", wantErr: true},
		{in: "https://mirror.example.com/?x=1", wantErr: true},
		{in: "https://mirror.example.com/#f", wantErr: true},
		{in: "https://mirror exam.com", wantErr: true},
		{in: `https://mirror.example.com\a`, wantErr: true},
	}
	for _, tc := range cases {
		got, err := normalizeMirrorBaseURL(tc.in)
		if tc.wantErr {
			if err == nil {
				t.Errorf("normalize(%q) = %q, want error", tc.in, got)
			}
			continue
		}
		if err != nil {
			t.Errorf("normalize(%q) 意外出错：%v", tc.in, err)
			continue
		}
		if got != tc.want {
			t.Errorf("normalize(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

func TestMirrorArtifactURL(t *testing.T) {
	logs := &logRecorder{}
	fp := &failoverProvider{
		mirror: &updateSource{name: "mirror", baseURL: testMirrorBase, isMirror: true},
		log:    logs.logf,
	}
	got, err := fp.mirrorArtifactURL("0.2.11", testArtifactName)
	if err != nil {
		t.Fatalf("mirrorArtifactURL: %v", err)
	}
	if want := testMirrorBase + "/releases/0.2.11/" + testArtifactName; got != want {
		t.Errorf("url = %q, want %q", got, want)
	}
	if _, err := fp.mirrorArtifactURL("../evil", testArtifactName); err == nil {
		t.Error("版本号含 .. 必须拒绝")
	}
	if _, err := fp.mirrorArtifactURL("0.2.11", "sub/dir.zip"); err == nil {
		t.Error("文件名含路径必须拒绝")
	}
}

// --- 装配：attach 里的 Provider 选择 ---

func TestAttachWithMirrorUsesFailoverProvider(t *testing.T) {
	logs := &logRecorder{}
	em := &emitRecorder{}
	c := newUpdaterControllerWithSources(logs.logf, testPrimaryManifest, testMirrorBase)
	t.Cleanup(c.cancel)
	fw := &fakeFW{}
	attachReal(c, fw, em, "1.2.3")

	if fw.initCalled != 1 {
		t.Fatalf("Init 调用 %d 次, want 1", fw.initCalled)
	}
	if len(fw.initCfg.Providers) != 1 {
		t.Fatalf("Providers 数量 = %d, want 1（合并 Provider）", len(fw.initCfg.Providers))
	}
	if got := fw.initCfg.Providers[0].Name(); got != failoverProviderName {
		t.Errorf("Provider = %q, want %q", got, failoverProviderName)
	}
	if !logs.contains(testMirrorBase) {
		t.Errorf("就绪日志应包含镜像地址：%v", logs.line)
	}
}

func TestAttachMirrorOnlyStillInitializes(t *testing.T) {
	logs := &logRecorder{}
	em := &emitRecorder{}
	c := newUpdaterControllerWithSources(logs.logf, "", testMirrorBase)
	t.Cleanup(c.cancel)
	fw := &fakeFW{}
	attachReal(c, fw, em, "1.2.3")

	if fw.initCalled != 1 {
		t.Fatalf("仅镜像时也应 Init，实际 %d 次", fw.initCalled)
	}
	if got := fw.initCfg.Providers[0].Name(); got != failoverProviderName {
		t.Errorf("Provider = %q, want %q", got, failoverProviderName)
	}
}

func TestCompareVersions(t *testing.T) {
	cases := []struct {
		a, b string
		want int
	}{
		{"0.2.10", "0.2.11", -1},
		{"0.2.11", "0.2.10", 1},
		{"0.2.11", "0.2.11", 0},
		{"v0.2.11", "0.2.11", 0},
		{"0.10.0", "0.9.9", 1},
		{"1.0.0", "0.99.99", 1},
		{"abc", "0.0.1", -1},
	}
	for _, tc := range cases {
		if got := compareVersions(tc.a, tc.b); got != tc.want {
			t.Errorf("compareVersions(%q, %q) = %d, want %d", tc.a, tc.b, got, tc.want)
		}
	}
}
