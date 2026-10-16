package main

import (
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"unicode"
)

// 日志语言守卫（全量）：所有应用日志输出固定英文，不随界面语言翻译，也不得
// 残留任何中文消息本体/前缀（用户语义：日志部分直接全部使用英文）。覆盖：
//
//  1. 终端诊断日志：全部非 third_party、非测试运行时 Go 文件中的
//     slog.Debug/Info/Warn/Error/Print(含 *Context) 消息模板（首个字符串实参），
//     以及 fmt.Fprint/Fprintf/Fprintln（stderr/stdout 等）写入的文本；
//     动态结构化值（状态消息/错误）必须经英文通道（configstore LogMessage /
//     i18n.ErrText / TLEn），不得直接传本地化中文文本。
//  2. 前端日志面板的环形日志：Manager.logLocked / Manager.AppendLog /
//     AppService.logUpdate / updater(c.log) / updatecleanup(logf/Log) 等
//     消息实参内嵌的全部字符串字面量（含 fmt.Sprintf 内层模板）。
//  3. 错误构造模板：fmt.Errorf / errors.New（会经 err.Error()/i18n.ErrText
//     落入日志）与 i18n.LErr/LErrWrap 的 key（ASCII）。
//
// 刻意豁免：注释、测试文件、internal/i18n/keys.go（双语 UI 词典）、FRP/OS
// 原始错误（运行期变量）、用户输入的服务器名/服务名（值拷贝）、状态/托盘等
// 非日志 UI 文案（词典渲染，保持双语）。
// 守卫按目录遍历全量扫描，不依赖手工维护的目标文件清单；关键入口仍在校验
// （防函数改名后规则静默失配）。

// skipDirs 扫描时跳过的目录（第三方代码与前端产物）。
var skipDirs = map[string]bool{
	"third_party": true,
	"licenses":    true,
	"frontend":    true,
	"bin":         true,
	"build":       true,
	"docs":        true,
}

// dictFiles 豁免的词典文件（中文 UI 文案不在日志守卫范围）。
var dictFiles = map[string]bool{
	"internal/i18n/keys.go": true,
}

// 关键入口必须仍存在：改名会使扫描规则失配（守卫空转），先行拦截。
var requiredSymbols = []string{
	"internal/tunnel/manager.go:func (m *Manager) logLocked",
	"internal/tunnel/manager.go:func (m *Manager) AppendLog",
	"internal/tunnel/manager.go:func (m *Manager) transitionLocked",
	"internal/tunnel/logring.go:func (s *frpLogSink) enqueue",
	"app.go:func (a *AppService) logUpdate",
	"app.go:func (a *AppService) FrontendLog",
	"tray.go:func (t *trayController) applyStatus",
	"internal/configstore/store.go:func AggregateStatus",
	"internal/configstore/store.go:func AggregateStatusEN",
}

// minRuntimeFiles 扫描文件数下限：低于该值说明遍历配置失准（守卫空转）。
const minRuntimeFiles = 15

// logCallRules 按被调名（标识符或选择器末段）匹配的环形日志调用：
// 值为消息起始实参下标（其后实参一并扫描）。check 的首参为更新来源标签。
var logCallRules = map[string]int{
	"logLocked": 2,
	"AppendLog": 1,
	"logUpdate": 1,
	"logf":      1,
	"log":       1,
	"Log":       1,
	"check":     0,
}

// qualifiedErrorRules 按完整选择器文本匹配的错误构造：模板/key 会经
// err.Error()/i18n.ErrText 进入日志，必须英文/ASCII。
var qualifiedErrorRules = map[string]int{
	"fmt.Errorf":    0,
	"errors.New":    0,
	"i18n.LErr":     0,
	"i18n.LErrWrap": 0,
}

// slogCalls 终端诊断日志调用（消息模板为首参）。
var slogCalls = map[string]bool{
	"Debug": true, "Info": true, "Warn": true, "Error": true, "Print": true,
	"DebugContext": true, "InfoContext": true, "WarnContext": true, "ErrorContext": true,
}

// fmtWrites 终端/流写入调用（文本实参从下标 1 开始）。
var fmtWrites = map[string]bool{
	"Fprint": true, "Fprintf": true, "Fprintln": true,
}

// hasCJK 是否包含 CJK 文字字符（汉字/日文假名/韩文谚文；全角标点不在其内，
// 但消息本体为英文时不会出现）。
func hasCJK(s string) bool {
	for _, r := range s {
		if unicode.Is(unicode.Han, r) || unicode.Is(unicode.Hiragana, r) ||
			unicode.Is(unicode.Katakana, r) || unicode.Is(unicode.Hangul, r) {
			return true
		}
	}
	return false
}

