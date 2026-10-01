package session_test

import (
	"context"
	"encoding/json"
	"errors"
	"github.com/lycaon/lycaon/internal/promptresult"
	"sort"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/coordinator/anchor"
	"github.com/lycaon/lycaon/internal/guidance"
	"github.com/lycaon/lycaon/internal/guidance/ledgertest"
	"github.com/lycaon/lycaon/internal/limits"
	"github.com/lycaon/lycaon/internal/prompts"
	"github.com/lycaon/lycaon/internal/session"
	"github.com/lycaon/lycaon/internal/session/store"
	"github.com/lycaon/lycaon/internal/session/stream"
	"github.com/lycaon/lycaon/internal/session/workercloseout"
	"github.com/lycaon/lycaon/internal/session/workercompletion"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/pkg/api"
)

type stubSummaryResolver struct {
	prompts                        int
	promptErr                      error
	closeoutAddsLegTool            bool
	trimAddsProse                  bool
	trimAddsLegTool                bool
	citationGroundingFixOnRetry    bool
	citationGroundingFixViaLegTool bool
	msgs                           []api.Message
	streamByMsg                    map[string]string
	supersededReports              []string
	fullTranscriptReads            int
	workerJobTranscriptReads       int
	projectDir                     string
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
	if s.promptErr != nil {
		return nil, s.promptErr
	}
	if s.closeoutAddsLegTool && strings.Contains(text, "[host:worker-closeout]") {
		s.msgs = append(s.msgs, completeLegToolRow(map[string]any{"leg_status": "complete", "objectives_met": []string{"surveyed layout"}, "brief": "Survey complete"}))
	}
	if s.closeoutAddsLegTool && strings.Contains(text, "[host:worker-cancel-closeout]") {
		s.msgs = append(s.msgs, completeLegToolRow(map[string]any{"leg_status": "partial", "objectives_met": []string{"mapped partial layout"}, "remaining_risk": []string{"canceled mid-survey"}, "brief": "Partial survey before cancel"}))
	}
	if s.trimAddsProse && strings.Contains(text, "[host:worker-summary-trim]") {
		s.msgs = append(s.msgs, api.Message{
			Role:    api.MessageRoleAssistant,
			Content: `{"leg_status":"complete","objectives_met":["ok"],"remaining_risk":[],"suggested_next_task":"","brief":"short survey"}`,
		})
	}
	if s.trimAddsLegTool && strings.Contains(text, "[host:worker-summary-trim]") {
		s.msgs = append(s.msgs,
			api.Message{
				Role:      api.MessageRoleAssistant,
				ToolCalls: []api.ToolCall{{ID: "leg-trim", Name: "complete_leg"}},
			},
			completeLegToolRow(map[string]any{
				"leg_status": "complete",
				"brief":      "short survey via tool",
			}),
		)
	}
	if s.citationGroundingFixOnRetry && strings.Contains(text, "[host:worker-citation-grounding]") {
		readJSON := `{"path":"src/x.go","content":"1|package x\n","offset":1,"end_line":1,"limit":10}`
		s.msgs = append(s.msgs,
			api.Message{
				Role: api.MessageRoleAssistant,
				ToolCalls: []api.ToolCall{
					{ID: "read-x", Name: "read", Args: map[string]any{"path": "src/x.go", "offset": 1, "limit": 10}},
				},
			},
			api.Message{Role: api.MessageRoleTool, Content: readJSON, ToolResult: &api.ToolResult{Content: readJSON, Outcome: api.ToolResultOutcomeCompleted}},
		)
		if s.citationGroundingFixViaLegTool {
			s.msgs = append(s.msgs,
				api.Message{
					Role:      api.MessageRoleAssistant,
					ToolCalls: []api.ToolCall{{ID: "leg-retry", Name: "complete_leg"}},
				},
				completeLegNamedRow("grounded-report", map[string]any{
					"leg_status": "complete",
					"findings":   []any{map[string]any{"path": "src/x.go", "line": 1, "excerpt": "package x"}},
					"brief":      "Surveyed handler at src/x.go",
				}),
			)
		} else {
			s.msgs = append(s.msgs, api.Message{
				ID:      "grounded-report",
				Role:    api.MessageRoleAssistant,
				Content: `{"leg_status":"complete","findings":[{"path":"src/x.go","line":1,"excerpt":"package x"}],"brief":"Surveyed handler at src/x.go"}`,
			})
		}
		s.stampEvidenceHandles()
	}
	return &promptresult.Result{MessageID: "closeout-1"}, nil
}

