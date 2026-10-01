package app

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/lycaon/lycaon/internal/backup"
	"github.com/lycaon/lycaon/internal/db"
	"github.com/lycaon/lycaon/internal/localdata"
	"github.com/lycaon/lycaon/internal/testutil"
)

// A staged restore that cannot be applied leaves its marker on disk and fails
// the same way on every relaunch. Without recovery mode the person sees the
// generic offline stop, whose only actions are retry and report bundle, and no
// retry can ever clear the marker.
func TestBuildRecoveryModeWhenAStagedRestoreCannotBeApplied(t *testing.T) {
	t.Setenv("LYCAON_LLM_MOCK", "1")
	t.Setenv("LYCAON_API_TOKEN", "test-token")
	t.Setenv("LYCAON_TEST", "1")

	cfg := recoveryTestConfig(t)
	configDir := filepath.Dir(cfg.DBPath)
	store, err := db.Open(cfg.DBPath)
	testutil.FailErr(t, "seed store", err)
	_, err = backup.StageFreshStart(t.Context(), backup.FreshStartOpts{
		ConfigDir: configDir, DBPath: cfg.DBPath, SQLDB: store, AppVersion: "test",
	})
	testutil.FailErr(t, "stage fresh start", err)
	testutil.FailErr(t, "close store", store.Shutdown(t.Context()))

	// The latch the fresh start deletes is a non-empty directory, so the apply
	// cannot finish.
	onboarding := filepath.Join(configDir, filepath.FromSlash(localdata.FirstRunOnboardingRelPath()))
	testutil.FailErr(t, "seed blocked delete", os.MkdirAll(onboarding, 0o700))
	testutil.FailErr(t, "seed blocked delete child",
		os.WriteFile(filepath.Join(onboarding, "child"), []byte("x"), 0o600))

	app, err := Build(t.Context(), cfg)
	testutil.FailErr(t, "Build recovery", err)
	t.Cleanup(func() { _ = app.Close() })

	body := getHealth(t, app.Server)
	if body["status"] != "recovery" {
		t.Fatalf("health = %#v, want recovery mode so restore and start fresh are reachable", body)
	}
}
