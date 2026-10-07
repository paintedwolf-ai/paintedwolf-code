package promptloop

import (
	"context"
	"testing"

	"github.com/lycaon/lycaon/internal/evidence"
	"github.com/lycaon/lycaon/internal/guidance"
	"github.com/lycaon/lycaon/internal/jsonshape"
	"github.com/lycaon/lycaon/internal/oar"
	"github.com/lycaon/lycaon/internal/prompts/promptstest"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/pkg/api"
)

// Document repair replaces the whole fence, including citations.
func TestCloseoutDocumentDefectRefusedBeforeCitations(t *testing.T) {
	guidance.SetGuidanceRenderer(promptstest.GuidanceRenderer(t))
	hints := loadCoordinatorTestHintConfig(t)
	st := &promptLoopTurnState{
		coordinatorFrame: testReportFrame(), draftSlotID: "slot-1", draftSlotAppended: true,
		turnTools: []string{"read"},
	}
	ledger := evidence.Ledger{
		Handles: map[string]evidence.Record{
			"read#1": {Handle: "read#1", Kind: "read", Path: "src/a.go", Body: []string{"package main"}},
		},
		ByPath:       map[string][]string{"src/a.go": {"read#1"}},
		PathFidelity: map[string]string{"src/a.go": "structured"},
	}
	var assembled bool
	loop := NewPromptLoopForTest(PromptLoopDeps{
		HintConfig: hints, RejectFmt: guidance.NewStaticRejectFormatter(hints),
		EvidenceLedger: closeoutLedgerReader{ledger: ledger},
		CheckRunReportDocument: func(_ context.Context, _ string, _ guidance.CoordinatorCompletionReport) ([]guidance.ReportDocumentIssue, error) {
			return []guidance.ReportDocumentIssue{{
				Code:   guidance.ReportClaimUnreportedCode,
				Reason: `finding "c9" has no answers`,
				Count:  1,
			}}, nil
		},
		EvaluateCloseoutBlock: func(_ context.Context, _ *api.Session, gc *oar.GuardContext) (*oar.Decision, error) {
			code := guidance.ReportClaimUnreportedCode
			if gc.RejectObservation != guidance.ReportDocumentObservation(code) {
				t.Fatalf("observation = %q, want the document defect refused before the citation check", gc.RejectObservation)
			}
			return &oar.Decision{Code: code, Data: gc.RejectData[code]}, nil
		},
		AssembleLedgerCloseout: func(_ context.Context, _, _ string, _ []string, _ string, _ int) (guidance.CoordinatorCompletionReport, *api.CitationGrounding) {
			assembled = true
			return guidance.CoordinatorCompletionReport{Synthesis: "assembled"}, nil
		},
		NoteCloseoutGroundingReject: func(_ context.Context, _, code, _ string, _ string, _ []jsonshape.Issue) (int, string) {
			return 1, ""
		},
		AppendMessages:     func(context.Context, string, ...api.Message) error { return nil },
		AppendDraftVersion: func(context.Context, string, string, string, string) (int, error) { return 1, nil },
		UpdateMessage:      func(context.Context, string, string, api.Message) error { return nil },
	})
	history := []api.Message{
		{ID: "u1", Role: api.MessageRoleUser, Content: "research"},
		{ID: "slot-1", Role: api.MessageRoleAssistant, Content: "no citations here", Visibility: api.MessageVisibilityInternal},
	}
	out, err := loop.handleAcceptedCloseoutReport(
		context.Background(), &api.Session{ID: "s1", WorkspacePath: t.TempDir()},
		"s1", "", "implement_synthesis", st, history,
		api.Message{ID: "slot-1", Role: api.MessageRoleAssistant, Content: "no citations here"},
		guidance.CloseoutRead{Report: guidance.CoordinatorCompletionReport{
			Synthesis:     "no citations here",
			CitedEvidence: []guidance.CoordinatorCitedEvidence{{Evidence: "missing#1"}},
		}},
	)
	testutil.FailErr(t, "handleAcceptedCloseoutReport", err)
	if assembled {
		t.Fatal("a document-defective draft must not be assembled for commit")
	}
	if out.committed || !out.retry {
		t.Fatalf("outcome = %+v, want a document refusal with a repair retry", out)
	}
	codes := st.closeoutRetry.codes
	if len(codes) != 1 || codes[0] != guidance.ReportClaimUnreportedCode {
		t.Fatalf("refusal codes = %v, want the document defect refused first", codes)
	}
}
