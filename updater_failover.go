package main

// 更新源故障转移：镜像优先、主源兜底（应用层包装，不改 third_party）。
//
// 背景：框架原版 Check 会按 provider 链短路——镜像清单"可访问但无更新"会直接
// 终止检查，主源不会再被查询。因此这里把镜像与主源合并进同一个 Provider：
//   - Check：并发查询所有来源并合并结果。取版本最高者；同版本以主源清单为准
//     （摘要/元数据权威），镜像仍作为下载首选。任一来源确认无更新即按无更新处理，
//     网络错误不阻塞另一来源给出的升级提示。
//   - Download：先试镜像 releases/<version>/<filename>，失败换主源同版本地址，
//     再不行则实时查询主源清单补地址；全程只安装待确认的同一版本与摘要。
//   - 半途失败不写目标文件：先落独立临时文件并校验摘要，成功后才一次性提交 dst；
//     提交前复查 ctx。每个候选有独立尝试上限，body 挂起到时换源；
//     父 ctx 取消随时生效且不再换源。各清单来源也有独立且小于整体预算的短超时：
//     主源挂起不会把镜像已得结果拖成整体失败。
//
// 只在配置了 UPDATE_MIRROR_BASE_URL 时启用；未配置时保持原版单一 endpoint Provider。

import (
	"bytes"
	"context"
	"crypto/sha256"
	"crypto/sha512"
	"errors"
	"fmt"
	"hash"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"path"
	"runtime"
	"strconv"
	"strings"
	"time"

	"github.com/wailsapp/wails/v3/pkg/updater"
	"github.com/wailsapp/wails/v3/pkg/updater/providers/endpoint"
)

const (
	// failoverProviderName 框架用它在 pending release 上路由 Download。
	failoverProviderName = "endpoint-mirror"

	// updateManifestAttemptTimeout 单个清单来源（镜像/主源）各自独立的请求上限：
	// 必须明显小于整体检查预算（30s），确保任一来源挂起时：
	//   1) 不占满整体预算；
	//   2) 其他来源（尤其镜像）已得结果不被拖成整体失败。
	// 用户显式取消（父 ctx）不受此限制，立即生效。
	updateManifestAttemptTimeout = 5 * time.Second

	// updateDownloadAttemptTimeout 单个来源的下载整体上限：headers 成功后 body
	// 挂起不得无限等待，到时失败换下一来源。数分钟足够覆盖正常安装包传输；
	// 父 ctx 取消仍立即生效（不受此上限影响）。
	updateDownloadAttemptTimeout = 5 * time.Minute

	// updatePrimaryLookupTimeout 下载前实时查询主源清单（补齐回退地址）的上限。
	updatePrimaryLookupTimeout = 10 * time.Second

	// release 元数据键：记录待安装版本在各来源的下载地址（仅内存，不落盘）。
	metaMirrorURL  = "frpdesk.mirror.url"
	metaPrimaryURL = "frpdesk.primary.url"
)

// manifestChecker 是 Check 能力面：生产用 endpoint.Provider，单测可桩替。
type manifestChecker interface {
	Check(ctx context.Context, req updater.CheckRequest) (*updater.Release, error)
}

// updateSource 一个清单来源。
type updateSource struct {
	name     string          // "mirror" / "primary"，仅用于日志与选源
	provider manifestChecker // 清单检查（endpoint）
	isMirror bool
	baseURL  string        // 仅镜像：公开根地址（无尾斜杠），用于拼制品路径
	timeout  time.Duration // 单次清单请求上限；0 表示跟随父 ctx（仅测试用）
}

// failoverProvider 合并镜像与主源的 updater.Provider 实现。
type failoverProvider struct {
	sources                []updateSource // 顺序即优先级：镜像在前
	mirror                 *updateSource
	primary                *updateSource
	client                 *http.Client
	downloadAttemptTimeout time.Duration // 单来源下载整体上限；0 用默认常量
	log                    func(level, format string, args ...any)
}

// updateProviderConfig 构造更新源所需配置。
type updateProviderConfig struct {
	manifestURL   string
	mirrorBaseURL string
	log           func(level, format string, args ...any)
}

