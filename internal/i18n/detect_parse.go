package i18n

import (
	"os"
	"strings"
)

// 本文件集中「纯函数」语言解析：环境变量优先级、macOS defaults 输出解析、
// Windows LANGID 判定。不依赖平台调用，便于跨平台单测覆盖
// （平台探测代码见 detect_darwin.go / detect_windows.go / detect_other.go）。

// langFromEnv 从标准环境变量探测语言：LC_ALL > LC_MESSAGES > LANG。
// 只取第一个「已设置且非空」的变量（空串视为未设置），一旦命中即以其值解析，
// 后续变量不再参与——避免 LC_ALL=en_US、LANG=zh_CN 被误判为中文（英文环境被忽略）。
// 无任何变量时返回空串（由调用方继续平台兜底）。
func langFromEnv() Lang {
	for _, k := range []string{"LC_ALL", "LC_MESSAGES", "LANG"} {
		if v := strings.TrimSpace(os.Getenv(k)); v != "" {
			return NormalizeLang(v)
		}
	}
	return ""
}

// langFromUILanguageID 把 Windows GetUserDefaultUILanguage 的 LANGID 归一为语言：
// 低 10 位为主语言 ID（0x0004 = 中文）；0 表示未知（返回空，由调用方回退）。
func langFromUILanguageID(id uintptr) Lang {
	if id == 0 {
		return ""
	}
	if id&0x3ff == 0x0004 {
		return ZH
	}
	return EN
}

// firstAppleLanguagesLang 解析 `defaults read -g AppleLanguages` 输出（plist 数组
// 文本，如 `( en,\n "zh-Hans-CN"\n)`），取首个非空语言条目——AppleLanguages 按
// 用户界面语言首选顺序排列，首项即系统界面语言；无法解析返回空串。
func firstAppleLanguagesLang(out string) Lang {
	s := strings.TrimSpace(out)
	s = strings.TrimPrefix(s, "(")
	for {
		s = strings.TrimSpace(s)
		if s == "" || s == ")" {
			return ""
		}
		end := strings.IndexAny(s, ",)")
		item := s
		rest := ""
		if end >= 0 {
			item, rest = s[:end], s[end+1:]
		}
		item = strings.Trim(strings.TrimSpace(item), "\"")
		if item != "" {
			return NormalizeLang(item)
		}
		if end < 0 {
			return ""
		}
		s = rest
	}
}

// appleLocaleLang 解析 `defaults read -g AppleLocale` 输出（如 `zh_CN`、
// `en_US@rg=zhzzhy`）。AppleLocale 只含地区/语言偏好，非界面语言首选；
// 仅当 AppleLanguages 不可用时兜底。
func appleLocaleLang(out string) Lang {
	s := strings.Trim(strings.TrimSpace(out), "\"")
	if s == "" {
		return ""
	}
	return NormalizeLang(s)
}

// darwinLangFromOutputs macOS 两级解析顺序：先 AppleLanguages（界面语言，首选
// 优先），再 AppleLocale（地区兜底）。中文地区 + 英文首选应判定为英文。
// 两者都为空返回空串。
func darwinLangFromOutputs(languages, locale string) Lang {
	if l := firstAppleLanguagesLang(languages); l != "" {
		return l
	}
	return appleLocaleLang(locale)
}
