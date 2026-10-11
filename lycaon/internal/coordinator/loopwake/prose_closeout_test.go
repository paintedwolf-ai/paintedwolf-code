package loopwake_test

import (
	"context"
	"github.com/lycaon/lycaon/internal/coordinator/loopwake"
	"github.com/lycaon/lycaon/internal/coordinator/surface"
	"github.com/lycaon/lycaon/pkg/api"
	"testing"
)

func TestHostWakeProseLifecycle(t *testing.T) {
	for _, status := range []api.DraftStatus{"", api.DraftStatusCommitted, api.DraftStatusWithdrawn, api.DraftStatusRejected} {
		t.Run(string(status), func(t *testing.T) {
			history := []api.Message{
				{Role: api.MessageRoleUser, Visibility: api.MessageVisibilityTranscript, Content: "Explain the selected source."},
				{Role: api.MessageRoleAssistant, Kind: api.MessageKindDraft, Visibility: api.MessageVisibilityTranscript, DraftStatus: status, Content: "A read-only explanation."},
			}
			want := status != api.DraftStatusCommitted
			if got := loopwake.HostWakeActionableFromHistory(history, idleImplementSessionState()); got != want {
				t.Fatalf("draft status %q actionable=%v, want %v", status, got, want)
			}
			for _, next := range []api.Message{
				{Role: api.MessageRoleUser, Visibility: api.MessageVisibilityTranscript, Content: "Explain another selection."},
				{Role: api.MessageRoleAssistant, WorkerSummary: &api.WorkerSummaryMeta{WorkerID: "later-worker", Status: api.WorkerSummaryStatusComplete}},
			} {
				if !loopwake.HostWakeActionableFromHistory(append(history, next), idleImplementSessionState()) {
					t.Fatalf("new intent or worker evidence after %q was suppressed", status)
				}
			}
			if !loopwake.HostWakeActionableFromHistory(history, surface.ImplementSessionState{PendingOverlayIDs: []string{"pending"}}) {
				t.Fatalf("pending overlay after %q was suppressed", status)
			}
		})
	}
}

func TestHostWakeCommittedProseKeepsWorkflowObligations(t *testing.T) {
	history := []api.Message{{Role: api.MessageRoleAssistant, Kind: api.MessageKindDraft, Visibility: api.MessageVisibilityTranscript, DraftStatus: api.DraftStatusCommitted}}
	actionable := loopwake.BuildHostWakeActionable(loopwake.HostWakeActionableDeps{
		GetMessages:             func(context.Context, string) ([]api.Message, error) { return history, nil },
		WorkflowObligationsOpen: func(context.Context, string) bool { return true },
	})
	if !actionable(t.Context(), loopwake.HostWakeActionableInput{SessionID: "selection"}) {
		t.Fatal("committed prose suppressed an outstanding workflow obligation")
	}
}