// newUpdateProvider 构建更新源：镜像未配置（或配置非法）时返回与原版一致的单一
// endpoint Provider；配置镜像时返回镜像优先、主源兜底的合并 Provider。
func newUpdateProvider(cfg updateProviderConfig) (updater.Provider, error) {
	manifestURL := strings.TrimSpace(cfg.manifestURL)
	mirrorBase, mirrorErr := normalizeMirrorBaseURL(cfg.mirrorBaseURL)
	if mirrorErr != nil {
		cfg.log("error", "[update] Invalid mirror source config; ignoring mirror and falling back to primary: %v", mirrorErr)
	}

	if mirrorBase == "" {
		// 原行为：单一主源，不使用自定义 HTTP 客户端与超时。
		return endpoint.New(endpoint.Config{URL: manifestURL})
	}

	client := newUpdateHTTPClient()
	p := &failoverProvider{client: client, log: cfg.log}

	mp, err := endpoint.New(endpoint.Config{URL: mirrorBase + "/stable.json", HTTPClient: client})
	if err != nil {
		return nil, fmt.Errorf("failed to initialize mirror update source: %w", err)
	}
	p.sources = append(p.sources, updateSource{
		name:     "mirror",
		provider: mp,
		isMirror: true,
		baseURL:  mirrorBase,
		timeout:  updateManifestAttemptTimeout,
	})

	if manifestURL != "" {
		pp, err := endpoint.New(endpoint.Config{URL: manifestURL, HTTPClient: client})
		if err != nil {
			return nil, fmt.Errorf("failed to initialize primary update source: %w", err)
		}
		p.sources = append(p.sources, updateSource{name: "primary", provider: pp, timeout: updateManifestAttemptTimeout})
	}

	// 全部 append 完成后取地址，避免切片扩容后指针失效；之后不再修改 sources。
	p.mirror = &p.sources[0]
	if len(p.sources) > 1 {
		p.primary = &p.sources[1]
	}
	p.downloadAttemptTimeout = updateDownloadAttemptTimeout
	return p, nil
}

// normalizeMirrorBaseURL 校验并规范化镜像根地址：
// 必须是 http(s) URL、带主机名、无查询/片段、无尾斜杠、无空白或引号
// （空白/引号会破坏构建时 ldflags 参数）。
func normalizeMirrorBaseURL(raw string) (string, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return "", nil
	}
	if strings.ContainsAny(raw, " \t\r\n\"'\\") {
		return "", errors.New("mirror base URL cannot contain whitespace or quote characters")
	}
	u, err := url.Parse(raw)
	if err != nil {
		return "", fmt.Errorf("failed to parse the mirror base URL: %w", err)
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return "", fmt.Errorf("mirror base URL must be an http(s) URL")
	}
	if u.Host == "" {
		return "", fmt.Errorf("mirror base URL has no host")
	}
	if u.RawQuery != "" || u.Fragment != "" {
		return "", fmt.Errorf("mirror base URL must not contain a query or fragment")
	}
	if strings.HasSuffix(raw, "/") {
		return "", fmt.Errorf("mirror base URL must not end with '/'")
	}
	return raw, nil
}

// newUpdateHTTPClient 更新下载专用 HTTP 客户端：
// 连接/头部阶段有超时，整体不设总超时（大安装包传输不因总时限被误杀）；
// 禁用透明 gzip，避免传输层改写字节影响摘要与进度。
func newUpdateHTTPClient() *http.Client {
	return &http.Client{
		Transport: &http.Transport{
			Proxy:                 http.ProxyFromEnvironment,
			DialContext:           (&net.Dialer{Timeout: 5 * time.Second, KeepAlive: 30 * time.Second}).DialContext,
			TLSHandshakeTimeout:   10 * time.Second,
			ResponseHeaderTimeout: 30 * time.Second,
			ExpectContinueTimeout: 1 * time.Second,
			DisableCompression:    true,
			MaxIdleConns:          4,
		},
	}
}

func (p *failoverProvider) Name() string { return failoverProviderName }

// --- Check ---

type sourceCheckResult struct {
	src updateSource
	rel *updater.Release
	err error
}

