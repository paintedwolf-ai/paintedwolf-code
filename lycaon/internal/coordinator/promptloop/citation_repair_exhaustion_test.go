package promptloop

import (
	"context"
	"testing"

	"github.com/lycaon/lycaon/internal/evidence"
	"github.com/lycaon/lycaon/internal/guidance"
	"github.com/lycaon/lycaon/internal/oar"
	"github.com/lycaon/lycaon/internal/prompts/promptstest"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/pkg/api"
)

func TestCitationRepairExhaustionKeepsPinnedReport(t *testing.T) {
	guidance.SetGuidanceRenderer(promptstest.GuidanceRenderer(t))
	hints := loadCoordinatorTestHintConfig(t)
	original := guidance.CoordinatorCompletionReport{
		Synthesis: "The original marker was amber.", Headline: "Original report",
		Summary: "Historical observation", Limits: []string{"Inspection only."},
		CitedEvidence: []guidance.CoordinatorCitedEvidence{{Evidence: "read#1"}, {Evidence: "read#999"}},
	}
	pinned, err := guidance.MarshalCoordinatorCompletionReport(original)
	testutil.FailErr(t, "marshal retained report", err)
	ledger := evidence.AssembleLedger([]evidence.Record{{Handle: "read#1", Kind: "read", Shape: evidence.ShapeFileRegion, Path: "notes.txt", Body: []string{"Release marker: amber"}}})
	var committed api.Message
	loop := NewPromptLoopForTest(PromptLoopDeps{
		Closeout: CloseoutDeps{
			HintConfig:                          hints,
			RejectFmt:                           guidance.NewStaticRejectFormatter(hints),
			MaxCloseoutCitationGroundingRetries: 1,
			EvidenceLedger:                      closeoutLedgerReader{ledger: ledger},
			CloseoutStallState: func(context.Context, string) guidance.RetainedCloseout {
				return guidance.RetainedCloseout{Active: true, Attempt: 1, Drafted: pinned}
			},
			EvaluateCloseoutBlock: func(_ context.Context, _ *api.Session, gc *oar.GuardContext) (*oar.Decision, error) {
				return &oar.Decision{Code: guidance.InvestHandleNotObservedCode, Data: gc.RejectData[guidance.InvestHandleNotObservedCode]}, nil
			},
			AssembleLedgerCloseout: func(_ context.Context, _, surface string, codes []string, raw string, retries int) (guidance.CoordinatorCompletionReport, *api.CitationGrounding) {
				draft, ok := guidance.ParseCoordinatorCompletionReport(raw)
				if !ok {
					t.Fatalf("exhaustion lost the report envelope: %q", raw)
				}
				return guidance.AssembleRetainedCloseout(evidence.CitationRoots{}, surface, guidance.CloseoutEvidence{Ledger: ledger}, draft, codes[0], retries)
			},
		},
		Projection: ProjectionDeps{
			AppendMessages:     func(context.Context, string, ...api.Message) error { return nil },
			AppendDraftVersion: func(context.Context, string, string, string, string) (int, error) { return 1, nil },
			UpdateMessage: func(_ context.Context, _, _ string, msg api.Message) error {
				if msg.Grounding != nil {
					committed = msg
				}
				return nil
			},
		},
	})
	assistant := api.Message{ID: "slot-1", Role: api.MessageRoleAssistant, Content: "Replacement narrative."}
	state := &promptLoopTurnState{coordinatorFrame: testReportFrame(),
		draftSlotID: "slot-1", draftSlotAppended: true, closeoutRetry: closeoutRetryState{attempt: 1},
		turnTools: []string{"read"}, history: []api.Message{{ID: "u1", Role: api.MessageRoleUser, Content: "Inspect the marker"}, assistant},
	}
	repair := guidance.CoordinatorCompletionReport{Synthesis: assistant.Content, Headline: "Replacement", CitedEvidence: original.CitedEvidence}
	out, err := loop.Closeout.applyAcceptedCloseoutReport(t.Context(), &api.Session{ID: "s1", WorkspacePath: t.TempDir()}, "s1", "", "implement_investigate", state, assistant, guidance.CloseoutRead{Report: repair})
	testutil.FailErr(t, "finish exhausted citation repair", err)
	if !out.breakLoop || !state.proseFinishDelivered || committed.Content != original.Synthesis {
		t.Fatalf("exhaustion did not preserve the report body: outcome=%+v content=%q", out, committed.Content)
	}
	meta := committed.CompletionReport
	if meta == nil || meta.Headline != original.Headline || meta.Summary != original.Summary || len(meta.Limits) != 1 || meta.Limits[0] != original.Limits[0] {
		t.Fatalf("exhaustion lost report metadata: %+v", meta)
	}
	g := committed.Grounding
	if g == nil || !g.HostAssembled || g.Traced || len(g.CitedEvidence) != 1 || g.CitedEvidence[0].Handle != "read#1" {
		t.Fatalf("exhaustion did not retain exactly the supported citation: %+v", g)
	}
}
