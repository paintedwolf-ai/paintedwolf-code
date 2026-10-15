package promptloop

import (
	"context"
	"testing"

	"github.com/lycaon/lycaon/internal/guidance"
	"github.com/lycaon/lycaon/pkg/api"
)

func TestCitationOnlyRetryStitchesPersistedCloseoutDraftBeforeGuard(t *testing.T) {
	const pinned = "## Recon report\n\nThe backend manages session lifecycle."
	loop := NewPromptLoopForTest(PromptLoopDeps{
		Closeout: CloseoutDeps{
			CloseoutStallState: func(_ context.Context, sessionID string) guidance.RetainedCloseout {
				if sessionID != "session-1" {
					return guidance.RetainedCloseout{}
				}
				return guidance.RetainedCloseout{Active: true, Attempt: 1, PrevKey: guidance.SynthHandleNotInLegsCode, Drafted: pinned, ForcedBy: []string{guidance.SynthHandleNotInLegsCode}}
			},
		},
	})
	const trailer = "```json\n{\"cited_evidence\":[{\"evidence\":\"leg-1:list#1\"}],\"cited_urls\":[],\"artifact_ids\":[]}\n```"
	history := []api.Message{{ID: "draft-1", Role: api.MessageRoleAssistant, Content: trailer}}
	prepared, _ := loop.Tools.maybeCoerceCloseoutContent(
		context.Background(), history, "session-1", "implement_synthesis", trailer, "draft-1",
	)
	report, ok := guidance.ParseCoordinatorCompletionReport(prepared)
	if !ok {
		t.Fatalf("stitched retry did not become a closeout envelope: %q", prepared)
	}
	if report.Synthesis != pinned || len(report.CitedEvidence) != 1 || report.CitedEvidence[0].Evidence != "leg-1:list#1" {
		t.Fatalf("stitched report = %+v", report)
	}
	if history[0].Content != prepared {
		t.Fatalf("history kept unstiched citation fence: %q", history[0].Content)
	}
	inactive := NewPromptLoopForTest(PromptLoopDeps{
		Closeout: CloseoutDeps{
			CloseoutStallState: func(context.Context, string) guidance.RetainedCloseout {
				return guidance.RetainedCloseout{Drafted: pinned}
			},
		},
	})
	if got, _ := inactive.Tools.maybeCoerceCloseoutContent(context.Background(), nil, "session-1", "implement_synthesis", trailer, ""); got != trailer {
		t.Fatalf("inactive stall stitched stale draft: %q", got)
	}
}
