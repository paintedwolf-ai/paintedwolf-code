package promptloop

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/lycaon/lycaon/internal/anchorcatalog"
	"github.com/lycaon/lycaon/internal/coordinator/inject"
	"github.com/lycaon/lycaon/internal/evidence"
	"github.com/lycaon/lycaon/internal/guidance"
	"github.com/lycaon/lycaon/internal/jsonshape"
	"github.com/lycaon/lycaon/internal/oar"
	"github.com/lycaon/lycaon/internal/prompts/promptstest"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/pkg/api"
)

func TestDocumentFieldsRequireRunReportBinding(t *testing.T) {
	report := guidance.CoordinatorCompletionReport{
		Headline: "Conclusion", Summary: "Assessment", Limits: []string{"Unchecked"},
		Findings: []guidance.CoordinatorFinding{{Title: "Finding"}},
	}
	for _, binding := range []CompletionReportBinding{{}, {RunID: "plan", Phase: "research"}} {
		meta := completionReportMeta("implement_synthesis", binding, report)
		if meta.Scope == api.CompletionReportScopeRun || meta.Headline != "" || meta.Summary != "" || len(meta.Findings) != 0 || len(meta.Limits) != 0 {
			t.Fatalf("ordinary completion retained document fields: %+v", meta)
		}
	}
	meta := completionReportMeta("implement_synthesis", CompletionReportBinding{RunID: "survey", Phase: "report", PhaseDeliversRunReport: true}, report)
	if meta.Scope != api.CompletionReportScopeRun || meta.Headline == "" || len(meta.Findings) != 1 {
		t.Fatalf("enabled report lost document fields: %+v", meta)
	}
}

func testReportFrame() inject.CoordinatorTurnFrame {
	return inject.CoordinatorTurnFrame{
		RunContext: api.CoordinatorRunContext{RunID: "report-run", CurrentPhase: "report"},
		Runtime:    inject.WorkflowRuntimeSnapshot{ReportDocumentEnabled: true},
	}
}

func TestReviewResultRetainsOfferedPhaseForReplay(t *testing.T) {
	st := &promptLoopTurnState{}
	st.coordinatorFrame.RunContext.RunID = "run"
	st.coordinatorFrame.RunContext.CurrentPhase = "claims"
	original := &api.ToolResult{Tool: "submit_verdict", Feedback: []api.ToolFeedback{{Code: "TOOL_ARGS_INVALID"}}}
	msg := api.Message{ToolResult: original}
	bindReviewResult(&msg, st)
	if msg.WorkflowRunID != "run" || msg.ToolResult.Feedback[0].Details["workflow_phase"] != "claims" {
		t.Fatalf("missing review binding: %+v", msg)
	}
	if original.Feedback[0].Details != nil {
		t.Fatal("binding mutated the invocation's feedback")
	}
}

// The durable row carries the run and phase that offered the call, so repair
// accounting reads them from the transcript rather than the advancing run.
func TestRejectedVerdictPersistsWithOfferedBinding(t *testing.T) {
	var persisted []api.Message
	loop := NewPromptLoopForTest(PromptLoopDeps{
		Projection: ProjectionDeps{AppendMessages: func(_ context.Context, _ string, rows ...api.Message) error {
			persisted = append(persisted, rows...)
			return nil
		}},
	})
	st := &promptLoopTurnState{}
	st.coordinatorFrame.RunContext.RunID = "run"
	st.coordinatorFrame.RunContext.CurrentPhase = "claims"
	msg := api.Message{ID: "result", Role: api.MessageRoleTool, ToolResult: &api.ToolResult{Tool: "submit_verdict", ToolCallID: "call", AssistantMessageID: "response", Outcome: api.ToolResultOutcomeRejected, Feedback: []api.ToolFeedback{{Code: "TOOL_ARGS_INVALID"}}}}
	var last time.Time
	_, err := loop.Batch.persistClassifiedToolOutcome(t.Context(), "session", &api.Session{ID: "session"}, nil, toolCallOutcome{toolMsg: msg, toolName: "submit_verdict"}, &last, st)
	testutil.FailErr(t, "persist rejected verdict", err)
	for _, row := range persisted {
		if row.ToolResult != nil && row.WorkflowRunID == "run" && row.ToolResult.Feedback[0].Details["workflow_phase"] == "claims" {
			return
		}
	}
	t.Fatalf("persisted rows lost the offered workflow binding: %+v", persisted)
}

