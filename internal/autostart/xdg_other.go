//go:build !darwin && !windows

// Linux/其他 Unix 登录启动项：XDG autostart 桌面项写入
// os.UserConfigDir()/autostart/。主流桌面环境（GNOME/KDE 等）登录时自动执行
// 该目录内的 .desktop 项；无桌面环境的纯会话不消费该目录，属平台固有能力边界。
package autostart

import (
	"os"
	"path/filepath"

	"frpdesk/internal/i18n"
)

// xdgAutostartDir 当前用户的 XDG autostart 真实目录。
func xdgAutostartDir() (string, error) {
	dir, err := os.UserConfigDir()
	if err != nil {
		return "", i18n.LErrWrap("err.autostartDir", err)
	}
	return filepath.Join(dir, "autostart"), nil
}

// xdgSpec XDG 注册体：autostart 目录下的 <AppID>.desktop。
var xdgSpec = registerFile{
	location: func() (string, error) {
		dir, err := registeredDir(xdgAutostartDir)
		if err != nil {
			return "", err
		}
		return filepath.Join(dir, AppID+".desktop"), nil
	},
	content:   xdgDesktopEntry,
	enabledAt: xdgDesktopEntryEnabled,
}

func statusImpl(exe string) (bool, error) { return xdgSpec.status(exe) }

func enableImpl(exe string) error { return xdgSpec.enable(exe) }

func disableImpl() error { return xdgSpec.disable() }
