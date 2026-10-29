package configstore

import (
	"os"
	"path/filepath"
	"testing"
)

func TestConfigPathInDir(t *testing.T) {
	dir := t.TempDir()
	got, err := ConfigPathInDir(dir)
	if err != nil {
		t.Fatalf("ConfigPathInDir(%q): %v", dir, err)
	}
	if want := filepath.Join(dir, configFileName); got != want {
		t.Fatalf("ConfigPathInDir(%q) = %q, want %q", dir, got, want)
	}

	// 相对路径解析为绝对路径（配置文件始终落在给定目录内）
	t.Chdir(dir)
	got, err = ConfigPathInDir("nested")
	if err != nil {
		t.Fatalf("ConfigPathInDir(relative): %v", err)
	}
	cwd, err := os.Getwd()
	if err != nil {
		t.Fatalf("Getwd: %v", err)
	}
	if want := filepath.Join(cwd, "nested", configFileName); got != want {
		t.Fatalf("relative path = %q, want %q", got, want)
	}

	// 首尾空白会被裁剪；空目录明确报错，避免把配置写到进程当前目录
	if _, err := ConfigPathInDir("   "); err == nil {
		t.Fatal("ConfigPathInDir(空白) 应返回错误")
	}
}

func TestDefaultConfigPath(t *testing.T) {
	got, err := DefaultConfigPath()
	if err != nil {
		t.Fatalf("DefaultConfigPath: %v", err)
	}
	if filepath.Base(got) != configFileName {
		t.Fatalf("DefaultConfigPath = %q, want basename %q", got, configFileName)
	}
	if !filepath.IsAbs(got) {
		t.Fatalf("DefaultConfigPath = %q, want absolute path", got)
	}
}
