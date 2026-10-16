package updatecleanup

// updatecleanup 单元测试：tempDir + 注入 Remove/短延迟，不真实删除用户文件。
// 覆盖：精准匹配与真正删除（含目录/symlink/其他 EXE/非数字后缀不动）、
// 被占用首次失败稍后成功、重试上限、等待期取消。

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

// logRecorder 收集日志（level|内容）。
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

func (l *logRecorder) count(sub string) int {
	l.mu.Lock()
	defer l.mu.Unlock()
	n := 0
	for _, s := range l.line {
		if strings.Contains(s, sub) {
			n++
		}
	}
	return n
}

// touch 在 dir 下创建普通文件。
func touch(t *testing.T, dir, name string) string {
	t.Helper()
	p := filepath.Join(dir, name)
	if err := os.WriteFile(p, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	return p
}

// ls 列出 dir 下全部目录项名（排序后逗号拼接，便于断言）。
func ls(t *testing.T, dir string) string {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	names := make([]string, 0, len(entries))
	for _, e := range entries {
		names = append(names, e.Name())
	}
	return strings.Join(names, ",")
}

// --- 精准匹配矩阵 ---

func TestIsStaleResidue(t *testing.T) {
	cases := []struct {
		name    string
		exeBase string
		want    bool
	}{
		{"frpdesk.exe.old.1739482938123456789", "frpdesk.exe", true}, // helper 的 UnixNano 后缀
		{"frpdesk.exe.old.0", "frpdesk.exe", true},
		{"app[1].exe.old.42", "app[1].exe", true},     // 文件名带方括号：QuoteMeta 后精准命中
		{"frpdesk.exe", "frpdesk.exe", false},         // 当前 EXE 本身绝不匹配
		{"frpdesk.exe.old", "frpdesk.exe", false},     // 缺数字后缀
		{"frpdesk.exe.old.", "frpdesk.exe", false},    // 空后缀
		{"frpdesk.exe.old.12a", "frpdesk.exe", false}, // 非纯数字
		{"frpdesk.exe.old.1.2", "frpdesk.exe", false}, // 多段后缀
		{"frpdesk.exe.old.０９", "frpdesk.exe", false},  // 全角数字（非 ASCII）
		{"frpdesk.exe.old.123.bak", "frpdesk.exe", false},
		{"frpdesk.exe.bak", "frpdesk.exe", false},
		{"other.exe.old.123", "frpdesk.exe", false}, // 其他 EXE 的残留
		{"frpdesk.exe.old.1234", "frpdesk.exe.old", false},
		{"", "frpdesk.exe", false},
		{"frpdesk.exe.old.123", "", false}, // exeBase 空：一律不匹配
	}
	for _, tc := range cases {
		if got := IsStaleResidue(tc.name, tc.exeBase); got != tc.want {
			t.Errorf("IsStaleResidue(%q, %q) = %v, want %v", tc.name, tc.exeBase, got, tc.want)
		}
	}
}

// --- 精准匹配与真正删除 ---

func TestRunDeletesOnlyExactMatches(t *testing.T) {
	dir := t.TempDir()
	keep := []string{
		"frpdesk.exe",          // 当前 EXE
		"frpdesk.exe.old.abc",  // 非数字后缀
		"frpdesk.exe.old",      // 无后缀
		"frpdesk.exe.bak",      // .bak
		"other.exe.old.7",      // 别的 EXE 残留
		"frpdesk.exe.old.1.2",  // 多段后缀
		"subfrpdesk.exe.old.7", // 前缀撞名（非精准匹配）
	}
	for _, n := range keep {
		touch(t, dir, n)
	}
	// 名字匹配但是目录/symlink：绝不删除
	if err := os.Mkdir(filepath.Join(dir, "frpdesk.exe.old.111"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(dir, "frpdesk.exe"), filepath.Join(dir, "frpdesk.exe.old.222")); err != nil {
		t.Fatal(err)
	}
	// 待删除的真残留
	stale := touch(t, dir, "frpdesk.exe.old.1739482938123456789")

	logs := &logRecorder{}
	removed := 0
	Run(context.Background(), Options{
		Dir: dir, ExeBase: "frpdesk.exe",
		MaxAttempts: 2, Delay: time.Millisecond,
		Remove: func(string) error { removed++; return nil }, // 不真删，靠目录清点验证
		Log:    logs.logf,
	})

	// 精准命中的常规文件且 Remove 成功：目录内容不变（注入 Remove 未真删），
	// 但删除只针对唯一残留，且无失败/重试日志
	if removed != 1 {
		t.Errorf("删除调用次数 = %d, want 1（仅精准匹配的常规残留）", removed)
	}
	if logs.contains("failed") {
		t.Errorf("不应有失败日志：%v", logs.line)
	}
	if !logs.contains("cleanup finished") {
		t.Errorf("应记完成日志：%v", logs.line)
	}
	_ = stale
	if got := ls(t, dir); !strings.Contains(got, "frpdesk.exe.old.111") || !strings.Contains(got, "frpdesk.exe.old.222") {
		t.Errorf("目录/符号链接不应被动：%s", got)
	}
}

// 注入 os.Remove 的端到端小验证：真正删除匹配项、保留其余。
func TestRunRealRemove(t *testing.T) {
	dir := t.TempDir()
	stale := touch(t, dir, "frpdesk.exe.old.99")
	keep := touch(t, dir, "frpdesk.exe")
	touch(t, dir, "frpdesk.exe.old.xx")

	Run(context.Background(), Options{
		Dir: dir, ExeBase: "frpdesk.exe",
		MaxAttempts: 2, Delay: time.Millisecond, Log: nil, // Log nil：静默路径也应工作
	})

	if _, err := os.Stat(stale); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("残留应被真正删除，stat err = %v", err)
	}
	if _, err := os.Stat(keep); err != nil {
		t.Errorf("当前 EXE 不应被动：%v", err)
	}
}

// --- 被占用：首次失败、稍后成功 ---

func TestRunRetryOccupiedThenSuccess(t *testing.T) {
	dir := t.TempDir()
	stale := touch(t, dir, "frpdesk.exe.old.5")

	var mu sync.Mutex
	calls := 0
	logs := &logRecorder{}
	Run(context.Background(), Options{
		Dir: dir, ExeBase: "frpdesk.exe",
		MaxAttempts: 6, Delay: time.Millisecond,
		Remove: func(string) error {
			mu.Lock()
			defer mu.Unlock()
			calls++
			if calls == 1 {
				return errors.New("process cannot access the file (模拟被占用)") // 首次被占用
			}
			return os.Remove(stale) // 重试成功：真正删掉
		},
		Log: logs.logf,
	})

	mu.Lock()
	n := calls
	mu.Unlock()
	if n != 2 {
		t.Errorf("Remove 调用次数 = %d, want 2（首次失败 + 重试成功后完成即停）", n)
	}
	if _, err := os.Stat(stale); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("重试后残留应被真正删除，stat err = %v", err)
	}
	if !logs.contains("Failed to remove some update residue") || !logs.contains("cleanup finished") {
		t.Errorf("应记失败与完成日志：%v", logs.line)
	}
}