func (s *stubSummaryResolver) PromptWorker(ctx context.Context, sessionID, _ string, text string) (*promptresult.Result, error) {
	return s.Prompt(ctx, sessionID, text)
}

func (s *stubSummaryResolver) SupersedeWorkerReport(ctx context.Context, sessionID, messageID string) error {
	s.supersededReports = append(s.supersededReports, messageID)
	for i := range s.msgs {
		if s.msgs[i].ID == messageID {
			s.msgs[i].Kind = api.MessageKindSuperseded
			break
		}
	}
	return nil
}

func (s *stubSummaryResolver) SpawnChild(ctx context.Context, parentID string, req api.SpawnChildRequest) (*api.Session, error) {
	return &api.Session{ID: "child"}, nil
}

func (s *stubSummaryResolver) AppendWorkerSummary(ctx context.Context, parentID string, in session.WorkerSummaryInput) (string, error) {
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
	return api.Message{
		Role: api.MessageRoleTool,
		ToolResult: &api.ToolResult{
			Tool:     "complete_leg",
			Content:  `{"recorded":true}`,
			ToolArgs: args,
			Outcome:  api.ToolResultOutcomeCompleted,
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
	out, workerEvalErr := workercloseout.FinalizeWorkerSummaryForChild(context.Background(), resolver, "child", "path-explorer", finalizeOpts(resolver, "child", workercloseout.WorkerSummaryFinalizeOpts{}))
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
	currentMessages := scoutSurveyFixtureMessages()
	for i := range currentMessages {
		currentMessages[i].WorkerID = "job-current"
	}
	currentReport := completeLegToolRow(map[string]any{
		"leg_status": "complete",
		"brief":      "current run",
	})
	currentReport.WorkerID = "job-current"
	resolver := &stubSummaryResolver{msgs: append([]api.Message{oldReport}, append(currentMessages, currentReport)...)}
	opts := finalizeOpts(resolver, "child", workercloseout.WorkerSummaryFinalizeOpts{WorkerJobID: "job-current"})

	out, workerEvalErr := workercloseout.FinalizeWorkerSummaryForChild(
		context.Background(), resolver, "child", "path-explorer", opts,
	)
	testutil.FailErr(t, "evaluate worker completion", workerEvalErr)

	if out.Report.Brief != "current run" {
		t.Fatalf("brief = %q want current run", out.Report.Brief)
	}
}

func TestFinalizeWorkerSummaryCompleteLegSurvivesCompactedBody(t *testing.T) {
	// A compacted result body keeps the report without a closeout turn: the
	// report decodes from ToolArgs.
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
	out, workerEvalErr := workercloseout.FinalizeWorkerSummaryForChild(context.Background(), resolver, "child", "security-reviewer", finalizeOpts(resolver, "child", workercloseout.WorkerSummaryFinalizeOpts{}))
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
	out, workerEvalErr := workercloseout.FinalizeWorkerSummaryForChild(context.Background(), resolver, "child", "path-explorer", finalizeOpts(resolver, "child", workercloseout.WorkerSummaryFinalizeOpts{}))
	testutil.FailErr(t, "evaluate worker completion", workerEvalErr)
	if out.Status != "partial" {
		t.Fatalf("status = %q want partial for a blocked leg", out.Status)
	}
	if out.Report.LegStatus != "blocked" {
		t.Fatalf("report leg_status = %q want blocked preserved on the report", out.Report.LegStatus)
	}
}

func TestFinalizeWorkerSummaryCloseoutRecordsCompleteLeg(t *testing.T) {
	resolver := &stubSummaryResolver{
		closeoutAddsLegTool: true,
		msgs:                scoutSurveyFixtureMessages(),
	}
	out, workerEvalErr := workercloseout.FinalizeWorkerSummaryForChild(context.Background(), resolver, "child", "path-explorer", finalizeOpts(resolver, "child", workercloseout.WorkerSummaryFinalizeOpts{}))
	testutil.FailErr(t, "evaluate worker completion", workerEvalErr)
	if out.Status != "complete" || out.Provenance != "closeout_complete_leg" {
		t.Fatalf("out = %+v want closeout complete", out)
	}
	if out.Report.LegStatus != "complete" || out.Summary != "Survey complete" {
		t.Fatalf("report = %+v summary=%q", out.Report, out.Summary)
	}
	if resolver.prompts != 1 {
		t.Fatalf("prompts = %d want 1 closeout", resolver.prompts)
	}
}

func TestFinalizeWorkerSummaryForCanceledCloseout(t *testing.T) {
	resolver := &stubSummaryResolver{
		closeoutAddsLegTool: true,
		msgs:                scoutSurveyFixtureMessages(),
	}
	out, workerEvalErr := workercloseout.FinalizeWorkerSummaryForCanceled(context.Background(), resolver, "child", "path-explorer", "wrong scope", finalizeOpts(resolver, "child", workercloseout.WorkerSummaryFinalizeOpts{}))
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
	// The closeout prompt adds no prose, so synthesis runs.
	resolver.prompts = 0
	out, workerEvalErr := workercloseout.FinalizeWorkerSummaryForChild(context.Background(), resolver, "child", "path-explorer", finalizeOpts(resolver, "child", workercloseout.WorkerSummaryFinalizeOpts{}))
	testutil.FailErr(t, "evaluate worker completion", workerEvalErr)
	if out.Status != "partial" || out.Provenance != "synthesized" {
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
	out, workerEvalErr := workercloseout.FinalizeWorkerSummaryForChild(context.Background(), resolver, "child", "path-explorer", finalizeOpts(resolver, "child", workercloseout.WorkerSummaryFinalizeOpts{Pipeline: sessionTestOARPipeline(t)}))
	testutil.FailErr(t, "evaluate worker completion", workerEvalErr)
	if out.Status != "partial" || out.HintCode != workercompletion.WorkerCompletionReportMissingCode {
		t.Fatalf("out = %+v", out)
	}
	if out.Summary != "" {
		t.Fatalf("summary = %q want empty", out.Summary)
	}
}

func TestFinalizeWorkerSummaryTrimRejectsProseOnlyAnswer(t *testing.T) {
	guidance.SetGuidanceRenderer(prompts.NewGuidanceRenderer(prompts.NewFileTemplateEngineLayers(prompts.PromptLayers{})))

	longBrief := strings.Repeat("x", 200)
	resolver := &stubSummaryResolver{
		trimAddsProse: true,
		msgs:          append(scoutSurveyFixtureMessages(), completeLegToolRow(map[string]any{"leg_status": "complete", "brief": longBrief})),
	}
	out, workerEvalErr := workercloseout.FinalizeWorkerSummaryForChild(context.Background(), resolver, "child", "path-explorer", finalizeOpts(resolver, "child", workercloseout.WorkerSummaryFinalizeOpts{
		MaxChars: 50,
		Pipeline: sessionTestOARPipeline(t),
	}))
	testutil.FailErr(t, "evaluate worker completion", workerEvalErr)
	if out.Status != "partial" || out.HintCode != workercloseout.WorkerSummaryTooLongCode || out.PolicyFeedback == nil {
		t.Fatalf("prose bypassed complete_leg closeout: %+v", out)
	}
	if resolver.prompts != 1 {
		t.Fatalf("prompts = %d want 1 trim retry", resolver.prompts)
	}
}

func TestFinalizeWorkerSummaryTooLongTrimRetryAcceptsCompleteLegAnswer(t *testing.T) {
	// The trim kick teaches complete_leg; only the retry turn's call counts —
	// the pre-retry call is the report that was over budget.
	guidance.SetGuidanceRenderer(prompts.NewGuidanceRenderer(prompts.NewFileTemplateEngineLayers(prompts.PromptLayers{})))

	resolver := &stubSummaryResolver{
		trimAddsLegTool: true,
		msgs: append(scoutSurveyFixtureMessages(), completeLegToolRow(map[string]any{
			"leg_status": "complete",
			"brief":      strings.Repeat("x", 200),
		})),
	}
	out, workerEvalErr := workercloseout.FinalizeWorkerSummaryForChild(context.Background(), resolver, "child", "path-explorer", finalizeOpts(resolver, "child", workercloseout.WorkerSummaryFinalizeOpts{
		MaxChars: 50,
		Pipeline: sessionTestOARPipeline(t),
	}))
	testutil.FailErr(t, "evaluate worker completion", workerEvalErr)
	if out.Status != "complete" || out.Summary != "short survey via tool" {
		t.Fatalf("out = %+v", out)
	}
	if !strings.Contains(out.Provenance, "trimmed") {
		t.Fatalf("provenance = %q want trimmed", out.Provenance)
	}
	if resolver.prompts != 1 {
		t.Fatalf("prompts = %d want 1 trim retry", resolver.prompts)
	}
}

func TestFinalizeWorkerSummaryTooLongPartialWhenTrimFails(t *testing.T) {
	guidance.SetGuidanceRenderer(prompts.NewGuidanceRenderer(prompts.NewFileTemplateEngineLayers(prompts.PromptLayers{})))

	longBrief := strings.Repeat("x", 200)
	longJSON := `{"leg_status":"complete","objectives_met":["ok"],"remaining_risk":[],"suggested_next_task":"","brief":"` + longBrief + `"}`
	resolver := &stubSummaryResolver{
		msgs: append(scoutSurveyFixtureMessages(), completeLegJSONRow(t, "", longJSON)),
	}
	out, workerEvalErr := workercloseout.FinalizeWorkerSummaryForChild(context.Background(), resolver, "child", "path-explorer", finalizeOpts(resolver, "child", workercloseout.WorkerSummaryFinalizeOpts{
		MaxChars: 50,
		Pipeline: sessionTestOARPipeline(t),
	}))
	testutil.FailErr(t, "evaluate worker completion", workerEvalErr)
	if out.Status != "partial" || out.HintCode != workercloseout.WorkerSummaryTooLongCode {
		t.Fatalf("out = %+v", out)
	}
	if out.Report.Brief != longBrief || out.PolicyFeedback == nil {
		t.Fatalf("summary = %q want retained report and frozen feedback", out.Summary)
	}
}

func TestFinalizeWorkerSummaryMidRunProseTriggersCloseout(t *testing.T) {
	resolver := &stubSummaryResolver{
		closeoutAddsLegTool: true,
		msgs: []api.Message{
			{Role: api.MessageRoleAssistant, Content: "Now let me run parallel discovery and targeted searches.", ToolCalls: []api.ToolCall{{Name: "find"}}},
			{Role: api.MessageRoleTool, Content: `{"results":[]}`},
			{ID: "empty-terminal", Role: api.MessageRoleAssistant, Content: ""},
		},
	}
	out, workerEvalErr := workercloseout.FinalizeWorkerSummaryForChild(context.Background(), resolver, "child", "security-reviewer", finalizeOpts(resolver, "child", workercloseout.WorkerSummaryFinalizeOpts{}))
	testutil.FailErr(t, "evaluate worker completion", workerEvalErr)
	if out.Status != "complete" || out.Provenance != "closeout_complete_leg" {
		t.Fatalf("out = %+v want closeout complete (mid-run prose must not satisfy survey)", out)
	}
	if strings.Contains(out.Summary, "Now let me run parallel") {
		t.Fatalf("summary must not reuse mid-run intent prose: %q", out.Summary)
	}
	if resolver.prompts != 1 {
		t.Fatalf("prompts = %d want 1 closeout", resolver.prompts)
	}
}

func loadWorkerSummaryTestFinalizeOpts(t *testing.T) workercloseout.WorkerSummaryFinalizeOpts {
	t.Helper()
	guidance.SetGuidanceRenderer(prompts.NewGuidanceRenderer(prompts.NewFileTemplateEngineLayers(prompts.PromptLayers{})))
	cfg, err := guidance.LoadHintConfigStock()
	testutil.FailErr(t, "LoadHintConfigStock", err)
	return workercloseout.WorkerSummaryFinalizeOpts{
		MaxCitationGroundingRetries: limits.DefaultCitationGroundingRetries,
		WorkflowHints:               cfg,
		Pipeline:                    sessionTestOARPipeline(t),
	}
}

func TestFinalizeCitationGroundingRetrySuccess(t *testing.T) {
	root := t.TempDir()
	opts := loadWorkerSummaryTestFinalizeOpts(t)
	opts.ProjectDir = root
	opts.AgentType = "path-explorer"

	grepJSON := `{"matches":[{"path":"pkg/main.go","line":1,"content":"package main"}]}`
	badJSON := `{"leg_status":"complete","findings":[{"path":"pkg/unread.go","line":10}],"objectives_met":["found bug"],"remaining_risk":[],"suggested_next_task":"","brief":"Survey issue in handler"}`
	resolver := &stubSummaryResolver{
		citationGroundingFixOnRetry:    true,
		citationGroundingFixViaLegTool: true,
		msgs: []api.Message{
			{
				Role: api.MessageRoleAssistant,
				ToolCalls: []api.ToolCall{
					{ID: "tc1", Name: "grep", Args: map[string]any{"path": ".", "pattern": "main"}},
				},
			},
			{Role: api.MessageRoleTool, Content: grepJSON, ToolResult: &api.ToolResult{Content: grepJSON, Outcome: api.ToolResultOutcomeCompleted}},
			completeLegJSONRow(t, "", badJSON),
		},
	}
	out, workerEvalErr := workercloseout.FinalizeWorkerSummaryForChild(context.Background(), resolver, "child", "path-explorer", finalizeOpts(resolver, "child", opts))
	testutil.FailErr(t, "evaluate worker completion", workerEvalErr)
	if out.Status != "complete" {
		t.Fatalf("out = %+v want complete after grounding retry", out)
	}
	if out.Summary != "Surveyed handler at src/x.go" {
		t.Fatalf("summary = %q", out.Summary)
	}
	if out.Provenance != "complete_leg+grounding_retry+attempt_1" {
		t.Fatalf("provenance = %q want complete_leg+grounding_retry+attempt_1", out.Provenance)
	}
	if resolver.prompts != 1 {
		t.Fatalf("prompts = %d want 1 grounding retry", resolver.prompts)
	}
}

func TestFinalizeCitationGroundingRetryAcceptsCompleteLegAnswer(t *testing.T) {
	// A grounding retry may answer through complete_leg.
	root := t.TempDir()
	opts := loadWorkerSummaryTestFinalizeOpts(t)
	opts.ProjectDir = root
	opts.AgentType = "path-explorer"

	grepJSON := `{"matches":[{"path":"pkg/main.go","line":1,"content":"package main"}]}`
	resolver := &stubSummaryResolver{
		citationGroundingFixOnRetry:    true,
		citationGroundingFixViaLegTool: true,
		msgs: []api.Message{
			{
				Role: api.MessageRoleAssistant,
				ToolCalls: []api.ToolCall{
					{ID: "tc1", Name: "grep", Args: map[string]any{"path": ".", "pattern": "main"}},
				},
			},
			{Role: api.MessageRoleTool, Content: grepJSON, ToolResult: &api.ToolResult{Content: grepJSON, Outcome: api.ToolResultOutcomeCompleted}},
			completeLegToolRow(map[string]any{
				"leg_status": "complete",
				"findings":   []any{map[string]any{"path": "pkg/unread.go", "line": 10}},
				"brief":      "Survey issue in handler",
			}),
		},
	}
	out, workerEvalErr := workercloseout.FinalizeWorkerSummaryForChild(context.Background(), resolver, "child", "path-explorer", finalizeOpts(resolver, "child", opts))
	testutil.FailErr(t, "evaluate worker completion", workerEvalErr)
	if out.Status != "complete" {
		t.Fatalf("out = %+v want complete after tool-answered grounding retry", out)
	}
	if out.Summary != "Surveyed handler at src/x.go" {
		t.Fatalf("summary = %q want the retry report, not the ungrounded one", out.Summary)
	}
	if out.Provenance != "complete_leg+grounding_retry+attempt_1" {
		t.Fatalf("provenance = %q", out.Provenance)
	}
	if resolver.prompts != 1 {
		t.Fatalf("prompts = %d want 1 grounding retry", resolver.prompts)
	}
}

func TestFinalizeCitationGroundingSupersedesRejectedReport(t *testing.T) {
	root := t.TempDir()
	opts := loadWorkerSummaryTestFinalizeOpts(t)
	opts.ProjectDir = root
	opts.AgentType = "path-explorer"

	grepJSON := `{"matches":[{"path":"pkg/main.go","line":1,"content":"package main"}]}`
	badJSON := `{"leg_status":"complete","findings":[{"path":"pkg/unread.go","line":10}],"objectives_met":["found bug"],"remaining_risk":[],"suggested_next_task":"","brief":"Survey issue in handler"}`
	resolver := &stubSummaryResolver{
		citationGroundingFixOnRetry:    true,
		citationGroundingFixViaLegTool: true,
		msgs: []api.Message{
			{
				Role: api.MessageRoleAssistant,
				ToolCalls: []api.ToolCall{
					{ID: "tc1", Name: "grep", Args: map[string]any{"path": ".", "pattern": "main"}},
				},
			},
			{Role: api.MessageRoleTool, Content: grepJSON, ToolResult: &api.ToolResult{Content: grepJSON, Outcome: api.ToolResultOutcomeCompleted}},
			completeLegJSONRow(t, "draft-report", badJSON),
		},
	}
	out, workerEvalErr := workercloseout.FinalizeWorkerSummaryForChild(context.Background(), resolver, "child", "path-explorer", finalizeOpts(resolver, "child", opts))
	testutil.FailErr(t, "evaluate worker completion", workerEvalErr)
	if out.Status != "complete" {
		t.Fatalf("out = %+v want complete after grounding retry", out)
	}
	// The rejected draft remains superseded beside the grounded report.
	if len(resolver.supersededReports) != 1 || resolver.supersededReports[0] != "draft-report" {
		t.Fatalf("supersededReports = %v want [draft-report]", resolver.supersededReports)
	}
	var superseded, grounded *api.Message
	for i := range resolver.msgs {
		if resolver.msgs[i].ID == "draft-report" {
			superseded = &resolver.msgs[i]
		}
		if resolver.msgs[i].ID == "grounded-report" {
			grounded = &resolver.msgs[i]
		}
	}
	if superseded == nil {
		t.Fatal("rejected draft-report must remain in child transcript (superseded, not deleted)")
	}
	if superseded.Kind != api.MessageKindSuperseded {
		t.Fatalf("draft-report kind = %q want superseded", superseded.Kind)
	}
	if grounded == nil || grounded.Visibility == api.MessageVisibilityInternal {
		t.Fatal("final grounded report must remain visible in the worker panel")
	}
}

func TestFinalizeCitationGroundingAuditsFullBrief(t *testing.T) {
	// The citation sits beyond the first 600 bytes.
	root := t.TempDir()
	opts := loadWorkerSummaryTestFinalizeOpts(t)
	opts.MaxCitationGroundingRetries = 1
	opts.ProjectDir = root
	opts.AgentType = "path-explorer"

	grepJSON := `{"matches":[{"path":"pkg/main.go","line":1,"content":"package main"}]}`
	longBrief := strings.Repeat("x", 650) + " narrative only"
	badJSON := `{"leg_status":"complete","findings":[{"path":"pkg/unread.go","line":10}],"objectives_met":["found bug"],"remaining_risk":[],"suggested_next_task":"","brief":"` + longBrief + `"}`
	resolver := &stubSummaryResolver{
		msgs: []api.Message{
			{
				Role: api.MessageRoleAssistant,
				ToolCalls: []api.ToolCall{
					{ID: "tc1", Name: "grep", Args: map[string]any{"path": ".", "pattern": "main"}},
				},
			},
			{Role: api.MessageRoleTool, Content: grepJSON, ToolResult: &api.ToolResult{Content: grepJSON, Outcome: api.ToolResultOutcomeCompleted}},
			completeLegJSONRow(t, "", badJSON),
		},
	}
	out, workerEvalErr := workercloseout.FinalizeWorkerSummaryForChild(context.Background(), resolver, "child", "path-explorer", finalizeOpts(resolver, "child", opts))
	testutil.FailErr(t, "evaluate worker completion", workerEvalErr)
	// Retry exhaustion binds the full brief to observed evidence.
	if out.Status != "partial" || !out.HostAssembled || out.PolicyFeedback == nil {
		t.Fatalf("out = %+v want rejected host-assembled partial after retries spent", out)
	}
	if len(out.Report.Brief) <= 600 {
		t.Fatalf("brief len = %d want full uncapped brief on the outcome", len(out.Report.Brief))
	}
}

func TestFinalizeCitationGroundingKickAdvertisesSummaryBudget(t *testing.T) {
	root := t.TempDir()
	opts := loadWorkerSummaryTestFinalizeOpts(t)
	opts.ProjectDir = root
	opts.AgentType = "path-explorer"
	opts.MaxChars = 12345
	var groundingKickVars []map[string]any
	opts.RenderWorkerKick = func(ctx context.Context, kickID string, data map[string]any) (string, error) {
		if kickID == anchor.InformRender(anchor.WorkerCitationGrounding) {
			groundingKickVars = append(groundingKickVars, data)
		}
		return "[host:" + kickID + "]", nil
	}

	grepJSON := `{"matches":[{"path":"pkg/main.go","line":1,"content":"package main"}]}`
	badJSON := `{"leg_status":"complete","findings":[{"path":"pkg/unread.go","line":10}],"objectives_met":["found bug"],"remaining_risk":[],"suggested_next_task":"","brief":"Survey issue in handler"}`
	resolver := &stubSummaryResolver{
		citationGroundingFixOnRetry:    true,
		citationGroundingFixViaLegTool: true,
		msgs: []api.Message{
			{
				Role: api.MessageRoleAssistant,
				ToolCalls: []api.ToolCall{
					{ID: "tc1", Name: "grep", Args: map[string]any{"path": ".", "pattern": "main"}},
				},
			},
			{Role: api.MessageRoleTool, Content: grepJSON, ToolResult: &api.ToolResult{Content: grepJSON, Outcome: api.ToolResultOutcomeCompleted}},
			completeLegJSONRow(t, "", badJSON),
		},
	}
	out, workerEvalErr := workercloseout.FinalizeWorkerSummaryForChild(context.Background(), resolver, "child", "path-explorer", finalizeOpts(resolver, "child", opts))
	testutil.FailErr(t, "evaluate worker completion", workerEvalErr)
	if out.Status != "complete" {
		t.Fatalf("out = %+v want complete after grounding retry", out)
	}
	if len(groundingKickVars) != 1 {
		t.Fatalf("grounding kicks = %d want 1", len(groundingKickVars))
	}
	// The retry context carries the enforced summary budget.
	if got := groundingKickVars[0]["worker_summary_max_chars"]; got != 12345 {
		t.Fatalf("worker_summary_max_chars = %v want enforced budget 12345", got)
	}
}

func TestFinalizeCitationGroundingHostAssembledWhenRetriesExhausted(t *testing.T) {
	root := t.TempDir()
	opts := loadWorkerSummaryTestFinalizeOpts(t)
	opts.MaxCitationGroundingRetries = 1
	opts.ProjectDir = root
	opts.AgentType = "path-explorer"

	grepJSON := `{"matches":[{"path":"pkg/main.go","line":1,"content":"package main"}]}`
	badJSON := `{"leg_status":"complete","findings":[{"path":"pkg/unread.go","line":10}],"objectives_met":["found bug"],"remaining_risk":[],"suggested_next_task":"","brief":"Survey issue in handler"}`
	resolver := &stubSummaryResolver{
		msgs: []api.Message{
			{
				Role: api.MessageRoleAssistant,
				ToolCalls: []api.ToolCall{
					{ID: "tc1", Name: "grep", Args: map[string]any{"path": ".", "pattern": "main"}},
				},
			},
			{Role: api.MessageRoleTool, Content: grepJSON, ToolResult: &api.ToolResult{Content: grepJSON, Outcome: api.ToolResultOutcomeCompleted}},
			completeLegJSONRow(t, "", badJSON),
		},
	}
	out, workerEvalErr := workercloseout.FinalizeWorkerSummaryForChild(context.Background(), resolver, "child", "path-explorer", finalizeOpts(resolver, "child", opts))
	testutil.FailErr(t, "evaluate worker completion", workerEvalErr)
	// Repeated offenders trigger host-bound grounding.
	if out.Status != "partial" || !out.HostAssembled || out.PolicyFeedback == nil {
		t.Fatalf("out = %+v want rejected host-assembled partial when retry re-emits same offenders", out)
	}
	if !strings.Contains(out.Provenance, "grounding_host_assembled") {
		t.Fatalf("provenance = %q want grounding_host_assembled", out.Provenance)
	}
	if resolver.prompts != 1 {
		t.Fatalf("prompts = %d want 1 failed retry", resolver.prompts)
	}
	for _, finding := range out.Report.Findings {
		if finding.Path == "pkg/unread.go" {
			t.Fatalf("host-assembled report retained unobserved finding: %+v", finding)
		}
	}
}

func TestFinalizeCitationGroundingDoesNotCertifyWithoutLedger(t *testing.T) {
	root := t.TempDir()
	opts := loadWorkerSummaryTestFinalizeOpts(t)
	opts.MaxCitationGroundingRetries = 1
	opts.ProjectDir = root
	opts.AgentType = "path-explorer"

	grepJSON := `{"matches":[{"path":"pkg/main.go","line":1,"content":"package main"}]}`
	badJSON := `{"leg_status":"complete","findings":[{"path":"pkg/unread.go","line":10}],"objectives_met":["found bug"],"remaining_risk":[],"suggested_next_task":"","brief":"Survey issue in handler"}`
	resolver := &stubSummaryResolver{msgs: []api.Message{
		{Role: api.MessageRoleAssistant, ToolCalls: []api.ToolCall{
			{ID: "tc1", Name: "grep", Args: map[string]any{"path": ".", "pattern": "main"}},
		}},
		{Role: api.MessageRoleTool, Content: grepJSON, ToolResult: &api.ToolResult{Content: grepJSON, Outcome: api.ToolResultOutcomeCompleted}},
		completeLegJSONRow(t, "", badJSON),
	}}
	finalize := finalizeOpts(resolver, "child", opts)
	finalize.Ledger = nil
	out, workerEvalErr := workercloseout.FinalizeWorkerSummaryForChild(context.Background(), resolver, "child", "path-explorer", finalize)
	testutil.FailErr(t, "evaluate worker completion", workerEvalErr)
	if out.Status != "partial" || out.HintCode == "" || out.HostAssembled {
		t.Fatalf("out = %+v want unresolved grounding without a ledger", out)
	}
}

// PromptHostTurn captures closeout prompts.
func (s *stubSummaryResolver) PromptHostTurn(ctx context.Context, sessionID string, _ store.PromptSubmissionOrigin, text string) (*promptresult.Result, error) {
	return s.Prompt(ctx, sessionID, text)
}

// A pending decision parks the worker run.
func TestFinalizeWorkerSummaryParksOnAPendingDecision(t *testing.T) {
	resolver := &stubSummaryResolver{msgs: scoutSurveyFixtureMessages()}
	opts := finalizeOpts(resolver, "child", workercloseout.WorkerSummaryFinalizeOpts{
		DecisionPending: func(context.Context, string) bool { return true },
	})
	out, workerEvalErr := workercloseout.FinalizeWorkerSummaryForChild(
		context.Background(), resolver, "child", "skeptic", opts,
	)
	testutil.FailErr(t, "evaluate worker completion", workerEvalErr)
	if out.Status != string(api.WorkerSummaryStatusNeedsDecision) {
		t.Fatalf("status = %q want needs_decision", out.Status)
	}
	if resolver.prompts != 0 {
		t.Fatalf("prompts = %d want 0 — a parked worker is not given a forced closeout turn", resolver.prompts)
	}
}

func TestFinalizeWorkerSummaryStillClosesOutWithoutAPendingDecision(t *testing.T) {
	resolver := &stubSummaryResolver{msgs: scoutSurveyFixtureMessages()}
	opts := finalizeOpts(resolver, "child", workercloseout.WorkerSummaryFinalizeOpts{
		DecisionPending: func(context.Context, string) bool { return false },
	})
	out, workerEvalErr := workercloseout.FinalizeWorkerSummaryForChild(
		context.Background(), resolver, "child", "skeptic", opts,
	)
	testutil.FailErr(t, "evaluate worker completion", workerEvalErr)
	if out.Status == string(api.WorkerSummaryStatusNeedsDecision) {
		t.Fatalf("status = %q — nothing was pending, so the closeout path still handles this", out.Status)
	}
}

func TestFinalizeWorkerSummaryBudgetCountsUnicodeCharacters(t *testing.T) {
	brief := strings.Repeat("界🚀", 25)
	resolver := &stubSummaryResolver{msgs: append(scoutSurveyFixtureMessages(), completeLegToolRow(map[string]any{
		"leg_status": "complete", "brief": brief,
	}))}
	out, workerEvalErr := workercloseout.FinalizeWorkerSummaryForChild(t.Context(), resolver, "child", "path-explorer", finalizeOpts(resolver, "child", workercloseout.WorkerSummaryFinalizeOpts{
		MaxChars: 50, Pipeline: sessionTestOARPipeline(t),
	}))
	testutil.FailErr(t, "evaluate worker completion", workerEvalErr)
	if resolver.prompts != 0 || out.Summary != brief || out.Status != "complete" {
		t.Fatalf("in-budget Unicode report was trimmed or lost: prompts=%d outcome=%+v", resolver.prompts, out)
	}
}

func completeLegNamedRow(id string, args map[string]any) api.Message {
	message := completeLegToolRow(args)
	message.ID = id
	return message
}
func completeLegJSONRow(t *testing.T, id, raw string) api.Message {
	t.Helper()
	var args map[string]any
	testutil.FailErr(t, "decode complete_leg fixture", json.Unmarshal([]byte(raw), &args))
	return completeLegNamedRow(id, args)
}

func TestFinalizeWorkerSummaryPropagatesTrimRetryFailure(t *testing.T) {
	guidance.SetGuidanceRenderer(prompts.NewGuidanceRenderer(prompts.NewFileTemplateEngineLayers(prompts.PromptLayers{})))
	failure := errors.New("host turn unavailable")
	resolver := &stubSummaryResolver{
		promptErr: failure,
		msgs: append(scoutSurveyFixtureMessages(), completeLegToolRow(map[string]any{
			"leg_status": "complete", "brief": strings.Repeat("x", 200),
		})),
	}
	_, err := workercloseout.FinalizeWorkerSummaryForChild(t.Context(), resolver, "child", "path-explorer", finalizeOpts(resolver, "child", workercloseout.WorkerSummaryFinalizeOpts{
		MaxChars: 50, Pipeline: sessionTestOARPipeline(t),
	}))
	if !errors.Is(err, failure) || resolver.prompts != 1 {
		t.Fatalf("trim retry failure: prompts=%d error=%v", resolver.prompts, err)
	}
}
