package publication

import (
	"context"
	"github.com/lycaon/lycaon/pkg/api"
	"testing"
)

// Publication reads run state without writing to it.
func TestPublishedRunDoesNotMutateTheLiveRun(t *testing.T) {
	publisher := &Runs{Projector: mutatingRunProjector{}}

	live := &api.WorkflowRun{
		ID:              "run-1",
		SessionID:       "sess-1",
		WorkflowID:      "implement",
		WorkflowVersion: "1.0.0",
		Status:          api.WorkflowRunStatusRunning,
		CurrentPhase:    "work",
	}
	projected := publisher.publishedRun(context.Background(), live)

	if projected == live {
		t.Fatal("publishedRun returned the caller's run — publication must project a copy")
	}
	if live.UI != nil {
		t.Fatalf("publishedRun attached ui to the live run: %+v", live.UI)
	}
}

type mutatingRunProjector struct{}

func (mutatingRunProjector) AttachRunUI(_ context.Context, run *api.WorkflowRun) error {
	run.UI = &api.WorkflowRunUi{CurrentPhaseLabel: "Work"}
	return nil
}

func TestPhaseAdvanceDeliversOnlyRunningRunTransition(t *testing.T) {
	var delivered []string
	publisher := &Runs{OnPhaseAutoAdvanced: func(ctx context.Context, sessionID, runID, previous, current string) {
		if ctx != t.Context() {
			t.Fatal("phase transition lost its publication context")
		}
		delivered = []string{sessionID, runID, previous, current}
	}}
	run := &api.WorkflowRun{ID: "run", SessionID: "session", ProjectID: "project", Status: api.WorkflowRunStatusRunning, CurrentPhase: "review"}
	publisher.PublishPhaseAdvanced(t.Context(), run, "work")
	if len(delivered) != 4 || delivered[0] != "session" || delivered[1] != "run" || delivered[2] != "work" || delivered[3] != "review" {
		t.Fatalf("transition=%v", delivered)
	}
	delivered = nil
	run.Status = api.WorkflowRunStatusCompleted
	publisher.PublishPhaseAdvanced(t.Context(), run, "review")
	if delivered != nil {
		t.Fatalf("completed run was resumed: %v", delivered)
	}
}
