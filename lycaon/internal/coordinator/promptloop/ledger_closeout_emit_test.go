package promptloop

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/guidance"
	"github.com/lycaon/lycaon/internal/jsonshape"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/pkg/api"
)

func TestEmitAssembledCloseoutPreservesHostAssembled(t *testing.T) {
	var committed api.Message
	deps := PromptLoopDeps{
		UpdateMessage: func(_ context.Context, _, _ string, msg api.Message) error {
			committed = msg
			return nil
		},
		// Simulate a recompute that would set traced=true.
		ProseCitationGrounding: func(_ context.Context, _ *api.Session, _ []api.Message, _, _, _ string) *api.CitationGrounding {
			return &api.CitationGrounding{Traced: true}
		},
		AssembleLedgerCloseout: func(_ context.Context, _, _ string, forcedBy []string, drafted string, retryCount int) (guidance.CoordinatorCompletionReport, *api.CitationGrounding) {
			return guidance.CoordinatorCompletionReport{Synthesis: drafted},
				&api.CitationGrounding{HostAssembled: true, Traced: false, CitedURLs: []string{"https://status.example.com/42"}, RetryCount: retryCount}
		},
	}
	l := NewPromptLoopForTest(deps)
	st := &promptLoopTurnState{draftSlotID: "slot-1", draftSlotAppended: true, coordinatorFrame: testReportFrame()}
	st.coordinatorFrame.RunContext.RunID = "run-1"
	st.coordinatorFrame.RunContext.CurrentPhase = "work"
	st.coordinatorFrame.Runtime.ReportDocumentEnabled = false
	sess := &api.Session{ID: "s1"}
	history := []api.Message{{ID: "slot-1", Role: api.MessageRoleAssistant}}
	prose := "The outage began when the cache warmer looped; root cause is a missing backoff."

	out, err := turnCloseout{l}.emitAssembledCloseout(
		context.Background(), sess, "s1", "", "implement_investigate", prose,
		[]string{"INVEST_CITATIONS_REQUIRED"}, st, history,
	)
	testutil.FailErr(t, "emitAssembledCloseout", err)

	if !out.committed {
		t.Fatal("expected the assembled closeout to commit, not dead-end")
	}
	if committed.Grounding == nil || !committed.Grounding.HostAssembled {
		t.Fatalf("committed grounding = %+v want host_assembled", committed.Grounding)
	}
	if committed.Grounding.Traced {
		t.Fatal("investigate observed-autobind exit must keep traced=false (advisory)")
	}
	if !strings.Contains(committed.Content, "missing backoff") {
		t.Fatalf("committed content must retain the model prose, got %q", committed.Content)
	}
	if committed.Visibility != api.MessageVisibilityTranscript {
		t.Fatalf("committed visibility = %q want transcript", committed.Visibility)
	}
	if committed.Kind != api.MessageKindCompletionReport {
		t.Fatalf("committed kind = %q want completion_report", committed.Kind)
	}
	if committed.CompletionReport == nil ||
		committed.CompletionReport.Scope != api.CompletionReportScopePhase || committed.WorkflowRunID != "run-1" {
		t.Fatalf("completion report meta = %+v want phase scope bound to run-1", committed.CompletionReport)
	}
	// Closing the slot prevents draft withdrawal.
	if st.draftSlotID != "" {
		t.Fatalf("draft slot = %q want closed after assembled emit", st.draftSlotID)
	}
	if st.turnEndedGuidanceReject {
		t.Fatal("assembled emit ends the turn as committed, not a guidance reject")
	}
}

