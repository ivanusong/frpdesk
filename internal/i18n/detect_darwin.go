//go:build darwin

package i18n

import "os/exec"

// detectSystemLang macOS 系统语言探测：优先环境变量（终端/dev 场景）；
// GUI（Finder/launchd 启动）无 LANG 时读 `defaults read -g`：
// AppleLanguages（界面语言首选序）优先于 AppleLocale（地区兜底）——
// 中文地区 + 英文首选必须判定为英文，反之亦然。
func detectSystemLang() Lang {
	if l := langFromEnv(); l != "" {
		return l
	}
	if l := darwinLangFromOutputs(defaultsRead("AppleLanguages"), defaultsRead("AppleLocale")); l != "" {
		return l
	}
	return EN
}

// defaultsRead 读取 macOS 全局偏好（cfprefsd）；失败返回空串由调用方兜底。
func defaultsRead(key string) string {
	out, err := exec.Command("defaults", "read", "-g", key).Output()
	if err != nil {
		return ""
	}
	return string(out)
}
