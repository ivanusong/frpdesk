//go:build darwin

package i18n

import (
	"os/exec"
	"strings"
)

// detectSystemLang macOS 系统语言探测：优先环境变量（终端/dev 场景），
// GUI（Finder/launchd 启动）无 LANG 时回退 `defaults read -g AppleLocale`
// （读 cfprefsd，托盘常驻场景可靠）；仍失败再试 AppleLanguages 列表首项。
func detectSystemLang() Lang {
	if l := langFromEnv(); l != "" {
		return l
	}
	if out, err := exec.Command("defaults", "read", "-g", "AppleLocale").Output(); err == nil {
		if s := strings.TrimSpace(string(out)); s != "" {
			return NormalizeLang(s)
		}
	}
	if out, err := exec.Command("defaults", "read", "-g", "AppleLanguages").Output(); err == nil {
		// 形如 `( en,\n "zh-Hans-CN",\n ...)`：取首个语言 token
		s := strings.TrimSpace(string(out))
		s = strings.TrimPrefix(s, "(")
		if i := strings.IndexAny(s, ",)"); i >= 0 {
			s = s[:i]
		}
		if s = strings.Trim(strings.TrimSpace(s), "\""); s != "" {
			return NormalizeLang(s)
		}
	}
	return EN
}
