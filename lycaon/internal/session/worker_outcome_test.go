package session_test

import (
	"context"
	"sort"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/guidance/ledgertest"
	"github.com/lycaon/lycaon/internal/promptresult"
	"github.com/lycaon/lycaon/internal/session/store"
	"github.com/lycaon/lycaon/internal/session/stream"
	"github.com/lycaon/lycaon/internal/session/workercloseout"
	"github.com/lycaon/lycaon/internal/session/workercompletion"
	"github.com/lycaon/lycaon/internal/session/workeroutcomes"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/pkg/api"
)

type stubSummaryResolver struct {
	prompts                  int
	promptErr                error
	closeoutAddsLegTool      bool
	msgs                     []api.Message
	streamByMsg              map[string]string
	fullTranscriptReads      int
	workerJobTranscriptReads int
	promptFunc               func(ctx context.Context, sessionID, text string) (*promptresult.Result, error)
	projectDir               string
}

func (s *stubSummaryResolver) stampEvidenceHandles() {
	ledger := ledgertest.BuildFromMessages(s.projectDir, s.msgs)
	handles := make([]string, 0, len(ledger.Handles))
	for handle := range ledger.Handles {
		handles = append(handles, handle)
	}
	sort.Strings(handles)
	for i := range s.msgs {
		if s.msgs[i].Role == api.MessageRoleTool {
			s.msgs[i].EvidenceHandles = append([]string(nil), handles...)
		}
	}
}

func (s *stubSummaryResolver) Prompt(ctx context.Context, sessionID, text string) (*promptresult.Result, error) {
	s.prompts++
	if s.promptFunc != nil {
		return s.promptFunc(ctx, sessionID, text)
	}
	if s.promptErr != nil {
		return nil, s.promptErr
	}
	return &promptresult.Result{MessageID: "closeout-1"}, nil
}

func (s *stubSummaryResolver) PromptHostTurn(ctx context.Context, sessionID string, origin store.PromptSubmissionOrigin, text string) (*promptresult.Result, error) {
	result, err := s.Prompt(ctx, sessionID, text)
	if err == nil && origin == store.PromptSubmissionOriginWorkerCloseout && s.closeoutAddsLegTool {
		s.msgs = append(s.msgs, completeLegToolRow(map[string]any{"leg_status": "partial", "brief": "Partial survey before cancel", "remaining_risk": []string{"canceled mid-survey"}}))
	}
	return result, err
}

func (s *stubSummaryResolver) PromptWorker(ctx context.Context, sessionID, _ string, text string) (*promptresult.Result, error) {
	return s.Prompt(ctx, sessionID, text)
}

func (s *stubSummaryResolver) SpawnChild(ctx context.Context, parentID string, req api.SpawnChildRequest) (*api.Session, error) {
	return &api.Session{ID: "child"}, nil
}

func (s *stubSummaryResolver) AppendWorkerSummary(ctx context.Context, parentID string, in workeroutcomes.SummaryInput) (string, error) {
	return in.Status, nil
}

func (s *stubSummaryResolver) GetMessages(ctx context.Context, sessionID string) ([]api.Message, error) {
	s.fullTranscriptReads++
	return append([]api.Message(nil), s.msgs...), nil
}

func (s *stubSummaryResolver) GetWorkerJobMessages(ctx context.Context, sessionID, workerJobID string) ([]api.Message, error) {
	s.workerJobTranscriptReads++
	return append([]api.Message(nil), s.msgs...), nil
}

func (s *stubSummaryResolver) Streams() *stream.State {
	streams := stream.New(nil)
	for id, content := range s.streamByMsg {
		streams.CacheReplay(id, content, nil)
	}
	return streams
}

func finalizeOpts(resolver *stubSummaryResolver, childID string, opts workercloseout.WorkerSummaryFinalizeOpts) workercloseout.WorkerSummaryFinalizeOpts {
	if opts.WorkerJobID == "" {
		opts.WorkerJobID = "job-test"
	}
	if resolver != nil {
		resolver.projectDir = opts.ProjectDir
		resolver.stampEvidenceHandles()
	}
	opts.Ledger = ledgertest.ChildMessagesReader(opts.ProjectDir, func(id string) []api.Message {
		if id == childID && resolver != nil {
			return resolver.msgs
		}
		return nil
	})
	return opts
}

func scoutSurveyFixtureMessages() []api.Message {
	readJSON := `{"path":"foo.go","content":"1|package foo","offset":1,"end_line":1,"limit":10}`
	return []api.Message{
		{
			Role: api.MessageRoleAssistant,
			ToolCalls: []api.ToolCall{
				{ID: "read-foo", Name: "read", Args: map[string]any{"path": "foo.go", "offset": 1, "limit": 10}},
			},
		},
		{Role: api.MessageRoleTool, Content: readJSON, ToolResult: &api.ToolResult{Content: readJSON, Outcome: api.ToolResultOutcomeCompleted}},
	}
}

