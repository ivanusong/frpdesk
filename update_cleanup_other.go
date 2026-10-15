//go:build !windows

// 非 Windows 平台：更新替换走 Unix rename 语义，无 .old 残留问题，空实现。
package main

// startUpdateCleanup 非 Windows 平台无更新残留，不做任何事。
func (a *AppService) startUpdateCleanup() {}
