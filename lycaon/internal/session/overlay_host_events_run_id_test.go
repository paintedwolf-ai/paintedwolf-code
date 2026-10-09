package session

import (
	"context"
	"testing"

	"github.com/lycaon/lycaon/internal/hostmarker"
	"github.com/lycaon/lycaon/internal/llm"
	"github.com/lycaon/lycaon/internal/session/store"
	"github.com/lycaon/lycaon/internal/settings"
	"github.com/lycaon/lycaon/internal/testdbseed"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/tools"
	"github.com/lycaon/lycaon/pkg/api"
)

// activeRunView supplies a fixed active run for event attribution.
type activeRunView struct {
	recordingWorkflowView
	run *api.WorkflowRun
}

func (v *activeRunView) GetActive(ctx context.Context, sessionID string) (*api.WorkflowRun, error) {
	v.record("GetActive")
	return v.run, nil
}

func newHostEventManager(t *testing.T, run *api.WorkflowRun) (*Manager, string) {
	t.Helper()
	store := store.NewMemory()
	mgr := NewManager(store, llm.NewMockProvider(nil), tools.NewStubRegistry(), settings.DefaultSessionLimits())
	mgr.SetWorkflowSessionView(&activeRunView{run: run}, nil)
	ctx := context.Background()
	sess, err := store.Create(ctx, api.CreateSessionRequest{}, testdbseed.DefaultProjectID)
	testutil.FailErr(t, "store.Create failed", err)
	return mgr, sess.ID
}

// TestAppendHostEventStampedWithActiveRun verifies that an overlay
// promote/reject event appended while a run is active carries that run's id at
// creation, so it lands in its span with no client attribution fallback.
func TestAppendHostEventStampedWithActiveRun(t *testing.T) {
	const runID = "run-host-1"
	mgr, sessionID := newHostEventManager(t, &api.WorkflowRun{ID: runID})

	if err := mgr.appendHostEvent(context.Background(), sessionID, hostmarker.OverlayPromoteEventPrefix, map[string]any{"overlay": "o1"}, ""); err != nil {
		testutil.FailErr(t, "appendHostEvent failed", err)
	}
	msgs, err := mgr.store.GetMessages(context.Background(), sessionID)
	testutil.FailErr(t, "GetMessages failed", err)
	var found bool
	for _, m := range msgs {
		if m.Role == api.MessageRoleTool && m.WorkflowRunID == runID {
			found = true
		}
	}
	if !found {
		t.Fatalf("no host-event tool row stamped with run id %q in %d rows", runID, len(msgs))
	}
}

// TestAppendHostEventUnstampedWhenNoActiveRun confirms the legitimate no-run
// case leaves workflow_run_id empty rather than inventing one.
func TestAppendHostEventUnstampedWhenNoActiveRun(t *testing.T) {
	mgr, sessionID := newHostEventManager(t, nil)

	if err := mgr.appendHostEvent(context.Background(), sessionID, hostmarker.OverlayRejectEventPrefix, map[string]any{"overlay": "o2"}, ""); err != nil {
		testutil.FailErr(t, "appendHostEvent failed", err)
	}
	msgs, err := mgr.store.GetMessages(context.Background(), sessionID)
	testutil.FailErr(t, "GetMessages failed", err)
	for _, m := range msgs {
		if m.Role == api.MessageRoleTool && m.WorkflowRunID != "" {
			t.Fatalf("host-event workflow_run_id = %q want empty (no active run)", m.WorkflowRunID)
		}
	}
}