func completeLegToolRow(args map[string]any) api.Message {
	report, _ := workercompletion.ReportFromCompleteLegArgs(args)
	return api.Message{
		Role: api.MessageRoleTool,
		ToolResult: &api.ToolResult{
			Tool:         "complete_leg",
			Content:      `{"recorded":true}`,
			ToolArgs:     args,
			WorkerReport: &api.WorkerReportAcceptance{WorkerJobID: "job-test", Report: workercompletion.ReportWire(report), Grounding: &api.CitationGrounding{Traced: true}},
			Outcome:      api.ToolResultOutcomeCompleted,
		},
	}
}

func TestFinalizeWorkerSummaryPrefersCompleteLeg(t *testing.T) {
	resolver := &stubSummaryResolver{
		msgs: append(scoutSurveyFixtureMessages(), completeLegToolRow(map[string]any{
			"leg_status":     "complete",
			"brief":          "surveyed via tool",
			"objectives_met": []any{"mapped auth"},
		})),
	}
	out, workerEvalErr := workercloseout.FinalizeWorkerSummaryForChild(context.Background(), resolver, resolver, "child", "path-explorer", finalizeOpts(resolver, "child", workercloseout.WorkerSummaryFinalizeOpts{}))
	testutil.FailErr(t, "evaluate worker completion", workerEvalErr)
	if out.Provenance != "complete_leg" || out.Report.Brief != "surveyed via tool" {
		t.Fatalf("out = %+v want complete_leg", out)
	}
	if resolver.prompts != 0 {
		t.Fatalf("prompts = %d want 0 (no JSON closeout generate)", resolver.prompts)
	}
}

func TestFinalizeWorkerSummaryScopesReusedChildToWorkerJob(t *testing.T) {
	oldReport := completeLegToolRow(map[string]any{
		"leg_status": "complete",
		"brief":      "previous run",
	})
	oldReport.WorkerID = "job-old"
	oldReport.ToolResult.WorkerReport.WorkerJobID = "job-old"
	currentMessages := scoutSurveyFixtureMessages()
	for i := range currentMessages {
		currentMessages[i].WorkerID = "job-current"
	}
	currentReport := completeLegToolRow(map[string]any{
		"leg_status": "complete",
		"brief":      "current run",
	})
	currentReport.WorkerID = "job-current"
	currentReport.ToolResult.WorkerReport.WorkerJobID = "job-current"
	resolver := &stubSummaryResolver{msgs: append([]api.Message{oldReport}, append(currentMessages, currentReport)...)}
	opts := finalizeOpts(resolver, "child", workercloseout.WorkerSummaryFinalizeOpts{WorkerJobID: "job-current"})

	out, workerEvalErr := workercloseout.FinalizeWorkerSummaryForChild(
		context.Background(), resolver, resolver, "child", "path-explorer", opts,
	)
	testutil.FailErr(t, "evaluate worker completion", workerEvalErr)

	if out.Report.Brief != "current run" {
		t.Fatalf("brief = %q want current run", out.Report.Brief)
	}
}

func TestFinalizeWorkerSummaryCompleteLegSurvivesCompactedBody(t *testing.T) {
	// Compaction preserves the host acceptance receipt.
	row := completeLegToolRow(map[string]any{
		"leg_status": "complete",
		"brief":      "Triaged all three scans.",
	})
	compacted := "[compacted tool_result — original ~2240 tokens; map + verbatim head/tail below are the working set]"
	row.Content = compacted
	row.ToolResult.Content = compacted
	resolver := &stubSummaryResolver{
		msgs: append(scoutSurveyFixtureMessages(), row),
	}
	out, workerEvalErr := workercloseout.FinalizeWorkerSummaryForChild(context.Background(), resolver, resolver, "child", "security-reviewer", finalizeOpts(resolver, "child", workercloseout.WorkerSummaryFinalizeOpts{}))
	testutil.FailErr(t, "evaluate worker completion", workerEvalErr)
	if out.Provenance != "complete_leg" || out.Status != "complete" || out.Summary != "Triaged all three scans." {
		t.Fatalf("out = %+v want complete_leg complete", out)
	}
	if resolver.prompts != 0 {
		t.Fatalf("prompts = %d want 0 — no closeout turn after an accepted complete_leg", resolver.prompts)
	}
}

func TestFinalizeWorkerSummaryBlockedLegMapsToPartialStatus(t *testing.T) {
	resolver := &stubSummaryResolver{
		msgs: append(scoutSurveyFixtureMessages(), completeLegToolRow(map[string]any{
			"leg_status": "blocked",
			"brief":      "waiting on missing schema decision",
		})),
	}
	out, workerEvalErr := workercloseout.FinalizeWorkerSummaryForChild(context.Background(), resolver, resolver, "child", "path-explorer", finalizeOpts(resolver, "child", workercloseout.WorkerSummaryFinalizeOpts{}))
	testutil.FailErr(t, "evaluate worker completion", workerEvalErr)
	if out.Status != "partial" {
		t.Fatalf("status = %q want partial for a blocked leg", out.Status)
	}
	if out.Report.LegStatus != "blocked" {
		t.Fatalf("report leg_status = %q want blocked preserved on the report", out.Report.LegStatus)
	}
}

