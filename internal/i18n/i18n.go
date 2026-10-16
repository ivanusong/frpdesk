// Package i18n 提供 frpdesk 后端的最小双语（简体中文/英文）文案能力。
//
// 设计约定（与前端 frontend/dist/i18n.js 对齐）：
//   - 集中词典：key → {zh, en} 两条文案，{0}..{n} 为参数占位符；
//   - 进程级单一当前语言（启动时按配置/系统解析，前端可通过绑定切换）；
//   - 查找顺序：当前语言 → 英文回退 → key 本身（缺失 key 可见且可排查）；
//   - 错误保留 frp 原始文本进日志（RawErr / slog），此处只承载「直接可见」文案。
//
// 默认语言为简体中文：与历史行为一致，且仅影响「配置加载前」的极短窗口与
// 单元测试环境；应用启动时 NewAppService 会按用户偏好/系统语言重新解析。
package i18n

import (
	"errors"
	"fmt"
	"strings"
	"sync"
)

// Lang 支持的语言（BCP-47 风格标签）。
type Lang string

// 支持的语言常量。
const (
	ZH Lang = "zh-CN"
	EN Lang = "en"
)

// 语言偏好取值（持久化在 config.json 的 language 字段，前端 localStorage 同名）。
const (
	PrefSystem = "system"
	PrefZH     = string(ZH)
	PrefEN     = string(EN)
)

var (
	mu         sync.RWMutex
	current    = ZH      // 进程当前语言（默认中文，启动时被 SetPreference 覆盖）
	detectOnce sync.Once // 系统语言探测只做一次（darwin 需子进程，避免重复开销）
	detected   = EN      // 探测结果缓存
)

// Current 返回当前生效语言。
func Current() Lang {
	mu.RLock()
	defer mu.RUnlock()
	return current
}

// Set 直接设置当前语言（规范化：非 zh 一律回退英文）。
func Set(l Lang) {
	mu.Lock()
	current = NormalizeLang(string(l))
	mu.Unlock()
}

// SetPreference 按「语言偏好」解析并应用当前语言：
// "system"/"" → 系统语言探测；zh 开头 → 简体中文；其他 → 英文回退。
// 返回解析后的语言。
func SetPreference(pref string) Lang {
	if NormalizePref(pref) == PrefSystem {
		Set(detectSystem())
		return Current()
	}
	Set(Lang(NormalizePref(pref)))
	return Current()
}

// NormalizePref 规范化偏好值：只允许 system/zh-CN/en；未知值回退 system
// （由调用方按系统语言解析，用户显式选择非法值时表现为跟随系统，可接受）。
func NormalizePref(pref string) string {
	switch strings.TrimSpace(pref) {
	case PrefZH:
		return PrefZH
	case PrefEN:
		return PrefEN
	default:
		return PrefSystem
	}
}

// IsValidPref 是否为可持久化的合法偏好值（与 NormalizePref 对应）。
func IsValidPref(pref string) bool {
	switch strings.TrimSpace(pref) {
	case PrefSystem, PrefZH, PrefEN:
		return true
	default:
		return false
	}
}

// NormalizeLang 把任意 locale 标签（zh_CN / zh-Hans-CN@rg=… / en_US.UTF-8 / C …）
// 归一为支持的语言：zh 开头 → ZH，其他（含未知/C/POSIX）→ EN（其他语言回退英文）。
func NormalizeLang(locale string) Lang {
	s := strings.TrimSpace(locale)
	if i := strings.IndexAny(s, ".@"); i >= 0 {
		s = s[:i] // 去代码集（.UTF-8）与修饰符（@rg=…）
	}
	s = strings.ReplaceAll(s, "_", "-")
	if strings.EqualFold(strings.SplitN(s, "-", 2)[0], "zh") {
		return ZH
	}
	return EN
}

// T 取当前语言文案并做 {n} 参数插值；args 为占位参数。
// 查找顺序：当前语言 → 英文 → key 本身（保证缺失 key 可被发现）。
func T(key string, args ...any) string {
	return TL(Current(), key, args...)
}

