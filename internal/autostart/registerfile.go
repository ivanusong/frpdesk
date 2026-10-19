package autostart

import (
	"os"
	"path/filepath"
)

// registerFile 「写注册文件」型平台（macOS LaunchAgents plist / Linux XDG autostart
// 桌面项）的共用实现：注册 = 原子写入一份固定文件；真实状态 = 文件存在且内容指向
// 当前可执行文件。所有操作幂等；失败原样上抛，绝不静默。
type registerFile struct {
	// location 返回注册文件的完整路径（测试注入隔离目录）。
	location func() (string, error)
	// content 生成注册文件内容（exe 为当前可执行文件路径）。
	content func(exe string) string
	// enabledAt 判断已有文件内容是否表示「已注册且指向 exe」；损坏内容按未注册处理。
	enabledAt func(raw []byte, exe string) bool
}

// status 真实状态：注册文件存在且 enabledAt 判定指向当前程序。文件不存在 → 未注册；
// 其他读取失败（权限等）→ 错误上抛，不猜测。
func (f registerFile) status(exe string) (bool, error) {
	path, err := f.location()
	if err != nil {
		return false, err
	}
	raw, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	return f.enabledAt(raw, exe), nil
}

// enable 注册（幂等）：已注册且指向当前程序时不重写（保留文件原样）；
// 未注册或残留路径过期（应用被移动）时原子重写。
func (f registerFile) enable(exe string) error {
	path, err := f.location()
	if err != nil {
		return err
	}
	if raw, err := os.ReadFile(path); err == nil && f.enabledAt(raw, exe) {
		return nil
	}
	return writeFileAtomic(path, []byte(f.content(exe)), 0o644)
}

// disable 注销（幂等）：文件不存在时 no-op；存在则移除。
func (f registerFile) disable() error {
	path, err := f.location()
	if err != nil {
		return err
	}
	if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
		return err
	}
	return nil
}

// writeFileAtomic 原子写文件：同目录临时文件 + rename，避免留下写一半的注册文件
// （半份 plist/desktop entry 会被系统按损坏处理）。目录不存在时先创建。
func writeFileAtomic(path string, content []byte, perm os.FileMode) error {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(dir, "."+filepath.Base(path)+".tmp-*")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	cleanup := true
	defer func() {
		if cleanup {
			_ = os.Remove(tmpName) // 失败路径不留下临时文件
		}
	}()
	if _, err := tmp.Write(content); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := os.Chmod(tmpName, perm); err != nil {
		return err
	}
	if err := os.Rename(tmpName, path); err != nil {
		return err
	}
	cleanup = false
	return nil
}
