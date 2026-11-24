package coordinatorbatch

import (
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/lycaon/lycaon/internal/coordinator/surface"
	"github.com/lycaon/lycaon/internal/progress"
	"github.com/lycaon/lycaon/internal/session/workeroutcomes"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/pkg/api"
)

// IncidentReplayFixture captures one transcript and scaffold snapshot.
type IncidentReplayFixture struct {
	CaptureID       string `json:"capture_id"`
	ParentSessionID string `json:"parent_session_id"`
	Description     string `json:"description"`
	History         []api.Message
	LoopWakeState   struct {
		BatchPhase        string   `json:"batch_phase"`
		BatchSeq          int      `json:"batch_seq"`
		WorkersInFlight   int      `json:"workers_in_flight"`
		PendingOverlayIDs []string `json:"pending_overlay_ids"`
	} `json:"loop_wake_state"`
	ProgressRows []progress.Row `json:"progress_rows"`
}

type incidentReplayJSON struct {
	CaptureID       string          `json:"capture_id"`
	ParentSessionID string          `json:"parent_session_id"`
	Description     string          `json:"description"`
	History         []api.Message   `json:"history"`
	LoopWakeState   json.RawMessage `json:"loop_wake_state"`
	ProgressRows    []progress.Row  `json:"progress_rows"`
}

// Load reads the incident replay fixture from disk.
func Load(t *testing.T) IncidentReplayFixture {
	t.Helper()
	path := filepath.Join(fixtureDir(t), "batch_wake_replay.json")
	raw, err := os.ReadFile(path)
	testutil.FailErr(t, "read incident replay fixture", err)

	var wire incidentReplayJSON
	testutil.FailErr(t, "unmarshal incident replay fixture", json.Unmarshal(raw, &wire))

	var out IncidentReplayFixture
	out.CaptureID = wire.CaptureID
	out.ParentSessionID = wire.ParentSessionID
	out.Description = wire.Description
	out.History = wire.History
	out.ProgressRows = wire.ProgressRows
	testutil.FailErr(t, "unmarshal loop_wake_state", json.Unmarshal(wire.LoopWakeState, &out.LoopWakeState))
	return out
}

// ImplementSessionState returns the scaffold snapshot from the fixture with wrapup gates computed.
func (f IncidentReplayFixture) ImplementSessionState() surface.ImplementSessionState {
	state := surface.ImplementSessionState{
		BatchPhase:        f.LoopWakeState.BatchPhase,
		BatchSeq:          f.LoopWakeState.BatchSeq,
		WorkersInFlight:   f.LoopWakeState.WorkersInFlight,
		PendingOverlayIDs: append([]string(nil), f.LoopWakeState.PendingOverlayIDs...),
	}
	snapshot := progress.NewSnapshot("", f.ProgressRows...)
	state.WrapupGatesLoaded = true
	state.BatchReadyForSynthesis = workeroutcomes.BatchReadyForSynthesis(state, f.History, snapshot, true, true)
	state.OpenRepairSinceUserIntent = workeroutcomes.OpenRepairSinceUserIntent(f.History, false)
	state.ProgressOpenCount = snapshot.OpenStepCount()
	state.ProgressMissing = snapshot.Missing()
	return state
}

func fixtureDir(t *testing.T) string {
	t.Helper()
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("runtime.Caller failed")
	}
	return filepath.Dir(file)
}
