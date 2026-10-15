//go:build windows

// Windows 更新残留清理入口：主实例 ServiceStartup 后异步清理本目录中
// 更新替换留下的 <exe>.old.<数字> 残留（helper 删除时文件可能仍被占用）。
// --version 快速路径、更新 helper 哨兵模式、第二实例通知均不走 ServiceStartup，
// 不会触发清理；生命周期复用 a.up.ctx（ServiceShutdown 已 cancel）。
package main

import (
	"os"
	"path/filepath"

	"frpdesk/internal/updatecleanup"
)

// startUpdateCleanup 异步启动更新残留清理（内部 goroutine + 首扫延迟，不阻塞启动）。
func (a *AppService) startUpdateCleanup() {
	exe, err := os.Executable()
	if err != nil {
		a.logUpdate("warn", "[更新] 无法定位当前可执行文件，跳过更新残留清理：%v", err)
		return
	}
	updatecleanup.Start(a.up.ctx, updatecleanup.Options{
		Dir:     filepath.Dir(exe),
		ExeBase: filepath.Base(exe),
		Log:     a.logUpdate,
	})
}