// Check 并发查询所有来源，合并出待安装版本。context 取消时立即返回。
func (p *failoverProvider) Check(ctx context.Context, req updater.CheckRequest) (*updater.Release, error) {
	// 结果通道容量等于来源数：即使提前返回，goroutine 也不会阻塞泄漏。
	results := make(chan sourceCheckResult, len(p.sources))
	for _, src := range p.sources {
		go func(src updateSource) {
			rel, err := p.checkSource(ctx, src, req)
			results <- sourceCheckResult{src: src, rel: rel, err: err}
		}(src)
	}

	got := make([]sourceCheckResult, 0, len(p.sources))
	for range p.sources {
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case r := <-results:
			got = append(got, r)
		}
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	return p.merge(got)
}

// checkSource 单来源检查；来源自带超时时只影响该来源，父 ctx 取消优先上报。
func (p *failoverProvider) checkSource(parent context.Context, src updateSource, req updater.CheckRequest) (*updater.Release, error) {
	ctx := parent
	if src.timeout > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(parent, src.timeout)
		defer cancel()
	}
	rel, err := src.provider.Check(ctx, req)
	if err != nil && parent.Err() != nil {
		// 整体取消：不把单个来源的错误混进正常回退流程
		return nil, parent.Err()
	}
	return rel, err
}

// merge 合并各来源结果：版本最高者胜出，同版本以主源清单为准；
// 至少一个来源确认无更新即按无更新处理，其余来源的网络错误不阻塞升级提示。
func (p *failoverProvider) merge(got []sourceCheckResult) (*updater.Release, error) {
	var (
		releases    []sourceCheckResult
		failures    []error
		anyUpToDate bool
	)
	for _, r := range got {
		switch {
		case r.err != nil:
			failures = append(failures, fmt.Errorf("%s: %w", r.src.name, r.err))
			p.log("warn", "[update] %s source check failed: %v", r.src.name, r.err)
		case r.rel == nil:
			anyUpToDate = true
			p.log("info", "[update] %s source: no new version", r.src.name)
		default:
			releases = append(releases, r)
			p.log("info", "[update] %s source: found version %s", r.src.name, r.rel.Version)
		}
	}
	if len(releases) == 0 {
		if anyUpToDate {
			return nil, nil
		}
		return nil, fmt.Errorf("update check failed: %w", errors.Join(failures...))
	}

	chosen := chooseRelease(releases)
	p.attachCandidates(chosen, got)
	return chosen.rel, nil
}

// chooseRelease 选版本最高的来源；同版本时主源优先（清单权威），
// 镜像仍通过下载候选顺序获得优先。
func chooseRelease(cands []sourceCheckResult) sourceCheckResult {
	best := cands[0]
	for _, r := range cands[1:] {
		switch cmp := compareVersions(r.rel.Version, best.rel.Version); {
		case cmp > 0:
			best = r
		case cmp == 0 && best.src.isMirror && !r.src.isMirror:
			best = r
		}
	}
	return best
}

// attachCandidates 把各来源的下载候选写入 release 元数据（仅内存）：
// 顺序是镜像在前、主源在后；主源地址只在版本与制品完全一致时才采纳。
func (p *failoverProvider) attachCandidates(chosen sourceCheckResult, got []sourceCheckResult) {
	rel := chosen.rel
	filename := rel.Artifact.Filename
	if filename == "" {
		filename = filenameFromArtifactURL(artifactURL(rel))
	}

	mirrorURL := ""
	if p.mirror != nil {
		u, err := p.mirrorArtifactURL(rel.Version, filename)
		if err != nil {
			p.log("warn", "[update] Failed to build the mirror download URL: %v", err)
		} else {
			mirrorURL = u
		}
	}

	primaryURL := ""
	for _, r := range got {
		if r.err != nil || r.rel == nil || r.src.isMirror {
			continue
		}
		if !sameArtifact(rel, r.rel) {
			p.log("warn", "[update] Primary manifest version %s artifact differs from the chosen %s; not used as the fallback URL",
				r.rel.Version, rel.Version)
			continue
		}
		if u := artifactURL(r.rel); u != "" {
			primaryURL = u
		}
	}

	if rel.Metadata == nil {
		rel.Metadata = map[string]any{}
	}
	rel.Metadata[metaMirrorURL] = mirrorURL
	rel.Metadata[metaPrimaryURL] = primaryURL
	p.log("info", "[update] Version %s download order: mirror=%t primary=%t", rel.Version, mirrorURL != "", primaryURL != "")
}

