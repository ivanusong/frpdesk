package configstore

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"frpdesk/internal/i18n"
)

// resetLang 恢复进程语言为默认中文，避免污染同包其他用例的全局 i18n 状态。
func resetLang(t *testing.T) {
	t.Helper()
	t.Cleanup(func() { i18n.Set(i18n.ZH) })
}

// TestLanguagePersistence 语言偏好持久化：默认 system；显式选择可写入并重启加载；
// 旧配置（无 language 键）加载后 Language() 仍为 system，不破坏服务/服务器语义。
func TestLanguagePersistence(t *testing.T) {
	resetLang(t)
	path := filepath.Join(t.TempDir(), "config.json")

	// 旧格式配置：无 language 键
	legacy := `{"servers":[{"id":"a","name":"主","serverAddr":"a.com","serverPort":7000}],"services":[]}`
	if err := os.WriteFile(path, []byte(legacy), 0o644); err != nil {
		t.Fatal(err)
	}
	s := newStore(path)
	if got := s.Language(); got != i18n.PrefSystem {
		t.Fatalf("旧配置默认应为 system，得到 %q", got)
	}
	// 旧服务/服务器语义不受影响
	if len(s.Config().Servers) != 1 || s.Config().Servers[0].Name != "主" {
		t.Fatalf("旧配置语义被破坏: %+v", s.Config().Servers)
	}

	// 显式选择英文：落盘并可重启加载
	if err := s.SetLanguage(i18n.PrefEN); err != nil {
		t.Fatalf("SetLanguage: %v", err)
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(raw), `"language": "en"`) {
		t.Fatalf("language 应落盘: %s", raw)
	}
	s2 := newStore(path)
	if got := s2.Language(); got != i18n.PrefEN {
		t.Fatalf("重启加载应为 en，得到 %q", got)
	}

	// system 也是合法持久化值
	if err := s2.SetLanguage(i18n.PrefSystem); err != nil {
		t.Fatalf("SetLanguage(system): %v", err)
	}
	if got := newStore(path).Language(); got != i18n.PrefSystem {
		t.Fatalf("重启加载应为 system，得到 %q", got)
	}
}

// TestLanguageLoadSetsI18n 加载带 language 键的配置时同步进程语言
// （迁移生成的默认服务器名等加载期文案需要正确语言）。
func TestLanguageLoadSetsI18n(t *testing.T) {
	resetLang(t)
	path := filepath.Join(t.TempDir(), "config.json")
	cfg := `{"servers":[],"services":[],"language":"en"}`
	if err := os.WriteFile(path, []byte(cfg), 0o644); err != nil {
		t.Fatal(err)
	}
	_ = newStore(path)
	if i18n.Current() != i18n.EN {
		t.Fatalf("加载 language=en 后进程语言应为 en，得到 %v", i18n.Current())
	}
}

// TestLocalizedValidationErrors 表单校验错误双语：同一输入在 en 下返回英文消息。
func TestLocalizedValidationErrors(t *testing.T) {
	resetLang(t)
	s := newTestStore(t)
	i18n.Set(i18n.EN)
	_, err := s.CreateServer(ServerInput{Name: "  ", ServerAddr: "a.com"})
	if err == nil || err.Error() != "Server name is required" {
		t.Fatalf("en 校验消息不符合预期: %v", err)
	}
	i18n.Set(i18n.ZH)
	_, err = s.CreateServer(ServerInput{Name: "  ", ServerAddr: "a.com"})
	if err == nil || err.Error() != "服务器名称不能为空" {
		t.Fatalf("zh 校验消息不符合预期: %v", err)
	}
}
