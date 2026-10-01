package coordinatorcoordinationplane

import (
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/lycaon/lycaon/internal/testutil"
)

// IncidentReplayFixture is the board-freshness replay shape.
type IncidentReplayFixture struct {
	CaptureID            string `json:"capture_id"`
	Description          string `json:"description"`
	PriorRunID           string `json:"prior_run_id"`
	NewRunID             string `json:"new_run_id"`
	PriorGoal            string `json:"prior_goal"`
	UserPrompt           string `json:"user_prompt"`
	StaleProgressContent string `json:"stale_progress_content"`
}

// Load reads the coordination-plane incident replay fixture.
func Load(t *testing.T) IncidentReplayFixture {
	t.Helper()
	path := filepath.Join(fixtureDir(t), "progress_refresh_replay.json")
	// #nosec G304 -- path is fixed beside this test helper.
	raw, err := os.ReadFile(path)
	testutil.FailErr(t, "read incident replay fixture", err)

	var out IncidentReplayFixture
	testutil.FailErr(t, "unmarshal incident replay fixture", json.Unmarshal(raw, &out))
	return out
}

func fixtureDir(t *testing.T) string {
	t.Helper()
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("runtime.Caller failed")
	}
	return filepath.Dir(file)
}