// mirrorArtifactURL 按镜像目录约定拼制品地址：<base>/releases/<version>/<filename>。
func (p *failoverProvider) mirrorArtifactURL(version, filename string) (string, error) {
	if p.mirror == nil || p.mirror.baseURL == "" {
		return "", errors.New("mirror source is not configured")
	}
	if version == "" || strings.ContainsAny(version, "/\\") || strings.Contains(version, "..") {
		return "", fmt.Errorf("version %q cannot be used in a mirror path", version)
	}
	if filename == "" || filename != path.Base(filename) || strings.ContainsAny(filename, "/\\") {
		return "", fmt.Errorf("artifact filename %q cannot be used in a mirror path", filename)
	}
	return p.mirror.baseURL + "/releases/" + url.PathEscape(version) + "/" + url.PathEscape(filename), nil
}

// sameArtifact 判定两个来源的 release 是否是同一待安装制品（版本/平台/架构/文件名）。
func sameArtifact(a, b *updater.Release) bool {
	if a == nil || b == nil || a.Version != b.Version {
		return false
	}
	if a.Artifact.Filename != "" && b.Artifact.Filename != "" && a.Artifact.Filename != b.Artifact.Filename {
		return false
	}
	if a.Artifact.Platform != "" && b.Artifact.Platform != "" && a.Artifact.Platform != b.Artifact.Platform {
		return false
	}
	if a.Artifact.Arch != "" && b.Artifact.Arch != "" && a.Artifact.Arch != b.Artifact.Arch {
		return false
	}
	return true
}

// --- Download ---

type downloadCandidate struct {
	name string
	url  string
}

// candidatesFromMetadata 按优先级取出下载候选：镜像 → 主源 → release 自带地址，
// 去重后返回。候选都来自同一 release 的元数据，不会引入其他版本。
func candidatesFromMetadata(rel *updater.Release) []downloadCandidate {
	seen := make(map[string]bool)
	var out []downloadCandidate
	add := func(name, u string) {
		if u == "" || seen[u] {
			return
		}
		seen[u] = true
		out = append(out, downloadCandidate{name: name, url: u})
	}
	add("mirror", metadataString(rel, metaMirrorURL))
	add("primary", metadataString(rel, metaPrimaryURL))
	add("manifest", artifactURL(rel))
	return out
}

// Download 逐个尝试候选地址；网络/HTTP/摘要失败才换源，本地写失败立即停止。
// 下载先落独立临时文件，校验摘要后一次性提交 dst，绝不把两个来源的字节拼接。
// Download 逐个尝试候选地址；网络/HTTP/摘要失败才换源，本地写失败立即停止。
// 下载先落独立临时文件，校验摘要后一次性提交 dst，绝不把两个来源的字节拼接。
// 每个候选有独立尝试上限（body 挂起也能到时失败换源）；父 ctx 取消立即生效且不再换源。
func (p *failoverProvider) Download(ctx context.Context, rel *updater.Release, dst io.Writer, onProgress func(int64, int64)) error {
	if rel == nil {
		return errors.New("update package info is missing")
	}
	total := rel.Artifact.Size
	var lastErr error

	for _, cand := range candidatesFromMetadata(rel) {
		if err := ctx.Err(); err != nil {
			return err
		}
		attemptCtx, cancel := context.WithTimeout(ctx, p.attemptTimeout())
		err := p.fetchToTempAndCommit(attemptCtx, cand, rel, dst, total, onProgress)
		cancel()
		if err == nil {
			return nil
		}
		if ctx.Err() != nil {
			// 父 ctx 取消/截止：立即终止，不再换源
			return ctx.Err()
		}
		if isLocalIOError(err) {
			// 本地临时文件/写入失败与来源无关，改试其他来源没有意义
			return err
		}
		lastErr = err
		p.log("warn", "[update] Download from %s failed: %v (trying remaining sources)", cand.name, err)
	}

	// 主源候选缺失（检查期主源不可用/无更新/不同版本）时，实时查询主源清单，
	// 严格限定同一版本与制品；不等于“重新检查后跳版本”。
	if metadataString(rel, metaPrimaryURL) == "" && p.primary != nil {
		if err := ctx.Err(); err != nil {
			return err
		}
		lookupCtx, cancel := context.WithTimeout(ctx, updatePrimaryLookupTimeout)
		u, lerr := p.lookupPrimaryArtifactURL(lookupCtx, rel)
		cancel()
		if lerr != nil {
			// 查询失败或版本不一致：原因并入最终错误，便于用户/日志排查
			p.log("warn", "[update] Failed to look up the primary fallback URL: %v", lerr)
			lastErr = errors.Join(lastErr, fmt.Errorf("failed to look up the primary fallback URL: %w", lerr))
		} else if u != "" {
			attemptCtx, cancel := context.WithTimeout(ctx, p.attemptTimeout())
			derr := p.fetchToTempAndCommit(attemptCtx, downloadCandidate{name: "primary", url: u}, rel, dst, total, onProgress)
			cancel()
			if derr == nil {
				return nil
			}
			if ctx.Err() != nil {
				return ctx.Err()
			}
			if isLocalIOError(derr) {
				return derr
			}
			lastErr = derr
		}
	}

	if lastErr == nil {
		lastErr = errors.New("no update package URL is available")
	}
	return fmt.Errorf("all update sources failed to download: %w", lastErr)
}

