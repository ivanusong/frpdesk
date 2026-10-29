package main

import (
	"path/filepath"
	"testing"

	"frpdesk/internal/configstore"
)

func TestRuntimeInfoDesktopMode(t *testing.T) {
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