// --- 重试上限 ---

func TestRunRetryLimit(t *testing.T) {
	dir := t.TempDir()
	touch(t, dir, "frpdesk.exe.old.5")
	touch(t, dir, "frpdesk.exe.old.6")

	var mu sync.Mutex
	calls := 0
	logs := &logRecorder{}
	Run(context.Background(), Options{
		Dir: dir, ExeBase: "frpdesk.exe",
		MaxAttempts: 3, Delay: time.Millisecond,
		Remove: func(string) error {
			mu.Lock()
			defer mu.Unlock()
			calls++
			return errors.New("被占用")
		},
		Log: logs.logf,
	})

	mu.Lock()
	n := calls
	mu.Unlock()
	if n != 6 { // 3 次尝试 × 2 个残留
		t.Errorf("Remove 调用次数 = %d, want 6（恰好 3 轮尝试后停止）", n)
	}
	if got := logs.count("retry limit reached"); got != 1 {
		t.Errorf("最终失败 warn 日志条数 = %d, want 1：%v", got, logs.line)
	}
}

// --- 等待期取消（应用退出） ---

func TestRunCancelDuringWait(t *testing.T) {
	dir := t.TempDir()
	touch(t, dir, "frpdesk.exe.old.5")

	var mu sync.Mutex
	calls := 0
	logs := &logRecorder{}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		defer close(done)
		Run(ctx, Options{
			Dir: dir, ExeBase: "frpdesk.exe",
			MaxAttempts: 6, Delay: 10 * time.Second, // 首次等待很长：取消必然落在等待期
			Remove: func(string) error {
				mu.Lock()
				defer mu.Unlock()
				calls++
				return nil
			},
			Log: logs.logf,
		})
	}()
	time.Sleep(20 * time.Millisecond) // 确保 Run 已进入首次等待
	cancel()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("取消后 Run 未返回")
	}
	mu.Lock()
	n := calls
	mu.Unlock()
	if n != 0 {
		t.Errorf("取消后不得发起删除，Remove 调用次数 = %d", n)
	}
	if !logs.contains("canceled") {
		t.Errorf("应记取消日志：%v", logs.line)
	}
}

func TestRunScanFailureRetriesWithoutReportingSuccess(t *testing.T) {
	logs := &logRecorder{}
	Run(context.Background(), Options{
		Dir: filepath.Join(t.TempDir(), "missing"), ExeBase: "frpdesk.exe",
		MaxAttempts: 3, Delay: time.Millisecond, Log: logs.logf,
	})
	if logs.count("Failed to scan update residue") != 3 || logs.count("retry limit") != 1 {
		t.Fatalf("扫描失败应有界重试：%v", logs.line)
	}
	if logs.contains("finished") {
		t.Fatalf("扫描失败不得报告清理成功：%v", logs.line)
	}
}