// collectStringLiterals 递归收集表达式内全部字符串字面量（覆盖 fmt.Sprintf 内层、
// 字符串拼接等）。
func collectStringLiterals(n ast.Node) []*ast.BasicLit {
	var out []*ast.BasicLit
	ast.Inspect(n, func(x ast.Node) bool {
		if lit, ok := x.(*ast.BasicLit); ok && lit.Kind == token.STRING {
			out = append(out, lit)
		}
		return true
	})
	return out
}

// TestLogBodiesAreEnglish 见文件头注释。
func TestLogBodiesAreEnglish(t *testing.T) {
	// 收集运行时 Go 文件（非 third_party、非测试）
	var files []string
	err := filepath.WalkDir(".", func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			name := d.Name()
			if path != "." && (skipDirs[name] || strings.HasPrefix(name, ".")) {
				return filepath.SkipDir
			}
			return nil
		}
		if strings.HasSuffix(path, ".go") && !strings.HasSuffix(path, "_test.go") {
			files = append(files, filepath.ToSlash(path))
		}
		return nil
	})
	if err != nil {
		t.Fatalf("遍历仓库失败: %v", err)
	}
	if len(files) < minRuntimeFiles {
		t.Fatalf("运行时 Go 文件仅 %d 个（<%d），遍历配置疑似失准，守卫拒绝空转", len(files), minRuntimeFiles)
	}

	// 关键入口仍在：改名/移动会使规则失配，先行拦截
	allSrc := map[string]string{}
	for _, rel := range files {
		raw, err := os.ReadFile(rel)
		if err != nil {
			t.Fatalf("读取 %s 失败: %v", rel, err)
		}
		allSrc[rel] = string(raw)
	}
	for _, req := range requiredSymbols {
		rel, sym, _ := strings.Cut(req, ":")
		if !strings.Contains(allSrc[rel], sym) {
			t.Errorf("%s 缺少关键入口 %q：扫描规则需同步更新（见 log_language_test.go 文件头）", rel, sym)
		}
	}

	fset := token.NewFileSet()
	violations := []string{}
	checked := 0

	for _, rel := range files {
		if dictFiles[rel] {
			continue // 双语 UI 词典不在日志守卫范围
		}
		f, err := parser.ParseFile(fset, rel, allSrc[rel], 0)
		if err != nil {
			t.Fatalf("解析 %s 失败: %v", rel, err)
		}

		ast.Inspect(f, func(n ast.Node) bool {
			call, ok := n.(*ast.CallExpr)
			if !ok {
				return true
			}

			// 规则选择：消息起始实参下标（-1 = 非目标调用）
			msgArg := -1
			switch fn := call.Fun.(type) {
			case *ast.Ident:
				// 包内直接调用：logLocked / AppendLog / logf
				if idx, ok := logCallRules[fn.Name]; ok {
					msgArg = idx
				}
			case *ast.SelectorExpr:
				// slog 诊断日志：消息模板为首参
				if pkg, ok := fn.X.(*ast.Ident); ok && pkg.Name == "slog" && slogCalls[fn.Sel.Name] {
					msgArg = 0
				}
				// fmt.Fprint*(w, ...)：写入文本从下标 1 起
				if pkg, ok := fn.X.(*ast.Ident); ok && pkg.Name == "fmt" && fmtWrites[fn.Sel.Name] && len(call.Args) >= 2 {
					msgArg = 1
				}
				// 环形日志方法/函数（c.log、a.logUpdate、opt.Log、a.up.check…）
				if idx, ok := logCallRules[fn.Sel.Name]; ok {
					msgArg = idx
				}
				// 错误构造完整选择器精确匹配（避免误伤其他包同名方法）
				if pkg, ok := fn.X.(*ast.Ident); ok {
					if idx, ok := qualifiedErrorRules[pkg.Name+"."+fn.Sel.Name]; ok {
						msgArg = idx
					}
				}
			}
			if msgArg < 0 || msgArg >= len(call.Args) {
				return true
			}
			checked++

			pos := fset.Position(call.Pos())
			expr := &ast.CallExpr{Fun: call.Fun, Args: call.Args[msgArg:]}
			for _, lit := range collectStringLiterals(expr) {
				text, err := strconv.Unquote(lit.Value)
				if err != nil {
					text = lit.Value // 罕见转义异常：退化为原文检查
				}
				if hasCJK(text) {
					violations = append(violations,
						pos.String()+": 日志/错误消息本体必须为英文（FRP/OS 原始错误与用户输入不受限）: "+lit.Value)
				}
			}
			return true
		})
	}

	for _, v := range violations {
		t.Error(v)
	}
	if checked < 10 {
		t.Fatalf("规则命中调用仅 %d 处（<10），扫描规则疑似失准，守卫拒绝空转", checked)
	}
}
