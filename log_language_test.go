package main

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"strconv"
	"strings"
	"testing"
	"unicode"
)

// 日志语言守卫：写入环形日志（前端「日志」面板 / LogEntry）的应用消息本体
// 约定固定英文，不随界面语言翻译（状态、错误、托盘等非日志 UI 仍走 i18n 双语）。
//
// 本测试静态扫描会进入日志通道的字符串字面量，防止后续在
// Manager.logLocked / Manager.AppendLog / AppService.logUpdate / updater(c.log)
// / updatecleanup(logf) 等调用里重新引入中文消息本体：
//   - 日志调用（logLocked/AppendLog/logUpdate/logf/log/Log/check）消息实参中
//     内嵌的全部字符串字面量（含 fmt.Sprintf 内层模板）不得含 CJK 字符；
//   - 这些文件里的 fmt.Errorf / errors.New 错误文本同样会经 err.Error()/i18n.ErrText
//     落入环形日志，模板一律英文（双语错误走 i18n.LErr/LErrWrap，key 为 ASCII）；
//   - i18n.LErr/LErrWrap 的 key 实参必须 ASCII。
//
// 刻意豁免（不在扫描范围）：注释、测试文件、FRP/OS 原始错误（运行期变量，
// 非字面量）、用户输入的服务器名/服务名（值拷贝，非字面量）、状态/托盘等
// 非日志 UI 文案（位于 internal/i18n/keys.go 词典，保持双语）。
// 新增会写环形日志的文件时，必须加入下方 targetFiles。

// targetFiles 会形成 LogEntry/环形日志的消息源文件（相对仓库根，即本包目录）。
var targetFiles = []string{
	"internal/tunnel/manager.go",
	"internal/tunnel/corsproxy.go",
	"app.go",
	"updater.go",
	"updater_failover.go",
	"update_cleanup_windows.go",
	"internal/updatecleanup/cleanup.go",
}

// requiredSymbols 关键入口必须仍存在于对应文件：防止函数改名后扫描规则
// 静默失配（守卫空转）。
var requiredSymbols = map[string][]string{
	"internal/tunnel/manager.go": {"func (m *Manager) logLocked", "func (m *Manager) AppendLog"},
	"app.go":                     {"func (a *AppService) logUpdate"},
}

// logCallRules 按被调名（标识符或选择器末段）匹配的日志调用：值为消息起始实参下标
//（其后实参一并扫描）。check 的首参为更新来源标签，进入日志正文。
var logCallRules = map[string]int{
	"logLocked": 2,
	"AppendLog": 1,
	"logUpdate": 1,
	"logf":      1,
	"log":       1,
	"Log":       1,
	"check":     0,
}

// qualifiedCallRules 按完整选择器文本匹配：错误构造与双语错误的 key 实参
//（错误文本会经 err.Error()/i18n.ErrText 进入环形日志，模板必须英文/key 必须 ASCII）。
var qualifiedCallRules = map[string]int{
	"fmt.Errorf":    0,
	"errors.New":    0,
	"i18n.LErr":     0,
	"i18n.LErrWrap": 0,
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
	fset := token.NewFileSet()
	violations := []string{}

	for _, rel := range targetFiles {
		raw, err := os.ReadFile(rel)
		if err != nil {
			t.Fatalf("读取 %s 失败: %v", rel, err)
		}
		// 守卫入口仍在：改名会使扫描规则失配，先行拦截
		for _, sym := range requiredSymbols[rel] {
			if !strings.Contains(string(raw), sym) {
				t.Errorf("%s 缺少关键入口 %q：扫描规则需同步更新（见 log_language_test.go 文件头）", rel, sym)
			}
		}

		f, err := parser.ParseFile(fset, rel, raw, 0)
		if err != nil {
			t.Fatalf("解析 %s 失败: %v", rel, err)
		}

		ast.Inspect(f, func(n ast.Node) bool {
			call, ok := n.(*ast.CallExpr)
			if !ok {
				return true
			}

			msgArg := -1
			switch fn := call.Fun.(type) {
			case *ast.Ident:
				if idx, ok := logCallRules[fn.Name]; ok {
					msgArg = idx
				}
			case *ast.SelectorExpr:
				if idx, ok := logCallRules[fn.Sel.Name]; ok {
					msgArg = idx
				}
				// 完整选择器精确匹配（避免误伤其他包的同名方法）
				if pkg, ok := fn.X.(*ast.Ident); ok {
					if idx, ok := qualifiedCallRules[pkg.Name+"."+fn.Sel.Name]; ok {
						msgArg = idx
					}
				}
			}
			if msgArg < 0 || msgArg >= len(call.Args) {
				return true
			}

			pos := fset.Position(call.Pos())
			for _, lit := range collectStringLiterals(&ast.CallExpr{Fun: call.Fun, Args: call.Args[msgArg:]}) {
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
}
