// Package autostart 注册/查询/注销「登录电脑后自动运行 Frpdesk」的系统登录启动项。
//
// 状态语义（与前端设置弹层冻结契约）：
//   - 系统注册是唯一权威状态：已注册且指向当前可执行文件才算开启；注册残留但
//     路径已过期（应用被移动/重装换路径）按未开启处理，重新开启会整体重写；
//   - 全部系统操作幂等：重复 Enable / Disable 均为 no-op 成功；
//   - 不写本地配置文件假装成功：真实系统操作失败原样返回错误，由 AppService
//     反馈用户（前端恢复开关并提示）。
//
// 平台实现（各平台文件提供 statusImpl/enableImpl/disableImpl）：
//   - macOS：~/Library/LaunchAgents/cc.frp.frpdesk.plist（launchd agent，
//     RunAtLoad，登录时 launchd 自动加载目录内全部 plist，无需额外注册命令）；
//   - Windows：HKCU\...\CurrentVersion\Run 注册表值（REG_SZ，带引号完整路径）；
//   - Linux/其他：XDG autostart 桌面项（~/.config/autostart/cc.frp.frpdesk.desktop）。
package autostart

import (
	"os"

	"frpdesk/internal/i18n"
)

// AppID 登录启动项的稳定标识（launchd Label / Windows Run 值名 / XDG 桌面项文件名）。
// 与应用单实例 UniqueID（main.go）保持一致。
const AppID = "cc.frp.frpdesk"

// executable 当前可执行文件路径（测试注入替身用；生产恒为 os.Executable）。
var executable = os.Executable

// dirHook 平台注册目录替身（测试注入隔离目录用；nil 时走平台真实目录）。
var dirHook func() (string, error)

// registeredDir 平台注册目录：测试注入优先（dirHook），生产走平台默认实现。
// 两种「写注册文件」平台（darwin/linux）共用此入口，包级测试在任意平台可跑。
func registeredDir(defaultDir func() (string, error)) (string, error) {
	if dirHook != nil {
		return dirHook()
	}
	return defaultDir()
}

// currentExe 当前可执行文件路径；失败返回双语错误（UI 可见）。
func currentExe() (string, error) {
	exe, err := executable()
	if err != nil {
		return "", i18n.LErrWrap("err.autostartExePath", err)
	}
	return exe, nil
}

// Status 返回开机自启动是否已注册且指向当前可执行文件（真实系统状态，非配置镜像）。
func Status() (bool, error) {
	exe, err := currentExe()
	if err != nil {
		return false, err
	}
	enabled, err := statusImpl(exe)
	if err != nil {
		return false, i18n.LErrWrap("err.autostartRead", err)
	}
	return enabled, nil
}

// Enable 注册开机自启动（幂等）：已注册且指向当前可执行文件时 no-op 成功。
func Enable() error {
	exe, err := currentExe()
	if err != nil {
		return err
	}
	if err := enableImpl(exe); err != nil {
		return i18n.LErrWrap("err.autostartWrite", err)
	}
	return nil
}

// Disable 注销开机自启动（幂等）：未注册时 no-op 成功。
func Disable() error {
	if err := disableImpl(); err != nil {
		return i18n.LErrWrap("err.autostartRemove", err)
	}
	return nil
}
