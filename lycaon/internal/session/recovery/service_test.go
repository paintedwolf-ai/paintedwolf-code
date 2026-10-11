package recovery

import (
	"errors"
	"github.com/lycaon/lycaon/internal/session/promptinput"
	"github.com/lycaon/lycaon/pkg/api"
	"testing"
)

func TestMalformedRecoveryIsNotStale(t *testing.T) {
	service := New(nil)
	for _, input := range []promptinput.Input{
		{},
		{Recovery: &api.PromptRecovery{Action: "unknown"}},
		{Recovery: &api.PromptRecovery{Action: "continue"}, ArtifactIDs: []string{"artifact"}},
	} {
		_, err := service.Prepare(t.Context(), "chat", input)
		if !errors.Is(err, ErrInvalid) || errors.Is(err, ErrStale) {
			t.Fatalf("invalid recovery = %v", err)
		}
	}
}

func TestStaleReasonSurvivesDiagnosticWrapping(t *testing.T) {
	for _, reason := range []Reason{TurnRunning, PromptPending, TurnCompleted, TurnMissing, TranscriptChanged, RetryHasProgress} {
		err := staleRecovery(reason, "display copy may change")
		var stale *StaleError
		if !errors.As(err, &stale) || !errors.Is(err, ErrStale) || stale.Reason != reason {
			t.Fatalf("structured reason lost: %v", err)
		}
	}
}
