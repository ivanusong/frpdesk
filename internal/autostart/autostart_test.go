package autostart

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// stubEnv 注入替身可执行路径与隔离注册目录（dirHook），全程不触碰本机真实登录
// 启动项；返回恢复函数。
func stubEnv(t *testing.T, exe, dir string) {
	t.Helper()
	oldExe, oldDir := executable, dirHook
	executable = func() (string, error) { return exe, nil }
	dirHook = func() (string, error) { return dir, nil }
	t.Cleanup(func() { executable, dirHook = oldExe, oldDir })
}

// TestLaunchdPlistContentAndMatch plist 内容生成与指向校验：Label/RunAtLoad 齐、
// 路径含空格原样保留；指向其他路径（应用被移动）= 未开启；损坏内容 = 未开启。
func TestLaunchdPlistContentAndMatch(t *testing.T) {
	exe := "/Applications/My Apps/Frpdesk.app/Contents/MacOS/frpdesk"
	raw := launchdPlistXML(exe)
	for _, want := range []string{AppID, "<key>RunAtLoad</key>", exe} {
		if !strings.Contains(raw, want) {
			t.Fatalf("plist 缺少 %q:\n%s", want, raw)
		}
	}
	if !launchdPlistEnabled([]byte(raw), exe) {
		t.Fatal("刚生成的 plist 应判定为已注册且指向自身")
	}
	if launchdPlistEnabled([]byte(raw), "/moved/elsewhere/frpdesk") {
		t.Fatal("指向其他路径应判定为未开启（过期注册）")
	}
	if launchdPlistEnabled([]byte("not a plist"), exe) {
		t.Fatal("损坏内容应判定为未开启")
	}
	// 截断文档整体作废：绝不用部分已解析内容判 true
	truncated := `<?xml version="1.0" encoding="UTF-8"?><plist version="1.0"><dict><key>Label</key><string>` + AppID
	if launchdPlistValues([]byte(truncated)) != nil {
		t.Fatal("截断文档应整体作废（返回 nil），不得返回部分解析结果")
	}
	if launchdPlistEnabled([]byte(truncated), exe) {
		t.Fatal("截断文档应判定为未开启")
	}
	// 缺 RunAtLoad：即使 Label/程序都匹配也不算开启
	noRunAtLoad := strings.Replace(raw, "<key>RunAtLoad</key>\n    <true/>\n", "", 1)
	if launchdPlistEnabled([]byte(noRunAtLoad), exe) {
		t.Fatal("缺 RunAtLoad 应判定为未开启")
	}
	// XML 转义：路径含 & < > 时生成的内容必须能被解析回原路径
	weird := `/opt/a&b<c>/frp desk`
	if !launchdPlistEnabled([]byte(launchdPlistXML(weird)), weird) {
		t.Fatalf("含特殊字符路径应往返一致: %s", launchdPlistXML(weird))
	}
}

// TestWindowsRunValueFormat Run 值格式与比对：带引号（空格路径安全）、
// 大小写不敏感、无引号旧值兼容。
func TestWindowsRunValueFormat(t *testing.T) {
	exe := `C:\Program Files\Frpdesk\frpdesk.exe`
	v := windowsRunValue(exe)
	if v != `"`+exe+`"` {
		t.Fatalf("Run 值必须带引号: %q", v)
	}
	if !windowsRunValueMatches(v, exe) || !windowsRunValueMatches(v, strings.ToLower(exe)) {
		t.Fatal("相同路径（含大小写差异）应匹配")
	}
	if !windowsRunValueMatches(exe, exe) {
		t.Fatal("历史无引号值指向相同程序应视为已注册")
	}
	if windowsRunValueMatches(v, `C:\Program Files\Other\app.exe`) {
		t.Fatal("指向其他程序应判定为未注册")
	}
}

