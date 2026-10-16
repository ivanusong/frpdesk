package i18n

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// resetZH 测试结束恢复默认语言（进程级全局状态，避免污染其他用例）。
func resetZH(t *testing.T) {
	t.Helper()
	t.Cleanup(func() { Set(ZH) })
}

// TestDictCompleteness 词典完整性：每个 key 的 zh/en 都非空且含插值占位符对齐
// （zh 中出现的每个 {n} 必须也出现在 en，反之亦然，防止漏参数）。
func TestDictCompleteness(t *testing.T) {
	if len(dict) == 0 {
		t.Fatal("词典为空")
	}
	placeholders := func(s string) map[string]bool {
		m := map[string]bool{}
		for i := 0; i+2 < len(s); i++ {
			if s[i] == '{' && s[i+1] >= '0' && s[i+1] <= '9' && s[i+2] == '}' {
				m[s[i:i+3]] = true
			}
		}
		return m
	}
	for key, e := range dict {
		if strings.TrimSpace(e.zh) == "" {
			t.Errorf("key %q 缺少中文文案", key)
		}
		if strings.TrimSpace(e.en) == "" {
			t.Errorf("key %q 缺少英文文案", key)
		}
		zhP, enP := placeholders(e.zh), placeholders(e.en)
		for p := range zhP {
			if !enP[p] {
				t.Errorf("key %q 英文缺少占位符 %s（zh=%q en=%q）", key, p, e.zh, e.en)
			}
		}
		for p := range enP {
			if !zhP[p] {
				t.Errorf("key %q 中文缺少占位符 %s（zh=%q en=%q）", key, p, e.zh, e.en)
			}
		}
	}
}

// TestSourceKeysExist 源码 key 对齐：所有 i18n.T("key")、i18n.Err(err, "key") 与
// MessageKey: "key" 引用的 key 必须在 dict 中存在（防止新增文案时漏进词典，
// 运行时以 key 形式直接展示给用户）。扫描范围排除 third_party 与 frontend。
func TestSourceKeysExist(t *testing.T) {
	re := regexp.MustCompile(`(?:i18n\.T\(|MessageKey:\s*|i18n\.Err\([^,]+,\s*)"([a-z][A-Za-z]*\.[A-Za-z]+)"`)
	root := filepath.Join("..", "..")
	found := 0
	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if path == root {
			return nil // 相对路径 root 的 d.Name() 为 ".."，不参与点号目录跳过判定
		}
		if d.IsDir() {
			name := d.Name()
			if name == "third_party" || name == "frontend" || strings.HasPrefix(name, ".") {
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return nil
		}
		raw, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		for _, m := range re.FindAllStringSubmatch(string(raw), -1) {
			found++
			if _, ok := dict[m[1]]; !ok {
				t.Errorf("%s: key %q 不在词典中", path, m[1])
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if found == 0 {
		t.Fatal("未扫描到任何 key 引用，请检查扫描路径")
	}
}

// TestTAndFallback 取词与回退：中文直取、英文直取、未知 key 回退 key 本身。
func TestTAndFallback(t *testing.T) {
	resetZH(t)
	Set(ZH)
	if got := T(KeyTrayShow); got != "显示主窗口" {
		t.Fatalf("zh T = %q", got)
	}
	Set(EN)
	if got := T(KeyTrayShow); got != "Show Main Window" {
		t.Fatalf("en T = %q", got)
	}
	if got := T("key.not.exist"); got != "key.not.exist" {
		t.Fatalf("未知 key 应原样返回，got %q", got)
	}
}

// TestInterpolation 参数插值：{0}..{n} 替换；缺参占位符原样保留。
func TestInterpolation(t *testing.T) {
	resetZH(t)
	Set(ZH)
	got := T(KeyStateRunningN, 3)
	if got != "已连接 3" {
		t.Fatalf("插值 = %q", got)
	}
	if got := T(KeyStateFailedNM, 1); got != "失败 1/{1}" {
		t.Fatalf("缺参应保留占位符，got %q", got)
	}
	Set(EN)
	if got := T(KeyStateFailedNM, 1, 2); got != "Failed 1/2" {
		t.Fatalf("en 插值 = %q", got)
	}
}

// TestSetPreferenceNormalization 偏好规范化：zh 系→中文、en 系→英文、
// 未知/system/空→系统探测（本机语言不确定，只断言落入两种合法值）。
func TestSetPreferenceNormalization(t *testing.T) {
	resetZH(t)
	if l := SetPreference("zh-CN"); l != ZH {
		t.Fatalf("zh-CN → %v", l)
	}
	if l := SetPreference("en"); l != EN {
		t.Fatalf("en → %v", l)
	}
	for _, pref := range []string{"system", "", "fr-FR"} {
		if l := SetPreference(pref); l != ZH && l != EN {
			t.Fatalf("pref %q 应解析为合法语言，got %v", pref, l)
		}
	}
	if NormalizePref(" zh-CN ") != PrefZH || NormalizePref("fr") != PrefSystem {
		t.Fatal("NormalizePref 规范化不符合预期")
	}
	if !IsValidPref("system") || IsValidPref("fr") {
		t.Fatal("IsValidPref 判定不符合预期")
	}
}

// TestNormalizeLang locale 标签归一：zh 各形态→ZH，其他→EN。
func TestNormalizeLang(t *testing.T) {
	for _, s := range []string{"zh", "zh-CN", "zh_CN", "zh-Hans-CN@rg=zzzz", "zh-TW"} {
		if l := NormalizeLang(s); l != ZH {
			t.Fatalf("NormalizeLang(%q) = %v, want zh-CN", s, l)
		}
	}
	for _, s := range []string{"en", "en-US", "fr-FR", "ja-JP", "", "en@rg=zhzzzy"} {
		if l := NormalizeLang(s); l != EN {
			t.Fatalf("NormalizeLang(%q) = %v, want en", s, l)
		}
	}
}

// TestErrKeepsSentinel Err 包装保留哨兵身份（errors.Is），消息按当前语言渲染。
func TestErrKeepsSentinel(t *testing.T) {
	resetZH(t)
	sentinel := errors.New("updater busy")
	err := Err(sentinel, "err.updaterBusy")
	Set(ZH)
	if err.Error() != "已有更新操作在进行，请稍后再试" {
		t.Fatalf("zh message = %q", err.Error())
	}
	Set(EN)
	if err.Error() != "Another update operation is in progress; try again later" {
		t.Fatalf("en message = %q", err.Error())
	}
	if !errors.Is(err, sentinel) {
		t.Fatal("errors.Is 应命中哨兵错误")
	}
}
