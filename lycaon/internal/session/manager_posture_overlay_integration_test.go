//go:build integration

package session

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/lycaon/lycaon/internal/llm"
	"github.com/lycaon/lycaon/internal/session/profiles"
	"github.com/lycaon/lycaon/internal/session/store"
	"github.com/lycaon/lycaon/internal/settings"
	"github.com/lycaon/lycaon/internal/settingsoverlay"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/tools"
	"github.com/lycaon/lycaon/pkg/api"
)

func TestManagerPostureOverlayIntegration(t *testing.T) {
	reg, err := profiles.LoadPostureRegistry()
	testutil.FailErr(t, "profiles.LoadPostureRegistry failed", err)
	dir := t.TempDir()
	overlayDir := filepath.Join(dir, settingsoverlay.DirName())
	if err := os.MkdirAll(overlayDir, 0o755); err != nil {
		testutil.FailErr(t, "create directory", err)
	}
	overlay := []byte(`
postures:
  vet:
    label: Audit
`)
	if err := os.WriteFile(filepath.Join(overlayDir, "postures.yaml"), overlay, 0o644); err != nil {
		testutil.FailErr(t, "write file", err)
	}

	mgr := NewManager(store.NewMemory(), llm.NewMockProvider(testMockConfig(t)), tools.NewStubRegistry(), settings.DefaultSessionLimits())
	mgr.Profiles.SetPostureRegistry(reg)
	if err := mgr.Profiles.WarmPostureOverlay(dir); err != nil {
		testutil.FailErr(t, "mgr.Profiles.WarmPostureOverlay failed", err)
	}

	// Project postures require project settings trust.
	projectID := RegisterProjectContextForTest(t, mgr, dir)
	sess := &api.Session{ID: "overlay", ProjectID: projectID, Posture: api.SessionPostureVet, WorkspacePath: dir}

	effective, err := mgr.Profiles.EffectivePostures(t.Context(), sess)
	testutil.FailErr(t, "mgr.Profiles.EffectivePostures failed", err)
	spec, err := effective.Get(api.SessionPostureVet)
	testutil.FailErr(t, "effective.Get failed", err)
	if spec.Label != "Audit" {
		t.Fatalf("effective label = %q", spec.Label)
	}
}

func TestWarmPostureOverlayRejectsUnknownPosture(t *testing.T) {
	reg, err := profiles.LoadPostureRegistry()
	testutil.FailErr(t, "profiles.LoadPostureRegistry failed", err)
	dir := t.TempDir()
	overlayDir := filepath.Join(dir, settingsoverlay.DirName())
	if err := os.MkdirAll(overlayDir, 0o755); err != nil {
		testutil.FailErr(t, "create directory", err)
	}
	if err := os.WriteFile(filepath.Join(overlayDir, "postures.yaml"), []byte(`
postures:
  plan:
    label: Plan
`), 0o644); err != nil {
		testutil.FailErr(t, "write file", err)
	}
	mgr := NewManager(store.NewMemory(), nil, nil, settings.DefaultSessionLimits())
	mgr.Profiles.SetPostureRegistry(reg)
	if err := mgr.Profiles.WarmPostureOverlay(dir); err == nil {
		t.Fatal("expected unknown overlay posture error")
	}
}
