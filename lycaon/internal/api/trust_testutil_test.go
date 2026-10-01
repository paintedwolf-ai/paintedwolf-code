package api

import (
	"github.com/lycaon/lycaon/internal/configlayout"
	"os"
	"path/filepath"
	"testing"

	"github.com/lycaon/lycaon/internal/project"
	"github.com/lycaon/lycaon/internal/session/store"
	"github.com/lycaon/lycaon/internal/settings"
	"github.com/lycaon/lycaon/internal/settingsoverlay"
	"github.com/lycaon/lycaon/internal/testutil"
)

func trustSettingsServer(t *testing.T, opts ...testDeps) *Server {
	t.Helper()
	cfg := t.TempDir()
	t.Setenv("LYCAON_CONFIG_DIR", cfg)
	project.SetDefaultOpenPolicy(project.TestOpenPolicy())

	svc, err := settings.NewService()
	testutil.FailErr(t, "settings.NewService", err)

	return newServerForTest(t, Dependencies{
		Store: store.NewMemory(), Projects: project.NewMemoryRegistry(), Settings: svc,
		ModuleRoot: configlayout.FindModuleRoot(),
	}, append([]testDeps{withExtensionOwner(t)}, opts...)...)
}

func writeOverlay(t *testing.T, root, basename, content string) {
	t.Helper()
	dir := filepath.Join(root, settingsoverlay.DirName())
	testutil.FailErr(t, "mkdir overlay", os.MkdirAll(dir, 0o755))
	testutil.FailErr(t, "write "+basename,
		os.WriteFile(filepath.Join(dir, basename), []byte(content), 0o644))
}
