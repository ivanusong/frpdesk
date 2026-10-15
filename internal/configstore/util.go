package configstore

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"strings"
	"time"
)

// trimSpace 去除首尾空白。
func trimSpace(s string) string {
	return strings.TrimSpace(s)
}

// newID 生成随机 ID（等价老版 crypto.randomUUID 的占位作用）。
func newID() string {
	buf := make([]byte, 16)
	if _, err := rand.Read(buf); err != nil {
		// crypto/rand 失败几乎不可能发生，兜底用时间戳保证唯一性
		return fmt.Sprintf("%016x", time.Now().UnixNano())
	}
	return hex.EncodeToString(buf)
}

// BriefMessage 把错误/消息单行化并按 rune 截断：回车、换行、制表符与连续空白
// 折叠成单个空格；超过 max（rune 数）截断以 … 结尾；max<=0 只单行化不截断。
// 用于 TunnelStatus.Message 等直达前端与托盘原生菜单的文本，
// 防止 frp 多行/超长错误把 UI 与菜单撑坏（完整原文仍进环形日志）。
func BriefMessage(s string, max int) string {
	single := strings.Join(strings.Fields(s), " ")
	runes := []rune(single)
	if max <= 0 || len(runes) <= max {
		return single
	}
	return string(runes[:max]) + "…"
}
