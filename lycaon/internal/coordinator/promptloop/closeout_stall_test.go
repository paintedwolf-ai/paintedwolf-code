package promptloop

import (
	"context"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/guidance"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/pkg/api"
)

func TestCloseoutFuseTrippedGuards(t *testing.T) {
	tripped := true
	l := NewPromptLoopForTest(PromptLoopDeps{
		NoteCoordinatorToolTurn: func(_ context.Context, _ string) bool { return tripped },
	})
	ctx := context.Background()

	if !l.closeoutFuseTripped(ctx, &api.Session{ID: "s1"}, "s1", "implement_investigate") {
		t.Fatal("coordinator closeout surface with a tripped dep should trip")
	}
	if l.closeoutFuseTripped(ctx, &api.Session{ID: "s1"}, "s1", "implement") {
		t.Fatal("non-closeout surface must never trip the fuse")
	}
	worker := &api.Session{ID: "w1", ParentSessionID: "s1"}
	if l.closeoutFuseTripped(ctx, worker, "w1", "implement_investigate") {
		t.Fatal("worker child must never trip the coordinator fuse")
	}

	nofuse := NewPromptLoopForTest(PromptLoopDeps{})
	if nofuse.closeoutFuseTripped(ctx, &api.Session{ID: "s1"}, "s1", "implement_investigate") {
		t.Fatal("no fuse dep wired → must never trip")
	}
}

func TestEmitStalledCloseoutUsesRetainedDraftAndClears(t *testing.T) {
	var gotDrafted string
	var gotForcedBy []string
	var cleared bool
	l := NewPromptLoopForTest(PromptLoopDeps{
		CloseoutStallState: func(_ context.Context, _ string) guidance.RetainedCloseout {
			return guidance.RetainedCloseout{Active: true, Attempt: 3, PrevKey: guidance.InvestCitationsRequiredCode, Drafted: "Root cause: missing backoff in the cache warmer.", ForcedBy: []string{guidance.InvestCitationsRequiredCode}}
		},
		ClearCloseoutStall: func(_ context.Context, _ string) { cleared = true },
		AssembleLedgerCloseout: func(_ context.Context, _, _ string, forcedBy []string, drafted string, retryCount int) (guidance.CoordinatorCompletionReport, *api.CitationGrounding) {
			gotDrafted = drafted
			gotForcedBy = forcedBy
			return guidance.CoordinatorCompletionReport{Synthesis: drafted},
				&api.CitationGrounding{HostAssembled: true, Traced: false, RetryCount: retryCount}
		},
	})
	st := &promptLoopTurnState{draftSlotID: "slot-1", draftSlotAppended: true, closeoutRetry: closeoutRetryState{attempt: 3}}
	history := []api.Message{{ID: "slot-1", Role: api.MessageRoleAssistant}}

	out, err := l.emitStalledCloseout(context.Background(), &api.Session{ID: "s1"}, "s1", "", "implement_investigate", st, history)
	testutil.FailErr(t, "emitStalledCloseout", err)

	if !out.committed {
		t.Fatal("stall exit should commit the assembled closeout")
	}
	if gotDrafted != "Root cause: missing backoff in the cache warmer." {
		t.Fatalf("assembler got drafted=%q, want the retained synthesis", gotDrafted)
	}
	if len(gotForcedBy) != 1 || gotForcedBy[0] != guidance.InvestCitationsRequiredCode {
		t.Fatalf("assembler got forcedBy=%v, want the retained citation code", gotForcedBy)
	}
	if !cleared {
		t.Fatal("committed stall exit must clear the stall record")
	}
	if !strings.Contains(out.assistantMsg.Content, "missing backoff") {
		t.Fatalf("committed content must retain the model prose, got %q", out.assistantMsg.Content)
	}
}

func TestCitationOnlyRetryStitchesPersistedCloseoutDraftBeforeGuard(t *testing.T) {
	const pinned = "## Recon report\n\nThe backend manages session lifecycle."
	loop := NewPromptLoopForTest(PromptLoopDeps{
		CloseoutStallState: func(_ context.Context, sessionID string) guidance.RetainedCloseout {
			if sessionID != "session-1" {
				return guidance.RetainedCloseout{}
			}
			return guidance.RetainedCloseout{Active: true, Attempt: 1, PrevKey: guidance.SynthHandleNotInLegsCode, Drafted: pinned, ForcedBy: []string{guidance.SynthHandleNotInLegsCode}}
		},
	})
	const trailer = "```json\n{\"cited_evidence\":[{\"evidence\":\"leg-1:list#1\"}],\"cited_urls\":[],\"artifact_ids\":[]}\n```"
	history := []api.Message{{ID: "draft-1", Role: api.MessageRoleAssistant, Content: trailer}}
	prepared, _ := loop.maybeCoerceCloseoutContent(
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
		CloseoutStallState: func(context.Context, string) guidance.RetainedCloseout {
			return guidance.RetainedCloseout{Drafted: pinned}
		},
	})
	if got, _ := inactive.maybeCoerceCloseoutContent(context.Background(), nil, "session-1", "implement_synthesis", trailer, ""); got != trailer {
		t.Fatalf("inactive stall stitched stale draft: %q", got)
	}
}