// Document repair replaces the whole fence, including citations.
func TestCloseoutDocumentDefectRefusedBeforeCitations(t *testing.T) {
	guidance.SetGuidanceRenderer(promptstest.GuidanceRenderer(t))
	hints := loadCoordinatorTestHintConfig(t)
	st := &promptLoopTurnState{
		coordinatorFrame: testReportFrame(), draftSlotID: "slot-1", draftSlotAppended: true,
		turnBatchState: turnBatchState{turnTools: []string{"read"}},
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
		Closeout: CloseoutDeps{
			HintConfig:     hints,
			RejectFmt:      guidance.NewStaticRejectFormatter(hints),
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
				if gc.Rejection.RejectObservation != guidance.ReportDocumentObservation(code) {
					t.Fatalf("observation = %q, want the document defect refused before the citation check", gc.Rejection.RejectObservation)
				}
				return &oar.Decision{Code: code, Data: gc.RejectData[code]}, nil
			},
			AssembleLedgerCloseout: func(_ context.Context, _, _ string, _ []string, _ string, _ int) (guidance.CoordinatorCompletionReport, *api.CitationGrounding) {
				assembled = true
				return guidance.CoordinatorCompletionReport{Synthesis: "assembled"}, nil
			},
			CloseoutTrackingDeps: CloseoutTrackingDeps{
				NoteCloseoutGroundingReject: func(_ context.Context, _, code, _ string, _ string, _ []jsonshape.Issue) (int, string) {
					return 1, ""
				},
			},
		},
		Projection: ProjectionDeps{
			AppendMessages:     func(context.Context, string, ...api.Message) error { return nil },
			AppendDraftVersion: func(context.Context, string, string, string, string) (int, error) { return 1, nil },
			UpdateMessage:      func(context.Context, string, string, api.Message) error { return nil },
		},
	})
	history := []api.Message{
		{ID: "u1", Role: api.MessageRoleUser, Content: "research"},
		{ID: "slot-1", Role: api.MessageRoleAssistant, Content: "no citations here", Visibility: api.MessageVisibilityInternal},
	}
	out, err := loop.Closeout.handleAcceptedCloseoutReport(
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

func TestCloseoutReportUsesOnePolicyOccurrence(t *testing.T) {
	guidance.SetGuidanceRenderer(promptstest.GuidanceRenderer(t))
	root := testutil.CheckoutRoot(t)
	testutil.FailErr(t, "install anchors", anchorcatalog.InstallFile(filepath.Join(root, "lycaon/config/packs/painted-wolf/platform/host/anchors/catalog.yaml")))
	loader, err := oar.NewLoader(filepath.Join(root, "schemas"))
	testutil.FailErr(t, "create loader", err)
	rules, err := loader.LoadEffectivePolicy()
	testutil.FailErr(t, "load policy", err)
	pipeline := oar.NewGuardPipeline(rules, loader, oar.NewCounterStore())
	pipeline.EnableAnchor(oar.AnchorCoordinatorCloseoutCheck)
	cfg := loadCoordinatorTestHintConfig(t)
	for _, code := range []string{guidance.PresentMarkdownEmbedCode, guidance.InvestNoNewEvidenceCode, guidance.SynthNoNewEvidenceCode} {
		rule, _ := rules.Get(code)
		rule.OnFire = []oar.OnFireAction{oar.OnFireIncrementCounter}
	}
	for _, tc := range []struct {
		name, surface, evidence, body, code string
		duplicate                           bool
	}{
		{"format precedes citation repair", "implement_investigate", "missing#1", "![image](90dcba36-946a-46c6-933d-e94207b897ab)", guidance.PresentMarkdownEmbedCode, false},
		{"investigate duplicate", "implement_investigate", "command#1", "Earlier result.", guidance.InvestNoNewEvidenceCode, true},
		{"synthesis duplicate", "implement_synthesis", "command#1", "Earlier result.", guidance.SynthNoNewEvidenceCode, true},
		{"new observation", "implement_investigate", "command#2", "New result.", "", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			sess := &api.Session{ID: t.Name(), WorkspacePath: t.TempDir()}
			history := []api.Message{
				{ID: "user", Role: api.MessageRoleUser, Content: "Inspect the result.", Visibility: api.MessageVisibilityTranscript},
				{ID: "prior", Role: api.MessageRoleAssistant, Content: "Earlier result.", Visibility: api.MessageVisibilityTranscript, Grounding: &api.CitationGrounding{CitedEvidence: []api.CitationGroundingCitedEvidence{{Handle: "command#1"}}}},
				{ID: "draft", Role: api.MessageRoleAssistant},
			}
			calls := 0
			var selected *oar.Decision
			loop := NewPromptLoopForTest(PromptLoopDeps{
				Closeout: CloseoutDeps{
					HintConfig:     cfg,
					RejectFmt:      guidance.NewStaticRejectFormatter(cfg),
					EvidenceLedger: closeoutLedgerReader{ledger: evidence.AssembleLedger([]evidence.Record{{Handle: "command#1", Kind: "command", Shape: evidence.ShapeCommand, Body: []string{"prior"}}, {Handle: "command#2", Kind: "command", Shape: evidence.ShapeCommand, Body: []string{"new"}}})},
					EvaluateCloseoutBlock: func(ctx context.Context, sess *api.Session, gc *oar.GuardContext) (*oar.Decision, error) {
						calls++
						gc.Session.SessionID = sess.ID
						result, err := pipeline.EvaluateBlock(ctx, oar.AnchorCoordinatorCloseoutCheck, gc)
						if err != nil {
							return nil, err
						}
						selected = result.Decision
						return selected, nil
					},
				},
				Projection: ProjectionDeps{
					AppendDraftVersion: func(context.Context, string, string, string, string) (int, error) { return 1, nil },
				},
			})
			st := &promptLoopTurnState{draftSlotID: "draft", draftSlotAppended: true, turnBatchState: turnBatchState{turnTools: []string{"command"}}}
			report := guidance.CoordinatorCompletionReport{Synthesis: tc.body, CitedEvidence: []guidance.CoordinatorCitedEvidence{{Evidence: tc.evidence}}}
			out, err := loop.Closeout.handleAcceptedCloseoutReport(t.Context(), sess, sess.ID, "", tc.surface, st, history, history[2], guidance.CloseoutRead{Report: report})
			testutil.FailErr(t, "check report", err)
			if calls != 1 {
				t.Fatalf("report generated %d policy occurrences", calls)
			}
			if tc.code == "" {
				if !out.committed {
					t.Fatalf("new evidence did not commit: %+v", out)
				}
				return
			}
			if selected == nil || selected.Code != tc.code || len(selected.Copy) != 5 {
				t.Fatalf("closeout did not retain evaluated decision: %+v", selected)
			}
			if out.committed || out.endWithoutAssemble != tc.duplicate || out.retry == tc.duplicate {
				t.Fatalf("closeout lifecycle changed: %+v", out)
			}
			rule, _ := rules.Get(tc.code)
			if count := pipeline.Counters().Get(sess.ID, rule.Qualified(), oar.CounterFire); count != 1 {
				t.Fatalf("rule fired %d times", count)
			}
		})
	}
}

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
			HintConfig:     hints,
			RejectFmt:      guidance.NewStaticRejectFormatter(hints),
			EvidenceLedger: closeoutLedgerReader{ledger: ledger},
			CloseoutTrackingDeps: CloseoutTrackingDeps{
				MaxCloseoutCitationGroundingRetries: 1,
			},
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
	state := &promptLoopTurnState{
		coordinatorFrame:  testReportFrame(),
		draftSlotID:       "slot-1",
		draftSlotAppended: true,
		turnCloseoutState: turnCloseoutState{closeoutRetry: closeoutRetryState{attempt: 1}},
		turnBatchState:    turnBatchState{turnTools: []string{"read"}},
		history:           []api.Message{{ID: "u1", Role: api.MessageRoleUser, Content: "Inspect the marker"}, assistant},
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
