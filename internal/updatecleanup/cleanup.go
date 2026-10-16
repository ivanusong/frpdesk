// Package updatecleanup 清理 Windows 应用内更新留下的二进制残留。
//
// 更新重启时框架 helper（wails pkg/updater/helper_windows.go）会把被占用的旧
// 可执行文件改名为 <exe>.old.<UnixNano> 留在原目录，立即删除失败时会残留。
// 本包在主实例启动后异步清理：仅精确匹配
// <exe文件名>.old.<纯ASCII数字> 的常规文件，带界重试（首次延迟 + 失败重试），
// 生命周期复用宿主 ctx（应用退出即取消等待与重试）。
//
// 安全边界：只删普通文件；当前 EXE、其他 EXE 的残留、.bak、非数字后缀、
// 目录、符号链接一律不动；匹配用 QuoteMeta 精准正则，不用宽泛 glob。
package updatecleanup

import (
	"context"
	"os"
	"path/filepath"
	"regexp"
	"time"
)

// 默认节奏：启动后延迟 5s 首扫；失败（文件被占用等）待 5s 重试；最多 6 次。
const (
	defaultDelay       = 5 * time.Second
	defaultMaxAttempts = 6
)

// Logger 日志出口（与 updaterController.log 同型：slog + 应用日志环形缓冲）。
type Logger func(level, format string, args ...any)

// Options 清理参数；零值字段在 Run 内归一化（测试可注入 Remove/Delay）。
type Options struct {
	Dir         string             // 扫描目录（通常为当前可执行文件所在目录）
	ExeBase     string             // 当前可执行文件 basename（如 frpdesk.exe）
	MaxAttempts int                // 总尝试次数上限（含首次；<=0 取默认 6）
	Delay       time.Duration      // 首次前与每次失败后的等待（<=0 取默认 5s）
	Remove      func(string) error // 删除函数（nil 取 os.Remove；测试注入计错/计数）
	Log         Logger             // 日志出口（nil 则静默）
}

// Start 异步执行清理（独立 goroutine，不阻塞调用方），生命周期复用宿主 ctx。
func Start(ctx context.Context, opt Options) {
	go Run(ctx, opt)
}

// Run 同步执行：等待 Delay → 扫描删除 → 有失败且未达上限则再等 Delay 重试，
// 完成即停；达上限仍有残留则 warn 收尾。ctx 取消（应用退出）立即返回。
func Run(ctx context.Context, opt Options) {
	if opt.MaxAttempts <= 0 {
		opt.MaxAttempts = defaultMaxAttempts
	}
	if opt.Delay <= 0 {
		opt.Delay = defaultDelay
	}
	if opt.Remove == nil {
		opt.Remove = os.Remove
	}
	logf := opt.Log
	if logf == nil {
		logf = func(string, string, ...any) {}
	}
	opt.Log = logf

	logf("info", "[update] Starting update residue cleanup (dir %s, exe %s, up to %d attempts)",
		opt.Dir, opt.ExeBase, opt.MaxAttempts)
	for attempt := 1; ; attempt++ {
		// 首次与每次重试前统一等待：更新刚完成时旧 EXE 可能仍被占用，给足退避；
		// 等待期间应用退出（ctx 取消）则放弃清理
		select {
		case <-ctx.Done():
			logf("info", "[update] Update residue cleanup canceled (app quit, before attempt %d)", attempt)
			return
		case <-time.After(opt.Delay):
		}

		remaining, removed, err := sweep(ctx, opt)
		if ctx.Err() != nil {
			// sweep 中途取消（应用退出）：统一 info 收尾，不误报扫描/删除失败
			logf("info", "[update] Update residue cleanup canceled (app quit, during attempt %d)", attempt)
			return
		}
		if err != nil {
			logf("error", "[update] Failed to scan update residue (attempt %d, dir %s): %v", attempt, opt.Dir, err)
			if attempt >= opt.MaxAttempts {
				logf("warn", "[update] Update residue scan hit the retry limit (%d attempts); will retry on next launch", opt.MaxAttempts)
				return
			}
			continue
		}
		if remaining == 0 {
			logf("info", "[update] Update residue cleanup finished (attempt %d, removed %d)", attempt, removed)
			return
		}
		if attempt >= opt.MaxAttempts {
			logf("warn", "[update] Update residue cleanup incomplete: retry limit reached (%d attempts), %d locked file(s) remain; will retry on next launch",
				opt.MaxAttempts, remaining)
			return
		}
		logf("warn", "[update] Failed to remove some update residue (attempt %d, %d remaining, usually file locked); retrying in %s",
			attempt, remaining, opt.Delay)
	}
}

// sweep 扫描 Dir 并删除全部匹配残留，返回（剩余失败数、本次成功删除数）。
// 扫描失败返回错误；单个文件删除失败计入剩余（等待后有界重试）；
// 名字匹配但非普通文件（目录/符号链接）只记日志跳过，绝不删除也不重试。
func sweep(ctx context.Context, opt Options) (remaining, removed int, err error) {
	entries, err := os.ReadDir(opt.Dir)
	if err != nil {
		return 0, 0, err
	}
	for _, e := range entries {
		if !IsStaleResidue(e.Name(), opt.ExeBase) {
			continue
		}
		if !e.Type().IsRegular() {
			opt.Log("warn", "[update] Skipping non-regular residue %s (directory/symlink; not deleted)", filepath.Join(opt.Dir, e.Name()))
			continue
		}
		if ctx.Err() != nil {
			return remaining + 1, removed, ctx.Err() // 应用退出：余下残留不再处理，由 Run 统一收尾
		}
		full := filepath.Join(opt.Dir, e.Name())
		if rmErr := opt.Remove(full); rmErr != nil {
			opt.Log("warn", "[update] Failed to remove update residue %s: %v", full, rmErr)
			remaining++
			continue
		}
		removed++
		opt.Log("info", "[update] Removed update residue %s", full)
	}
	return remaining, removed, nil
}

// residuePattern 按 exeBase 现编译精准正则：<exeBase>.old.<纯ASCII数字>。
// QuoteMeta 转义文件名中的 . [ ] 等元字符（如 frpdesk.exe → frpdesk\.exe），
// 杜绝宽泛 glob；[0-9]+ 即纯 ASCII 数字（Go regexp 不做 Unicode 数字宽匹配）。
func residuePattern(exeBase string) *regexp.Regexp {
	return regexp.MustCompile("^" + regexp.QuoteMeta(exeBase) + `\.old\.[0-9]+$`)
}

// IsStaleResidue 判断目录项名是否为当前可执行文件的更新残留：
// 仅 <exeBase>.old.<纯ASCII数字>（与 helper_windows.go 的 aside 命名一致）。
func IsStaleResidue(name, exeBase string) bool {
	if exeBase == "" || name == exeBase {
		return false // 绝不匹配当前 EXE 本身
	}
	return residuePattern(exeBase).MatchString(name)
}