// A run report the host stores after document repair ran out records every
// requirement it still fails, and keeps off-enum values off the wire record.
func TestEmitAssembledRunReportRecordsItsDefects(t *testing.T) {
	var committed api.Message
	var checked guidance.CoordinatorCompletionReport
	issues := []guidance.ReportDocumentIssue{
		{Code: guidance.ReportDocumentInvalidCode, Reason: `finding "f1" has disposition "ok" (want act, accept, or held)`},
		{Code: guidance.ReportClaimUnreportedCode, Reason: "claims need a finding", Offenders: []string{"c1 (failed)"}, Count: 2},
	}
	l := NewPromptLoopForTest(PromptLoopDeps{
		UpdateMessage: func(_ context.Context, _, _ string, msg api.Message) error {
			committed = msg
			return nil
		},
		AssembleLedgerCloseout: func(context.Context, string, string, []string, string, int) (guidance.CoordinatorCompletionReport, *api.CitationGrounding) {
			return guidance.CoordinatorCompletionReport{
				Synthesis: "Survey done.",
				Findings:  []guidance.CoordinatorFinding{{ID: "f1", Title: "Weak pin", Disposition: "ok"}},
				Ask:       &guidance.CoordinatorAsk{Do: "Bump the pin.", Effort: "tiny"},
			}, &api.CitationGrounding{HostAssembled: true}
		},
		CheckRunReportDocument: func(_ context.Context, _ string, report guidance.CoordinatorCompletionReport) ([]guidance.ReportDocumentIssue, error) {
			checked = report
			return issues, nil
		},
	})
	st := &promptLoopTurnState{draftSlotID: "slot-1", draftSlotAppended: true, coordinatorFrame: testReportFrame()}
	st.coordinatorFrame.RunContext.RunID = "run-1"
	st.coordinatorFrame.RunContext.CurrentPhase = "synthesis"
	st.coordinatorFrame.Runtime.ReportDocumentEnabled = true
	out, err := turnCloseout{l}.emitAssembledCloseout(
		t.Context(), &api.Session{ID: "s1"}, "s1", "", "coordinator_security_synthesis", "draft",
		[]string{guidance.ReportClaimUnreportedCode}, st, []api.Message{{ID: "slot-1", Role: api.MessageRoleAssistant}},
	)
	testutil.FailErr(t, "emitAssembledCloseout", err)
	if !out.committed {
		t.Fatal("expected the unaccepted report to be stored")
	}
	if checked.Synthesis != "Survey done." {
		t.Fatalf("checked report = %+v, want the stored one", checked)
	}
	meta := committed.CompletionReport
	if meta == nil || meta.Scope != api.CompletionReportScopeRun {
		t.Fatalf("meta = %+v, want run scope", meta)
	}
	want := guidance.ReportDocumentDefects(issues)
	if len(meta.Defects) != 2 || meta.Defects[0].Code != api.CompletionReportDefectCodeDocumentInvalid ||
		meta.Defects[1].Code != api.CompletionReportDefectCodeClaimUnreported || meta.Defects[1].Count != 2 ||
		meta.Defects[1].Subjects[0] != want[1].Subjects[0] {
		t.Fatalf("defects = %+v, want %+v", meta.Defects, want)
	}
	if len(meta.Findings) != 1 || meta.Findings[0].Disposition != "" || meta.Ask != nil {
		t.Fatalf("meta = %+v, want the off-enum disposition and ask kept off the record", meta)
	}
}

// A run report stored after repair ran out on an unreadable fence keeps the
// fields the host read and records the members it did not, from the cycle's
// latest refusal, ahead of the document checks.
func TestEmitAssembledRunReportRecordsItsUnreadFence(t *testing.T) {
	var committed api.Message
	unread := []jsonshape.Issue{
		{Path: "findings[0].ask", Pattern: "findings[].ask", Parent: "findings[]", Name: "ask", Kind: jsonshape.Unknown},
		{Path: "findings[1].ask", Pattern: "findings[].ask", Parent: "findings[]", Name: "ask", Kind: jsonshape.Unknown},
	}
	drafted, err := guidance.MarshalCoordinatorCompletionReport(guidance.CoordinatorCompletionReport{
		Synthesis: "Survey done.",
		Findings:  []guidance.CoordinatorFinding{{ID: "c1", Title: "Pin bump", Disposition: "act"}},
	})
	testutil.FailErr(t, "marshal retained draft", err)
	l := NewPromptLoopForTest(PromptLoopDeps{
		UpdateMessage: func(_ context.Context, _, _ string, msg api.Message) error {
			committed = msg
			return nil
		},
		CloseoutStallState: func(context.Context, string) guidance.RetainedCloseout {
			return guidance.RetainedCloseout{Active: true, DocumentAttempt: 3, Drafted: drafted, ForcedBy: []string{guidance.ReportFenceUnreadableCode}, Unread: unread}
		},
		AssembleLedgerCloseout: func(_ context.Context, _, _ string, _ []string, raw string, _ int) (guidance.CoordinatorCompletionReport, *api.CitationGrounding) {
			report, ok := guidance.ParseCoordinatorCompletionReport(raw)
			if !ok {
				t.Fatalf("retained draft is not an envelope: %q", raw)
			}
			return report, &api.CitationGrounding{HostAssembled: true}
		},
		CheckRunReportDocument: func(context.Context, string, guidance.CoordinatorCompletionReport) ([]guidance.ReportDocumentIssue, error) {
			return []guidance.ReportDocumentIssue{{Code: guidance.ReportDocumentInvalidCode, Reason: "1 finding(s) need action, so the report needs an ask"}}, nil
		},
	})
	st := &promptLoopTurnState{draftSlotID: "slot-1", draftSlotAppended: true, coordinatorFrame: testReportFrame()}
	out, err := turnCloseout{l}.emitStalledCloseout(t.Context(), &api.Session{ID: "s1"}, "s1", "", "coordinator_security_synthesis", st,
		[]api.Message{{ID: "slot-1", Role: api.MessageRoleAssistant}})
	testutil.FailErr(t, "emitStalledCloseout", err)
	meta := committed.CompletionReport
	if !out.committed || meta == nil || len(meta.Findings) != 1 {
		t.Fatalf("meta = %+v, want the finding the host read", meta)
	}
	if len(meta.Defects) != 2 || meta.Defects[0].Code != api.CompletionReportDefectCodeFenceUnreadable ||
		meta.Defects[0].Count != 1 || !strings.Contains(meta.Defects[0].Subjects[0], "findings[].ask") ||
		meta.Defects[1].Code != api.CompletionReportDefectCodeDocumentInvalid {
		t.Fatalf("defects = %+v, want the unread fence, then the document check", meta.Defects)
	}
}