// TL 指定语言取文案（测试与回退逻辑复用）。
func TL(l Lang, key string, args ...any) string {
	if l != ZH && l != EN {
		l = EN
	}
	msg, ok := dict[key]
	if !ok {
		return key
	}
	text := msg.zh
	if l == EN {
		text = msg.en
	}
	if text == "" {
		text = msg.zh // en 缺失时回退中文，再由完整性测试兜住
	}
	return interpolate(text, args...)
}

// interpolate 把文案中的 {0}..{9} 替换为 fmt.Sprint(args[i])；
// 未提供的占位符原样保留（词典错误肉眼可见）。
func interpolate(text string, args ...any) string {
	if len(args) == 0 || !strings.ContainsAny(text, "{}") {
		return text
	}
	var b strings.Builder
	for i := 0; i < len(text); {
		c := text[i]
		if c == '{' && i+2 < len(text) && text[i+1] >= '0' && text[i+1] <= '9' && text[i+2] == '}' {
			n := int(text[i+1] - '0')
			if n < len(args) {
				b.WriteString(fmt.Sprint(args[n]))
				i += 3
				continue
			}
		}
		b.WriteByte(c)
		i++
	}
	return b.String()
}

// sentinelError 包装哨兵错误：Error() 按当前语言渲染，Unwrap 保留哨兵身份
// （errors.Is 继续命中原哨兵，调用方分支判断不受影响）。
type sentinelError struct {
	sentinel error
	key      string
	args     []any
}

func (e *sentinelError) Error() string { return T(e.key, e.args...) }
func (e *sentinelError) Unwrap() error { return e.sentinel }

// Err 返回保留哨兵身份（errors.Is）且消息按当前语言渲染的错误。
func Err(sentinel error, key string, args ...any) error {
	return &sentinelError{sentinel: sentinel, key: key, args: args}
}

// LocalizedError 双语错误：Error() 按进程当前语言渲染（状态/错误等 UI 继续双语），
// 固定英文文本经 ErrText 取出（环形日志通道用，不随界面语言变化）。
// 可选包装下层错误：Unwrap 透传，errors.Is/As 语义不受影响。
type LocalizedError struct {
	key  string
	args []any
	err  error // 可选下层错误（nil = 无包装）
}

// LErr 构造双语错误（无包装）。key 必须在词典中存在（i18n_test 兜底）。
func LErr(key string, args ...any) *LocalizedError {
	return &LocalizedError{key: key, args: args}
}

// LErrWrap 构造带下层错误的双语错误：渲染形如 "<当前语言文案>: <下层错误>"。
func LErrWrap(key string, err error, args ...any) *LocalizedError {
	return &LocalizedError{key: key, args: args, err: err}
}

func (e *LocalizedError) Error() string {
	msg := T(e.key, e.args...)
	if e.err != nil {
		return msg + ": " + e.err.Error()
	}
	return msg
}

// Unwrap 透传下层错误，保留 errors.Is/As 判断链。
func (e *LocalizedError) Unwrap() error { return e.err }

// EN 固定英文渲染（环形日志通道专用；包装链逐层取英文）。
func (e *LocalizedError) EN() string {
	msg := TL(EN, e.key, e.args...)
	if e.err != nil {
		return msg + ": " + ErrText(e.err)
	}
	return msg
}

// ErrText 返回错误的「固定英文」文本：LocalizedError 取英文渲染
// （状态/错误 UI 仍走 Error() 保持双语），其余错误保持原文
// （FRP/OS 原始错误不在本类型体系内，原样进日志便于排查）。
func ErrText(err error) string {
	if err == nil {
		return ""
	}
	var le *LocalizedError
	if errors.As(err, &le) {
		return le.EN()
	}
	return err.Error()
}

// detectSystem 探测操作系统语言（结果缓存；详见各平台 detect_*.go）。
func detectSystem() Lang {
	detectOnce.Do(func() { detected = detectSystemLang() })
	return detected
}