// TestXDGDesktopEntryContentAndMatch 桌面项内容生成与指向校验：Exec 按规范转义、
// 路径含空格/特殊字符往返一致、Hidden=true / Type 错误 / 缺失 Exec / 指向其他程序
// 均为未开启；含明确预期字符串断言（不只用自家生成与匹配循环互证）。
func TestXDGDesktopEntryContentAndMatch(t *testing.T) {
	// 明确预期字符串：含空格的普通路径
	exe := "/opt/My Apps/frpdesk"
	want := "[Desktop Entry]\nType=Application\nName=Frpdesk\nExec=\"/opt/My Apps/frpdesk\"\nTerminal=false\n"
	if got := xdgDesktopEntry(exe); got != want {
		t.Fatalf("desktop entry 与预期不符:\ngot:\n%s\nwant:\n%s", got, want)
	}
	// 规范两层转义预期（参数引用层 + key value 层）：字面反斜杠在文件里连续 4 个
	// 反斜杠，字面 $ 前 2 个反斜杠，字面双引号/反引号前 3 个反斜杠，% 写作 %%
	tricky := `/opt/a"b\c$d` + "`" + `e%f`
	wantTrickyExec := `Exec="/opt/a\\"b\\\\c\\$d\\` + "`" + `e%%f"` + "\n"
	gotTricky := xdgDesktopEntry(tricky)
	if !strings.Contains(gotTricky, wantTrickyExec) {
		t.Fatalf("特殊字符路径 Exec 两层转义与预期不符:\ngot:\n%s\nwant exec line: %s", gotTricky, wantTrickyExec)
	}
	// 已知规范字面断言（单一字符，两层各翻倍一次）
	if got := xdgExecEscape(`a\b`); got != `"a\\\\b"` {
		t.Fatalf("字面反斜杠应为连续 4 个反斜杠: %q", got)
	}
	if got := xdgExecEscape(`a$b`); got != `"a\\$b"` {
		t.Fatalf("字面 $ 应为 2 个反斜杠再 $: %q", got)
	}
	if !xdgDesktopEntryEnabled([]byte(gotTricky), tricky) {
		t.Fatal("刚生成的桌面项应判定为已注册且指向自身")
	}
	if xdgDesktopEntryEnabled([]byte(gotTricky), "/opt/other/frpdesk") {
		t.Fatal("指向其他程序应判定为未开启")
	}
	// Hidden=true / Type 错误 / 缺失 Exec：均未开启
	hidden := "[Desktop Entry]\nType=Application\nHidden=true\nExec=" + xdgExecEscape(tricky) + "\n"
	if xdgDesktopEntryEnabled([]byte(hidden), tricky) {
		t.Fatal("Hidden=true 应判定为未开启")
	}
	wrongType := "[Desktop Entry]\nType=Link\nExec=" + xdgExecEscape(tricky) + "\n"
	if xdgDesktopEntryEnabled([]byte(wrongType), tricky) {
		t.Fatal("Type 非 Application 应判定为未开启")
	}
	if xdgDesktopEntryEnabled([]byte("[Desktop Entry]\nType=Application\n"), tricky) {
		t.Fatal("缺失 Exec 应判定为未开启")
	}
	// 其他组内的同名 key 不影响判定
	otherGroup := "[Desktop Action x]\nExec=/somewhere/else\n[Desktop Entry]\nType=Application\nExec=" + xdgExecEscape(tricky) + "\n"
	if !xdgDesktopEntryEnabled([]byte(otherGroup), tricky) {
		t.Fatal("仅认 [Desktop Entry] 组，其他组同名 key 不应干扰")
	}
}

// plistSpecFor / desktopSpecFor：定位于指定文件的两种注册体（生命周期测试共用）。
func plistSpecFor(path string) registerFile {
	return registerFile{location: func() (string, error) { return path, nil }, content: launchdPlistXML, enabledAt: launchdPlistEnabled}
}

func desktopSpecFor(path string) registerFile {
	return registerFile{location: func() (string, error) { return path, nil }, content: xdgDesktopEntry, enabledAt: xdgDesktopEntryEnabled}
}

// TestRegisterFileLifecycle 注册文件生命周期（隔离目录，两种注册体同跑）：
// 未注册 → 开启 → 真实状态开启 → 重复开启幂等（内容不变）→ 关闭 → 状态关闭 →
// 重复关闭幂等；注册文件名与路径含空格。
func TestRegisterFileLifecycle(t *testing.T) {
	cases := []struct {
		name     string
		fileName string
		spec     func(string) registerFile
	}{
		{"launchd plist", "cc.frp.frpdesk.plist", plistSpecFor},
		{"xdg desktop", "my dir" + string(filepath.Separator) + "cc.frp.frpdesk.desktop", desktopSpecFor},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			spec := tc.spec(filepath.Join(t.TempDir(), tc.fileName))
			exe := "/tmp/space dir/frpdesk"

			if enabled, err := spec.status(exe); err != nil || enabled {
				t.Fatalf("初始应为未注册: enabled=%v err=%v", enabled, err)
			}
			if err := spec.enable(exe); err != nil {
				t.Fatalf("enable: %v", err)
			}
			if enabled, err := spec.status(exe); err != nil || !enabled {
				t.Fatalf("enable 后应为已注册: enabled=%v err=%v", enabled, err)
			}
			// 幂等：重复开启内容不变
			path, _ := spec.location()
			before, _ := os.ReadFile(path)
			if err := spec.enable(exe); err != nil {
				t.Fatalf("重复 enable 应幂等成功: %v", err)
			}
			after, _ := os.ReadFile(path)
			if string(before) != string(after) {
				t.Fatal("重复 enable 不应改写注册文件")
			}
			// 关闭与重复关闭幂等
			if err := spec.disable(); err != nil {
				t.Fatalf("disable: %v", err)
			}
			if enabled, err := spec.status(exe); err != nil || enabled {
				t.Fatalf("disable 后应为未注册: enabled=%v err=%v", enabled, err)
			}
			if _, err := os.Stat(path); !os.IsNotExist(err) {
				t.Fatalf("disable 后注册文件应被移除: %v", err)
			}
			if err := spec.disable(); err != nil {
				t.Fatalf("重复 disable 应幂等成功: %v", err)
			}
		})
	}
}