func TestFinalizationDoesNotAddAToolDisabledRepairTurn(t *testing.T) {
	resolver := &stubSummaryResolver{msgs: scoutSurveyFixtureMessages()}
	out, err := workercloseout.FinalizeWorkerSummaryForChild(t.Context(), resolver, resolver, "child", "path-explorer", finalizeOpts(resolver, "child", workercloseout.WorkerSummaryFinalizeOpts{}))
	testutil.FailErr(t, "finalize missing report", err)
	if out.Status != "partial" || resolver.prompts != 0 {
		t.Fatalf("outcome=%+v prompts=%d", out, resolver.prompts)
	}
}

func TestFinalizeWorkerSummaryForCanceledCloseout(t *testing.T) {
	resolver := &stubSummaryResolver{
		closeoutAddsLegTool: true,
		msgs:                scoutSurveyFixtureMessages(),
	}
	out, workerEvalErr := workercloseout.FinalizeWorkerSummaryForCanceled(context.Background(), resolver, resolver, "child", "path-explorer", "wrong scope", finalizeOpts(resolver, "child", workercloseout.WorkerSummaryFinalizeOpts{}))
	testutil.FailErr(t, "evaluate worker completion", workerEvalErr)
	if out.Status != "partial" || out.Provenance != "cancel_complete_leg" {
		t.Fatalf("out = %+v want cancel closeout partial", out)
	}
	if out.Summary != "Partial survey before cancel" {
		t.Fatalf("summary = %q", out.Summary)
	}
	if len(out.Report.RemainingRisk) == 0 || !strings.Contains(strings.Join(out.Report.RemainingRisk, " "), "wrong scope") {
		t.Fatalf("remaining_risk = %v want cancel reason appended", out.Report.RemainingRisk)
	}
}

func TestFinalizeWorkerSummarySynthesizedFromTools(t *testing.T) {
	resolver := &stubSummaryResolver{
		msgs: []api.Message{
			{Role: api.MessageRoleAssistant, ToolCalls: []api.ToolCall{{Name: "read"}}},
			{Role: api.MessageRoleTool, Content: strings.Repeat("file contents here ", 40)},
		},
	}
	out, workerEvalErr := workercloseout.FinalizeWorkerSummaryForChild(context.Background(), resolver, resolver, "child", "path-explorer", finalizeOpts(resolver, "child", workercloseout.WorkerSummaryFinalizeOpts{}))
	testutil.FailErr(t, "evaluate worker completion", workerEvalErr)
	if out.Status != "partial" || out.Provenance != "synthesized+grounding_host_assembly_unavailable" {
		t.Fatalf("out = %+v want synthesized partial", out)
	}
	if out.Report.LegStatus != "partial" || len(out.Report.ObjectivesMet) == 0 {
		t.Fatalf("report = %+v", out.Report)
	}
}

func TestFinalizeWorkerSummaryNoProsePartial(t *testing.T) {
	resolver := &stubSummaryResolver{
		msgs: []api.Message{
			{Role: api.MessageRoleTool, Content: "Rejected: bad"},
		},
	}
	out, workerEvalErr := workercloseout.FinalizeWorkerSummaryForChild(context.Background(), resolver, resolver, "child", "path-explorer", finalizeOpts(resolver, "child", workercloseout.WorkerSummaryFinalizeOpts{Pipeline: sessionTestOARPipeline(t)}))
	testutil.FailErr(t, "evaluate worker completion", workerEvalErr)
	if out.Status != "partial" || out.HintCode != workercompletion.WorkerCompletionReportMissingCode {
		t.Fatalf("out = %+v", out)
	}
	if out.Summary != "" {
		t.Fatalf("summary = %q want empty", out.Summary)
	}
}

func TestFinalizationDoesNotTreatMidRunProseAsAnAcceptedReport(t *testing.T) {
	resolver := &stubSummaryResolver{msgs: []api.Message{{Role: api.MessageRoleAssistant, Content: "Work remains", ToolCalls: []api.ToolCall{{Name: "find"}}}, {Role: api.MessageRoleTool, Content: `{"results":[]}`}}}
	out, err := workercloseout.FinalizeWorkerSummaryForChild(t.Context(), resolver, resolver, "child", "security-reviewer", finalizeOpts(resolver, "child", workercloseout.WorkerSummaryFinalizeOpts{}))
	testutil.FailErr(t, "finalize mid-run prose", err)
	if out.Status != "partial" || resolver.prompts != 0 {
		t.Fatalf("outcome=%+v prompts=%d", out, resolver.prompts)
	}
}