func TestEmitAssembledCloseoutRequiresAssembler(t *testing.T) {
	l := NewPromptLoopForTest(PromptLoopDeps{})
	st := &promptLoopTurnState{draftSlotID: "slot-1", draftSlotAppended: true}
	_, err := turnCloseout{l}.emitAssembledCloseout(
		context.Background(), &api.Session{ID: "s1"}, "s1", "", "implement_investigate", "prose",
		nil, st, nil,
	)
	if !errors.Is(err, errCloseoutAssemblerUnavailable) {
		t.Fatalf("error = %v want %v", err, errCloseoutAssemblerUnavailable)
	}
}

func TestEmitAssembledCloseoutWithoutGroundingStaysDraft(t *testing.T) {
	var committed api.Message
	var updates []api.Message
	l := NewPromptLoopForTest(PromptLoopDeps{
		AssembleLedgerCloseout: func(context.Context, string, string, []string, string, int) (guidance.CoordinatorCompletionReport, *api.CitationGrounding) {
			return guidance.CoordinatorCompletionReport{Synthesis: "No observed evidence."}, nil
		},
		UpdateMessage: func(_ context.Context, _, _ string, msg api.Message) error {
			updates = append(updates, msg)
			committed = msg
			return nil
		},
	})
	st := &promptLoopTurnState{draftSlotID: "slot-1", draftSlotAppended: true}
	out, err := turnCloseout{l}.emitAssembledCloseout(
		context.Background(), &api.Session{ID: "s1"}, "s1", "", "implement_synthesis", "", nil,
		st, []api.Message{{ID: "slot-1", Role: api.MessageRoleAssistant}},
	)
	testutil.FailErr(t, "emitAssembledCloseout", err)
	if !out.committed {
		t.Fatal("expected narrative draft to commit")
	}
	if committed.Kind != api.MessageKindDraft || committed.CompletionReport != nil {
		t.Fatalf("committed kind/meta = %q/%+v want draft without report identity", committed.Kind, committed.CompletionReport)
	}
	for i, update := range updates {
		if update.Kind == api.MessageKindCompletionReport || update.CompletionReport != nil {
			t.Fatalf("update %d transiently advertised an ungrounded report: %+v", i, update)
		}
	}
}

func TestEarlyCloseoutFinishBlockAssembles(t *testing.T) {
	var committed api.Message
	var forcedCodes []string
	deps := PromptLoopDeps{
		UpdateMessage: func(_ context.Context, _, _ string, msg api.Message) error {
			committed = msg
			return nil
		},
		AssembleLedgerCloseout: func(_ context.Context, _, _ string, forcedBy []string, drafted string, _ int) (guidance.CoordinatorCompletionReport, *api.CitationGrounding) {
			forcedCodes = append([]string(nil), forcedBy...)
			report, ok := guidance.ParseCoordinatorCompletionReport(drafted)
			if !ok || report.Headline != "Partial audit" || len(report.CitedEvidence) != 1 {
				t.Fatalf("forced finish lost the report envelope: %q", drafted)
			}
			return report, &api.CitationGrounding{HostAssembled: true, Traced: false}
		},
	}
	l := NewPromptLoopForTest(deps)
	st := &promptLoopTurnState{draftSlotID: "slot-1", draftSlotAppended: true}
	sess := &api.Session{ID: "s1"}
	history := []api.Message{{ID: "slot-1", Role: api.MessageRoleAssistant}}
	envelope := "```json\n{\"synthesis\":\"## Audit status\\nPartial findings after sandbox blocks.\",\"headline\":\"Partial audit\",\"cited_evidence\":[{\"evidence\":\"read#1\"}]}\n```"
	reject := "Rejected: open progress\n\nCode: PROGRESS_OPEN_BEFORE_CLOSEOUT\n"

	hist, aid, content, err := turnCloseout{l}.emitEarlyCloseoutAfterFinishBlock(
		context.Background(), sess, "s1", "", "implement_investigate", envelope, refusalForTest(reject), st, history,
	)
	testutil.FailErr(t, "emitEarlyCloseoutAfterFinishBlock", err)
	if aid == "" {
		t.Fatal("expected committed assistant id")
	}
	if !strings.Contains(content, "Partial findings") && !strings.Contains(committed.Content, "Partial findings") {
		t.Fatalf("committed content must retain drafted synthesis, got content=%q committed=%q", content, committed.Content)
	}
	if committed.Grounding == nil || !committed.Grounding.HostAssembled {
		t.Fatalf("committed grounding = %+v want host_assembled", committed.Grounding)
	}
	found := false
	for _, c := range forcedCodes {
		if c == "PROGRESS_OPEN_BEFORE_CLOSEOUT" {
			found = true
			break
		}
	}
	if !found {
		t.Fatalf("forcedBy = %v want PROGRESS_OPEN_BEFORE_CLOSEOUT", forcedCodes)
	}
	if st.draftSlotID != "" {
		t.Fatalf("draft slot = %q want closed after assemble", st.draftSlotID)
	}
	_ = hist
}
