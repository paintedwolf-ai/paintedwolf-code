package promptloop

import (
	"context"
	"errors"
	"testing"

	"github.com/lycaon/lycaon/pkg/api"
)

func TestEvidenceCommitFailureStopsEnrichment(t *testing.T) {
	failure := errors.New("evidence store unavailable")
	loop := NewPromptLoop(PromptLoopDeps{
		Tools: ToolsDeps{
			CommitEvidenceToolResult: func(context.Context, string, *api.Session, string, map[string]any, string, string) (string, string, error) {
				return "", "", failure
			},
		},
	})
	message := api.Message{ID: "result", Content: "observed bytes"}
	history := []api.Message{message}
	_, err := loop.Batch.enrichCommittedToolRow(t.Context(), "session", &api.Session{ID: "session"}, history,
		"read", map[string]any{"path": "data.json"}, message.ID, true, &promptLoopTurnState{})
	if !errors.Is(err, failure) {
		t.Fatalf("evidence failure = %v, want store failure", err)
	}
	if history[0].Content != message.Content || len(history[0].EvidenceHandles) != 0 {
		t.Fatal("failed evidence commit changed the stored observation")
	}
}
