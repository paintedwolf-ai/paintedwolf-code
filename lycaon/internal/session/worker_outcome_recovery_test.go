package session_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/coordinator/anchor"
	"github.com/lycaon/lycaon/internal/guidance"
	"github.com/lycaon/lycaon/internal/limits"
	"github.com/lycaon/lycaon/internal/promptresult"
	"github.com/lycaon/lycaon/internal/prompts"
	"github.com/lycaon/lycaon/internal/session/store"
	"github.com/lycaon/lycaon/internal/session/workercloseout"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/pkg/api"
)

func loadWorkerSummaryTestFinalizeOpts(t *testing.T) workercloseout.WorkerSummaryFinalizeOpts {
	t.Helper()
	guidance.SetGuidanceRenderer(prompts.NewGuidanceRenderer(prompts.NewFileTemplateEngineLayers(prompts.PromptLayers{})))
	cfg, err := guidance.LoadHintConfigStock()
	testutil.FailErr(t, "LoadHintConfigStock", err)
	return workercloseout.WorkerSummaryFinalizeOpts{
		MaxGroundingRetries: limits.DefaultWorkerGroundingRetries,
		WorkflowHints:       cfg,
		Pipeline:            sessionTestOARPipeline(t),
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
	out, workerEvalErr := workercloseout.FinalizeWorkerSummaryForChild(context.Background(), resolver, resolver, "child", "path-explorer", finalizeOpts(resolver, "child", opts))
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
	out, workerEvalErr := workercloseout.FinalizeWorkerSummaryForChild(context.Background(), resolver, resolver, "child", "path-explorer", finalizeOpts(resolver, "child", opts))
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
	out, workerEvalErr := workercloseout.FinalizeWorkerSummaryForChild(context.Background(), resolver, resolver, "child", "path-explorer", finalizeOpts(resolver, "child", opts))
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
	opts.MaxGroundingRetries = 1
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
	out, workerEvalErr := workercloseout.FinalizeWorkerSummaryForChild(context.Background(), resolver, resolver, "child", "path-explorer", finalizeOpts(resolver, "child", opts))
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
	out, workerEvalErr := workercloseout.FinalizeWorkerSummaryForChild(context.Background(), resolver, resolver, "child", "path-explorer", finalizeOpts(resolver, "child", opts))
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
	opts.MaxGroundingRetries = 1
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
	out, workerEvalErr := workercloseout.FinalizeWorkerSummaryForChild(context.Background(), resolver, resolver, "child", "path-explorer", finalizeOpts(resolver, "child", opts))
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
	opts.MaxGroundingRetries = 1
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
	out, workerEvalErr := workercloseout.FinalizeWorkerSummaryForChild(context.Background(), resolver, resolver, "child", "path-explorer", finalize)
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
		context.Background(), resolver, resolver, "child", "skeptic", opts,
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
		context.Background(), resolver, resolver, "child", "skeptic", opts,
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
	out, workerEvalErr := workercloseout.FinalizeWorkerSummaryForChild(t.Context(), resolver, resolver, "child", "path-explorer", finalizeOpts(resolver, "child", workercloseout.WorkerSummaryFinalizeOpts{
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
	_, err := workercloseout.FinalizeWorkerSummaryForChild(t.Context(), resolver, resolver, "child", "path-explorer", finalizeOpts(resolver, "child", workercloseout.WorkerSummaryFinalizeOpts{
		MaxChars: 50, Pipeline: sessionTestOARPipeline(t),
	}))
	if !errors.Is(err, failure) || resolver.prompts != 1 {
		t.Fatalf("trim retry failure: prompts=%d error=%v", resolver.prompts, err)
	}
}

func TestWorkerGroundingBudgetIsPerLeg(t *testing.T) {
	root := t.TempDir()
	opts := loadWorkerSummaryTestFinalizeOpts(t)
	opts.ProjectDir = root
	opts.AgentType = "path-explorer"

	grepJSON := `{"matches":[{"path":"pkg/main.go","line":1,"content":"package main"}]}`

	makeChildResolver := func() *stubSummaryResolver {
		r := &stubSummaryResolver{
			msgs: []api.Message{
				{
					Role: api.MessageRoleAssistant,
					ToolCalls: []api.ToolCall{
						{ID: "tc1", Name: "grep", Args: map[string]any{"path": ".", "pattern": "main"}},
					},
				},
				{Role: api.MessageRoleTool, Content: grepJSON, ToolResult: &api.ToolResult{Content: grepJSON, Outcome: api.ToolResultOutcomeCompleted}},
				completeLegJSONRow(t, "", `{"leg_status":"complete","findings":[{"path":"pkg/unread0.go","line":10}],"objectives_met":["found bug"],"remaining_risk":[],"suggested_next_task":"","brief":"Survey issue in handler"}`),
			},
		}
		attempt := 0
		r.promptFunc = func(ctx context.Context, sessionID, text string) (*promptresult.Result, error) {
			attempt++
			r.msgs = append(r.msgs, completeLegJSONRow(t, "", fmt.Sprintf(`{"leg_status":"complete","findings":[{"path":"pkg/unread%d.go","line":10}],"objectives_met":["found bug"],"remaining_risk":[],"suggested_next_task":"","brief":"Survey issue in handler"}`, attempt)))
			return &promptresult.Result{MessageID: fmt.Sprintf("retry-%d", attempt)}, nil
		}
		return r
	}

	// Leg 1 spends its 3 grounding retries.
	r1 := makeChildResolver()
	out1, err1 := workercloseout.FinalizeWorkerSummaryForChild(t.Context(), r1, r1, "child1", "path-explorer", finalizeOpts(r1, "child1", opts))
	testutil.FailErr(t, "evaluate leg 1", err1)
	if r1.prompts != limits.DefaultWorkerGroundingRetries {
		t.Fatalf("leg 1 prompts = %d, want %d", r1.prompts, limits.DefaultWorkerGroundingRetries)
	}
	if out1.Status != "partial" || !out1.HostAssembled {
		t.Fatalf("leg 1 out = %+v, want host-assembled partial after retries", out1)
	}

	// Leg 2 under same configuration gets its own 3 retries (not exhausted by leg 1).
	r2 := makeChildResolver()
	out2, err2 := workercloseout.FinalizeWorkerSummaryForChild(t.Context(), r2, r2, "child2", "path-explorer", finalizeOpts(r2, "child2", opts))
	testutil.FailErr(t, "evaluate leg 2", err2)
	if r2.prompts != limits.DefaultWorkerGroundingRetries {
		t.Fatalf("leg 2 prompts = %d, want %d (must have own retry budget)", r2.prompts, limits.DefaultWorkerGroundingRetries)
	}
	if out2.Status != "partial" || !out2.HostAssembled {
		t.Fatalf("leg 2 out = %+v, want host-assembled partial after retries", out2)
	}
}