// TestRegisterFileRewritesStalePath 过期注册（应用被移动）真实状态按未开启处理，
// 重新开启会重写为当前路径（自愈）。
func TestRegisterFileRewritesStalePath(t *testing.T) {
	path := filepath.Join(t.TempDir(), "cc.frp.frpdesk.desktop")
	spec := desktopSpecFor(path)
	old := "/old/install/path/frpdesk"
	if err := os.WriteFile(path, []byte(xdgDesktopEntry(old)), 0o644); err != nil {
		t.Fatal(err)
	}
	newExe := "/new/install/path/frp desk"
	if enabled, err := spec.status(newExe); err != nil || enabled {
		t.Fatalf("过期注册应视为未开启: enabled=%v err=%v", enabled, err)
	}
	if err := spec.enable(newExe); err != nil {
		t.Fatalf("重新开启应重写过期注册: %v", err)
	}
	if enabled, err := spec.status(newExe); err != nil || !enabled {
		t.Fatalf("重写后应为已注册: enabled=%v err=%v", enabled, err)
	}
	raw, _ := os.ReadFile(path)
	if !strings.Contains(string(raw), newExe) {
		t.Fatalf("注册应指向新路径:\n%s", raw)
	}
}

// TestRegisterFileFailures 失败上抛：location 失败、目录不可写（权限失败）都必须
// 返回错误，绝不静默假装成功；失败路径不留下临时文件。
func TestRegisterFileFailures(t *testing.T) {
	// location 自身失败
	broken := registerFile{
		location:  func() (string, error) { return "", os.ErrPermission },
		content:   launchdPlistXML,
		enabledAt: launchdPlistEnabled,
	}
	if _, err := broken.status("/x"); !os.IsPermission(err) {
		t.Fatalf("location 失败应上抛: %v", err)
	}
	if err := broken.enable("/x"); !os.IsPermission(err) {
		t.Fatalf("enable 应上抛 location 失败: %v", err)
	}
	if err := broken.disable(); !os.IsPermission(err) {
		t.Fatalf("disable 应上抛 location 失败: %v", err)
	}

	// 目录只读：enable 写入失败
	ro := filepath.Join(t.TempDir(), "ro")
	if err := os.Mkdir(ro, 0o555); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(ro, 0o755) }) // 让 TempDir 清理可用
	spec := plistSpecFor(filepath.Join(ro, "cc.frp.frpdesk.plist"))
	if err := spec.enable("/x/frpdesk"); err == nil {
		t.Fatal("只读目录下 enable 必须失败")
	}
	if entries, err := os.ReadDir(ro); err != nil || len(entries) != 0 {
		t.Fatalf("失败路径不应留下临时文件: entries=%v err=%v", entries, err)
	}
}

// TestPublicAPIWithIsolatedDirs 包级 API 端到端（替身可执行路径 + 隔离注册目录，
// 不触碰本机真实登录启动项）：Status/Enable/Disable 全链路 + 路径含空格 + 幂等。
// Windows 注册表实现无目录注入（dirHook 不生效），在本平台运行会写删用户真实
// Run 值，必须跳过（纯格式测试仍全平台执行；注册表集成须在 Windows 上以替身运行）。
func TestPublicAPIWithIsolatedDirs(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Windows 注册表实现不使用目录注入，跳过以免操作真实 HKCU Run 值")
	}
	stubEnv(t, "/opt/My Tools/frpdesk bin/app", t.TempDir())

	if enabled, err := Status(); err != nil || enabled {
		t.Fatalf("初始应为未注册: enabled=%v err=%v", enabled, err)
	}
	if err := Enable(); err != nil {
		t.Fatalf("Enable: %v", err)
	}
	if err := Enable(); err != nil { // 幂等
		t.Fatalf("重复 Enable 应成功: %v", err)
	}
	enabled, err := Status()
	if err != nil || !enabled {
		t.Fatalf("Enable 后应为已注册: enabled=%v err=%v", enabled, err)
	}
	if err := Disable(); err != nil {
		t.Fatalf("Disable: %v", err)
	}
	if err := Disable(); err != nil {
		t.Fatalf("重复 Disable 应成功: %v", err)
	}
	if enabled, err := Status(); err != nil || enabled {
		t.Fatalf("Disable 后应为未注册: enabled=%v err=%v", enabled, err)
	}
}

// TestExePathFailure 可执行路径无法确定时全部操作上抛（不写任何注册）。
func TestExePathFailure(t *testing.T) {
	old := executable
	executable = func() (string, error) { return "", os.ErrNotExist }
	t.Cleanup(func() { executable = old })

	if _, err := Status(); err == nil {
		t.Fatal("Status 应上抛路径错误")
	}
	if err := Enable(); err == nil {
		t.Fatal("Enable 应上抛路径错误")
	}
}
