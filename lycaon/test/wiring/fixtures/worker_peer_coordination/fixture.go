package workerpeercoord

import (
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/lycaon/lycaon/internal/testutil"
)

// ParallelWorkersReplayFixture is a synthetic parallel-worker replay.
type ParallelWorkersReplayFixture struct {
	CaptureID           string `json:"capture_id"`
	FixtureName         string `json:"fixture_name"`
	ParentSessionID     string `json:"parent_session_id"`
	Description         string `json:"description"`
	NoteSummary         string `json:"note_summary"`
	NoteRef             string `json:"note_ref"`
	PriorRunNoteSummary string `json:"prior_run_note_summary"`
	OverlayReadPath     string `json:"overlay_read_path"`
	PromoteOverlayID    string `json:"promote_overlay_id"`
	WorkerA             struct {
		JobID          string `json:"job_id"`
		ChildSessionID string `json:"child_session_id"`
		OverlaySuffix  string `json:"overlay_suffix"`
	} `json:"worker_a"`
	WorkerB struct {
		JobID          string `json:"job_id"`
		ChildSessionID string `json:"child_session_id"`
		OverlaySuffix  string `json:"overlay_suffix"`
	} `json:"worker_b"`
}

// Load reads the parallel-workers replay fixture.
func Load(t *testing.T) ParallelWorkersReplayFixture {
	t.Helper()
	path := filepath.Join(fixtureDir(t), "parallel_workers_replay.json")
	// #nosec G304 -- path is fixed beside this test helper.
	raw, err := os.ReadFile(path)
	testutil.FailErr(t, "read parallel workers replay fixture", err)
	var out ParallelWorkersReplayFixture
	testutil.FailErr(t, "unmarshal parallel workers replay fixture", json.Unmarshal(raw, &out))
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
