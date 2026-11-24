package main

import (
	"path/filepath"
	"testing"

	"frpdesk/internal/configstore"
	"frpdesk/internal/i18n"
)

func TestRuntimeInfoDesktopMode(t *testing.T) {
	prevLang := i18n.Current()
	t.Cleanup(func() { i18n.Set(prevLang) })

	store := configstore.NewAtPath(filepath.Join(t.TempDir(), "config.json"))
	svc := newAppService(store, appModeDesktop)
	info := svc.RuntimeInfo()
	if info.Mode != string(appModeDesktop) {
		t.Fatalf("RuntimeInfo.Mode = %q, want %q", info.Mode, appModeDesktop)
	}
	if info.Version != currentVersion {
		t.Fatalf("RuntimeInfo.Version = %q, want %q", info.Version, currentVersion)
	}
}
