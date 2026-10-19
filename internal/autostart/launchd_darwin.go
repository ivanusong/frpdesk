//go:build darwin

// macOS 登录启动项：launchd agent plist 写入 ~/Library/LaunchAgents/。
// launchd 登录时自动加载该目录内全部 plist（RunAtLoad 即启动），无需额外注册命令；
// 注册动作只写文件，不在当前会话 bootstrap，也不在注销时 bootout——对由 launchd
// 启动的本应用发控制命令会终止自身，且隔离测试会误触真实任务；仅删除 plist 即可
// 保证下次登录不再启动。
package autostart

import (
	"os"
	"path/filepath"

	"frpdesk/internal/i18n"
)

// launchAgentsDir 当前用户的 LaunchAgents 真实目录。
func launchAgentsDir() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", i18n.LErrWrap("err.autostartDir", err)
	}
	return filepath.Join(home, "Library", "LaunchAgents"), nil
}

// launchSpec darwin 注册体：LaunchAgents 目录下的 <AppID>.plist。
var launchSpec = registerFile{
	location: func() (string, error) {
		dir, err := registeredDir(launchAgentsDir)
		if err != nil {
			return "", err
		}
		return filepath.Join(dir, AppID+".plist"), nil
	},
	content:   launchdPlistXML,
	enabledAt: launchdPlistEnabled,
}

func statusImpl(exe string) (bool, error) { return launchSpec.status(exe) }

func enableImpl(exe string) error { return launchSpec.enable(exe) }

// disableImpl 仅移除 plist（幂等）：launchd 每次登录时按目录现状加载，文件不在
// 即不会启动；不对当前会话的 launchd 任务发任何控制命令。
func disableImpl() error { return launchSpec.disable() }