// attemptTimeout 单个来源的下载尝试上限（构造期已设默认，防御零值）。
func (p *failoverProvider) attemptTimeout() time.Duration {
	if p.downloadAttemptTimeout > 0 {
		return p.downloadAttemptTimeout
	}
	return updateDownloadAttemptTimeout
}

// fetchToTempAndCommit 从单个候选地址完整下载到临时文件：校验摘要通过后
// 一次性写入 dst。网络在途失败只丢弃临时文件，dst 不会被污染。
// ctx 为单次尝试 ctx（继承父 ctx，带独立尝试上限）：用户取消随时中止。
func (p *failoverProvider) fetchToTempAndCommit(
	ctx context.Context,
	cand downloadCandidate,
	rel *updater.Release,
	dst io.Writer,
	total int64,
	onProgress func(int64, int64),
) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, cand.url, nil)
	if err != nil {
		return fmt.Errorf("invalid download URL: %w", err)
	}
	req.Header.Set("Accept", "application/octet-stream")

	resp, err := p.client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("HTTP %d", resp.StatusCode)
	}
	if total <= 0 && resp.ContentLength > 0 {
		total = resp.ContentLength
	}

	tmp, err := os.CreateTemp("", "frpdesk-update-*.part")
	if err != nil {
		return &localIOError{fmt.Errorf("failed to create the temp file: %w", err)}
	}
	defer func() {
		_ = tmp.Close()
		_ = os.Remove(tmp.Name())
	}()

	hasher, err := newArtifactHasher(rel.Verification)
	if err != nil {
		return err
	}

	written := int64(0)
	buf := make([]byte, 64*1024)
	for {
		n, rerr := resp.Body.Read(buf)
		if n > 0 {
			if _, werr := tmp.Write(buf[:n]); werr != nil {
				return &localIOError{fmt.Errorf("failed to write the temp file: %w", werr)}
			}
			if hasher != nil {
				_, _ = hasher.Write(buf[:n])
			}
			written += int64(n)
			// 网络阶段推进进度；提交阶段不再回调（框架自身按写入 dst 的字节计数，
			// 再回调会重复累计，最终进度由框架 clamp 到 100%）。
			if onProgress != nil {
				onProgress(written, total)
			}
		}
		if rerr == io.EOF {
			break
		}
		if rerr != nil {
			return rerr
		}
		if err := ctx.Err(); err != nil {
			return err
		}
	}

	if hasher != nil && !bytes.Equal(hasher.Sum(nil), rel.Verification.Digest) {
		return fmt.Errorf("downloaded content digest does not match the manifest")
	}
	// 提交前复查 ctx：取消/尝试截止若发生在读取完成之后，也不得写 dst
	if err := ctx.Err(); err != nil {
		return err
	}
	if _, err := tmp.Seek(0, io.SeekStart); err != nil {
		return &localIOError{fmt.Errorf("failed to read the temp file: %w", err)}
	}
	// 一次性提交：若这里失败已无法回滚，直接失败，不再改试来源（避免拼接损坏）。
	if _, err := io.Copy(dst, tmp); err != nil {
		return &localIOError{fmt.Errorf("failed to write the update file: %w", err)}
	}
	return nil
}

