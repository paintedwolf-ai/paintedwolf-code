package app

import (
	"testing"

	"github.com/lycaon/lycaon/internal/configlayout"
	"github.com/lycaon/lycaon/internal/db"
	"github.com/lycaon/lycaon/internal/testdbseed"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/pkg/api"
)

func TestServeRecoverySettlesPersistedBusySession(t *testing.T) {
	t.Setenv("LYCAON_LLM_MOCK", "1")
	t.Setenv("LYCAON_API_TOKEN", "test-token")
	t.Setenv("LYCAON_TEST", "1")
	cfg := testBuildConfig(t, configlayout.FindModuleRoot())
	seed, err := db.Open(cfg.DBPath)
	testutil.FailErr(t, "open first host store", err)
	testdbseed.InsertSession(t, seed, "interrupted", testdbseed.DefaultProjectID)
	_, err = seed.ExecContext(t.Context(), "UPDATE sessions SET status = 'busy' WHERE id = 'interrupted'")
	testutil.FailErr(t, "persist active session", err)
	testutil.FailErr(t, "close first host store", seed.Close())
	host, err := Build(t.Context(), cfg)
	testutil.FailErr(t, "reopen host", err)
	t.Cleanup(func() { _ = host.Close() })
	stop, err := host.StartBackgroundWorkers(t.Context())
	testutil.FailErr(t, "start recovered host", err)
	t.Cleanup(stop)
	var status api.SessionStatus
	testutil.FailErr(t, "read recovered status", host.DB.QueryRowContext(t.Context(), "SELECT status FROM sessions WHERE id = 'interrupted'").Scan(&status))
	if status != api.SessionStatusIdle {
		t.Fatalf("recovered status = %q", status)
	}
}
