package publication

import (
	"context"
	"testing"

	"github.com/lycaon/lycaon/pkg/api"
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