// lookupPrimaryArtifactURL 实时查询主源清单，仅当版本/平台/架构/文件名与待安装
// 制品完全一致时返回其下载地址；主源已推进到更新版本时明确拒绝。
func (p *failoverProvider) lookupPrimaryArtifactURL(ctx context.Context, rel *updater.Release) (string, error) {
	if p.primary == nil {
		return "", nil
	}
	req := updater.CheckRequest{
		// 0.0.0 让主源返回其当前最新版本，再严格比对，绝不跨版本回退
		CurrentVersion: "0.0.0",
		Platform:       rel.Artifact.Platform,
		Arch:           rel.Artifact.Arch,
	}
	if req.Platform == "" {
		req.Platform = runtime.GOOS
	}
	if req.Arch == "" {
		req.Arch = runtime.GOARCH
	}

	cctx, cancel := context.WithTimeout(ctx, updatePrimaryLookupTimeout)
	defer cancel()
	got, err := p.primary.provider.Check(cctx, req)
	if err != nil {
		return "", err
	}
	if got == nil {
		return "", nil
	}
	if !sameArtifact(rel, got) {
		return "", fmt.Errorf("primary latest version %s differs from the version being installed (%s)", got.Version, rel.Version)
	}
	u := artifactURL(got)
	if u == "" {
		return "", errors.New("primary manifest has no download URL")
	}
	return u, nil
}

// --- 小工具 ---

// localIOError 本地文件读写失败：与下载来源无关，立即终止换源循环。
type localIOError struct{ err error }

func (e *localIOError) Error() string { return e.err.Error() }
func (e *localIOError) Unwrap() error { return e.err }

func isLocalIOError(err error) bool {
	var target *localIOError
	return errors.As(err, &target)
}

// newArtifactHasher 按清单摘要算法建流式哈希；无摘要（或未知算法）时返回 nil，
// 由框架在下载完成后统一校验。
func newArtifactHasher(v *updater.Verification) (hash.Hash, error) {
	if v == nil || len(v.Digest) == 0 {
		return nil, nil
	}
	switch v.DigestAlgo {
	case "", "sha256":
		return sha256.New(), nil
	case "sha512":
		return sha512.New(), nil
	default:
		return nil, nil
	}
}

func metadataString(rel *updater.Release, key string) string {
	if rel == nil || rel.Metadata == nil {
		return ""
	}
	s, _ := rel.Metadata[key].(string)
	return s
}

// artifactURL 取 endpoint provider 写入的清单下载地址。
func artifactURL(rel *updater.Release) string {
	return metadataString(rel, "endpoint.artifact.url")
}

func filenameFromArtifactURL(raw string) string {
	if raw == "" {
		return ""
	}
	u, err := url.Parse(raw)
	if err != nil {
		return ""
	}
	name := path.Base(u.Path)
	if name == "." || name == "/" {
		return ""
	}
	return name
}

// compareVersions 比较 x.y.z 三段数字版本；非数字段按 0 处理（不涉及预发布版本）。
func compareVersions(a, b string) int {
	pa, pb := parseVersionTriple(a), parseVersionTriple(b)
	for i := 0; i < 3; i++ {
		if pa[i] != pb[i] {
			if pa[i] < pb[i] {
				return -1
			}
			return 1
		}
	}
	return 0
}

func parseVersionTriple(v string) [3]int {
	v = strings.TrimPrefix(strings.TrimSpace(v), "v")
	parts := strings.Split(v, ".")
	var out [3]int
	for i := 0; i < 3 && i < len(parts); i++ {
		n, _ := strconv.Atoi(parts[i])
		out[i] = n
	}
	return out
}
