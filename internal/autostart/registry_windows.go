//go:build windows

// Windows 登录启动项：HKCU\Software\Microsoft\Windows\CurrentVersion\Run 注册表值
// （仅当前用户，无需管理员权限）。值体为带引号的完整程序路径，空格路径安全。
package autostart

import (
	"errors"

	"golang.org/x/sys/windows/registry"
)

// runKeyPath HKCU 登录启动项注册表键路径。
const runKeyPath = `Software\Microsoft\Windows\CurrentVersion\Run`

// isOpenKeyMissing 注册表键不存在（Run 键极少数被清空的场景）：等价未注册。
func isOpenKeyMissing(err error) bool {
	return errors.Is(err, registry.ErrNotExist)
}

func statusImpl(exe string) (bool, error) {
	k, err := registry.OpenKey(registry.CURRENT_USER, runKeyPath, registry.QUERY_VALUE)
	if err != nil {
		if isOpenKeyMissing(err) {
			return false, nil
		}
		return false, err
	}
	defer k.Close()
	value, _, err := k.GetStringValue(AppID)
	if err != nil {
		if isOpenKeyMissing(err) {
			return false, nil // 值不存在 = 未注册
		}
		return false, err
	}
	return windowsRunValueMatches(value, exe), nil
}

func enableImpl(exe string) error {
	// CreateKey：Run 键极少数被清空时自动创建（HKCU 当前用户权限即可，无需管理员）；
	// 键已存在时等价打开，幂等
	k, _, err := registry.CreateKey(registry.CURRENT_USER, runKeyPath, registry.QUERY_VALUE|registry.SET_VALUE)
	if err != nil {
		return err
	}
	defer k.Close()
	// 幂等：已注册且指向当前程序时 no-op（不重写，保留原值时间戳）
	if value, _, err := k.GetStringValue(AppID); err == nil && windowsRunValueMatches(value, exe) {
		return nil
	}
	return k.SetStringValue(AppID, windowsRunValue(exe))
}

func disableImpl() error {
	k, err := registry.OpenKey(registry.CURRENT_USER, runKeyPath, registry.SET_VALUE)
	if err != nil {
		if isOpenKeyMissing(err) {
			return nil
		}
		return err
	}
	defer k.Close()
	if err := k.DeleteValue(AppID); err != nil && !isOpenKeyMissing(err) {
		return err // 值不存在 = 未注册，幂等 no-op
	}
	return nil
}
