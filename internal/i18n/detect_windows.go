//go:build windows

package i18n

import "syscall"

// detectSystemLang Windows 系统语言探测：优先环境变量（终端/dev 场景），
// 否则取用户 UI 语言（GetUserDefaultUILanguage，直接 syscall，无新增依赖）。
func detectSystemLang() Lang {
	if l := langFromEnv(); l != "" {
		return l
	}
	proc := syscall.NewLazyDLL("kernel32.dll").NewProc("GetUserDefaultUILanguage")
	if err := proc.Find(); err == nil {
		langID, _, _ := proc.Call()
		if l := langFromUILanguageID(langID); l != "" {
			return l
		}
	}
	return EN
}
