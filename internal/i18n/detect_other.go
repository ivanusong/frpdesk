//go:build !darwin && !windows

package i18n

// detectSystemLang 其他平台（Linux 等）：仅环境变量探测，缺失回退英文。
func detectSystemLang() Lang {
	if l := langFromEnv(); l != "" {
		return l
	}
	return EN
}
