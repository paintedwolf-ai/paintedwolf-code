package closeoutassembly

import (
	"context"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/evidence"
	"github.com/lycaon/lycaon/internal/guidance"
	"github.com/lycaon/lycaon/internal/projectroot"
	"github.com/lycaon/lycaon/internal/session/workercompletion"
	"github.com/lycaon/lycaon/pkg/api"
)

type closeoutSession struct {
	msgs []api.Message
}

func (closeoutSession) Get(context.Context, string) (*api.Session, error) {
	return &api.Session{ID: "parent", WorkspaceRootID: "primary"}, nil
}
func (s closeoutSession) GetMessages(context.Context, string) ([]api.Message, error) {
	return s.msgs, nil
}
func (closeoutSession) LoadLedger(context.Context, string) (evidence.Ledger, error) {
	return evidence.InitLedger(), nil
}

type closeoutRoots struct{ root string }

func (r closeoutRoots) Roots(context.Context, *api.Session) ([]projectroot.RootRef, error) {
	return []projectroot.RootRef{{ID: "primary", Path: r.root, IsPrimary: true}}, nil
}

func TestAssemblyRecoversCompletedLegsWithoutInventingCitations(t *testing.T) {
	completeEnv := workercompletion.FormatWorkerCompletionEnvelope(workercompletion.WorkerCompletionEnvelope{
		JobID: "job", ChildSessionID: "child-1", AgentType: "Review", State: "complete",
		Report: workercompletion.WorkerCompletionReport{LegStatus: "complete", Brief: "Checked access controls."},
	})
	dupEnv := workercompletion.FormatWorkerCompletionEnvelope(workercompletion.WorkerCompletionEnvelope{
		JobID: "job", ChildSessionID: "child-1", AgentType: "Review", State: "complete",
		Report: workercompletion.WorkerCompletionReport{LegStatus: "complete", Brief: "Duplicate answer."},
	})
	unfinishedEnv := workercompletion.FormatWorkerCompletionEnvelope(workercompletion.WorkerCompletionEnvelope{
		JobID: "job-pending", ChildSessionID: "child-2", AgentType: "Review", State: "running",
		Report: workercompletion.WorkerCompletionReport{LegStatus: "partial", Brief: "Unfinished assertion."},
	})
	s := New(closeoutSession{msgs: []api.Message{
		{Role: api.MessageRoleTool, WorkerSummary: &api.WorkerSummaryMeta{WorkerID: "job", LegID: "leg", AgentType: "Review", Status: api.WorkerSummaryStatusComplete, Envelope: completeEnv}},
		{Role: api.MessageRoleTool, WorkerSummary: &api.WorkerSummaryMeta{WorkerID: "job", LegID: "duplicate", AgentType: "Review", Status: api.WorkerSummaryStatusComplete, Envelope: dupEnv}},
		{Role: api.MessageRoleTool, WorkerSummary: &api.WorkerSummaryMeta{WorkerID: "job-pending", LegID: "pending", AgentType: "Review", Status: api.WorkerSummaryStatusPartial, Envelope: unfinishedEnv}},
		{Role: api.MessageRoleTool, WorkerSummary: &api.WorkerSummaryMeta{WorkerID: "job-empty", LegID: "empty", AgentType: "Review", Status: api.WorkerSummaryStatusComplete}},
	}}, closeoutSession{msgs: []api.Message{
		{Role: api.MessageRoleTool, WorkerSummary: &api.WorkerSummaryMeta{WorkerID: "job", LegID: "leg", AgentType: "Review", Status: api.WorkerSummaryStatusComplete, Envelope: completeEnv}},
		{Role: api.MessageRoleTool, WorkerSummary: &api.WorkerSummaryMeta{WorkerID: "job", LegID: "duplicate", AgentType: "Review", Status: api.WorkerSummaryStatusComplete, Envelope: dupEnv}},
		{Role: api.MessageRoleTool, WorkerSummary: &api.WorkerSummaryMeta{WorkerID: "job-pending", LegID: "pending", AgentType: "Review", Status: api.WorkerSummaryStatusPartial, Envelope: unfinishedEnv}},
		{Role: api.MessageRoleTool, WorkerSummary: &api.WorkerSummaryMeta{WorkerID: "job-empty", LegID: "empty", AgentType: "Review", Status: api.WorkerSummaryStatusComplete}},
	}}, closeoutSession{msgs: []api.Message{
		{Role: api.MessageRoleTool, WorkerSummary: &api.WorkerSummaryMeta{WorkerID: "job", LegID: "leg", AgentType: "Review", Status: api.WorkerSummaryStatusComplete, Envelope: completeEnv}},
		{Role: api.MessageRoleTool, WorkerSummary: &api.WorkerSummaryMeta{WorkerID: "job", LegID: "duplicate", AgentType: "Review", Status: api.WorkerSummaryStatusComplete, Envelope: dupEnv}},
		{Role: api.MessageRoleTool, WorkerSummary: &api.WorkerSummaryMeta{WorkerID: "job-pending", LegID: "pending", AgentType: "Review", Status: api.WorkerSummaryStatusPartial, Envelope: unfinishedEnv}},
		{Role: api.MessageRoleTool, WorkerSummary: &api.WorkerSummaryMeta{WorkerID: "job-empty", LegID: "empty", AgentType: "Review", Status: api.WorkerSummaryStatusComplete}},
	}}, closeoutRoots{t.TempDir()}, nil, nil)
	report, grounding := s.Assemble(t.Context(), "parent", "implement_synthesis", nil, guidance.CloseoutDraft{}, 0)
	if !strings.Contains(report.Synthesis, "Review: Checked access controls.") || strings.Contains(report.Synthesis, "Duplicate") || strings.Contains(report.Synthesis, "Unfinished") {
		t.Fatalf("assembled report=%+v", report)
	}
	if len(report.CitedEvidence) != 0 || len(report.CitedURLs) != 0 {
		t.Fatalf("fabricated citations=%+v", report)
	}
	if grounding != nil && grounding.Traced {
		t.Fatalf("unobserved leg was traced: %+v", grounding)
	}
	report, _ = s.Assemble(t.Context(), "parent", "implement_synthesis", nil, draftOf("Human-facing draft."), 0)
	if report.Synthesis != "Human-facing draft." {
		t.Fatalf("draft replaced by retained summaries: %+v", report)
	}
}
