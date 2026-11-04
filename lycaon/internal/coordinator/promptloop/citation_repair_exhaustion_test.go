package promptloop

import (
	"context"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/citationmd"
	"github.com/lycaon/lycaon/internal/evidence"
	"github.com/lycaon/lycaon/internal/guidance"
	"github.com/lycaon/lycaon/internal/oar"
	"github.com/lycaon/lycaon/internal/prompts/promptstest"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/pkg/api"
)

// markdownRead reads a fresh closeout, on a report phase when its fence is the
// document.
func markdownRead(markdown string, reportPhase bool) guidance.CloseoutRead {
	return guidance.ReadCloseoutReport(markdown, guidance.RetainedCloseout{}, reportPhase, citationmd.Options{})
}

// activeRetained is a repair cycle holding markdown as its retained draft.
func activeRetained(markdown string, attempt int, reportPhase bool, forcedBy ...string) guidance.RetainedCloseout {
	retained := guidance.RetainedCloseout{Active: true, Attempt: attempt, ForcedBy: forcedBy}
	retained.Retain(markdownRead(markdown, reportPhase).Draft)
	return retained
}

// AC-CLO-11, AC-CLO-18: exhaustion keeps the retained answer and its document,
// removes unresolved nodes from display, and stays honest about host assembly.
func TestCitationRepairExhaustionKeepsPinnedReport(t *testing.T) {
	guidance.SetGuidanceRenderer(promptstest.GuidanceRenderer(t))
	hints := loadCoordinatorTestHintConfig(t)
	const pinned = "The original marker was amber [read#1] [read#999]."
	retained := activeRetained(pinned+"\n\n```json\n"+`{"headline":"Original report","summary":"Historical observation","limits":["Inspection only."]}`+"\n```", 1, true, guidance.InvestHandleNotObservedCode)
	ledger := evidence.AssembleLedger([]evidence.Record{{Handle: "read#1", Kind: "read", Shape: evidence.ShapeFileRegion, Path: "notes.txt", Body: []string{"Release marker: amber"}}})
	var committed api.Message
	var drafts []string
	loop := NewPromptLoopForTest(PromptLoopDeps{
		Closeout: CloseoutDeps{
			HintConfig:                          hints,
			RejectFmt:                           guidance.NewStaticRejectFormatter(hints),
			MaxCloseoutCitationGroundingRetries: 1,
			EvidenceLedger:                      closeoutLedgerReader{ledger: ledger},
			CloseoutStallState:                  func(context.Context, string) guidance.RetainedCloseout { return retained },
			EvaluateCloseoutBlock: func(_ context.Context, _ *api.Session, gc *oar.GuardContext) (*oar.Decision, error) {
				return &oar.Decision{Code: guidance.InvestHandleNotObservedCode, Data: gc.RejectData[guidance.InvestHandleNotObservedCode]}, nil
			},
			AssembleLedgerCloseout: func(_ context.Context, _, surface string, codes []string, draft guidance.CloseoutDraft, retries int) (guidance.CoordinatorCompletionReport, *api.CitationGrounding) {
				return guidance.AssembleRetainedCloseout(evidence.CitationRoots{}, surface, guidance.CloseoutEvidence{Ledger: ledger}, draft, nil, codes[0], retries)
			},
		},
		Projection: ProjectionDeps{
			AppendMessages: func(context.Context, string, ...api.Message) error { return nil },
			AppendDraftVersion: func(_ context.Context, _, _, body, _ string) (int, error) {
				drafts = append(drafts, body)
				return 1, nil
			},
			UpdateMessage: func(_ context.Context, _, _ string, msg api.Message) error {
				if msg.Grounding != nil {
					committed = msg
				}
				return nil
			},
		},
	})
	assistant := api.Message{ID: "slot-1", Role: api.MessageRoleAssistant, Content: "[cite: c2 => read#999]"}
	state := &promptLoopTurnState{coordinatorFrame: testReportFrame(),
		draftSlotID: "slot-1", draftSlotAppended: true, closeoutRetry: closeoutRetryState{attempt: 1},
		turnTools: []string{"read"}, history: []api.Message{{ID: "u1", Role: api.MessageRoleUser, Content: "Inspect the marker"}, assistant},
	}
	read := guidance.ReadCloseoutReport(assistant.Content, retained, true, citationmd.Options{})
	out, err := loop.Closeout.applyAcceptedCloseoutReport(t.Context(), &api.Session{ID: "s1", WorkspacePath: t.TempDir()}, "s1", "", "implement_investigate", state, assistant, read)
	testutil.FailErr(t, "finish exhausted citation repair", err)
	if !out.breakLoop || !state.proseFinishDelivered || committed.Content != "The original marker was amber [notes.txt](notes.txt)." {
		t.Fatalf("exhaustion did not preserve the report body: outcome=%+v content=%q", out, committed.Content)
	}
	// AC-CLO-11: the rejected raw draft stays inspectable after exhaustion.
	if len(drafts) == 0 || !strings.Contains(strings.Join(drafts, "\n"), "read#999") {
		t.Fatalf("raw rejected draft not retained: %q", drafts)
	}
	meta := committed.CompletionReport
	if meta == nil || meta.Headline != "Original report" || meta.Summary != "Historical observation" || len(meta.Limits) != 1 || meta.Limits[0] != "Inspection only." {
		t.Fatalf("exhaustion lost report metadata: %+v", meta)
	}
	g := committed.Grounding
	if g == nil || !g.HostAssembled || g.Traced || len(g.CitedEvidence) != 1 || g.CitedEvidence[0].Handle != "read#1" {
		t.Fatalf("exhaustion did not retain exactly the supported citation: %+v", g)
	}
}
