package guard_test

import (
	"context"
	"time"

	"github.com/lycaon/lycaon/internal/oar"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/coordinator/guard"
	"github.com/lycaon/lycaon/internal/evidence"
	"github.com/lycaon/lycaon/internal/guidance"
	"github.com/lycaon/lycaon/internal/guidance/ledgertest"
	"github.com/lycaon/lycaon/internal/orchestration"
	"github.com/lycaon/lycaon/internal/prompts"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/tools"
	"github.com/lycaon/lycaon/pkg/api"
)

func TestEvaluateCoordinatorCloseoutGrounding_blocksUnobservedPath(t *testing.T) {
	root := t.TempDir()
	sess := &api.Session{ID: "parent-1", WorkspacePath: root}
	grepJSON := `{"matches":[{"path":"internal/retention.go","line":1,"content":"package retention"}]}`
	child := []api.Message{
		{
			Role:      api.MessageRoleAssistant,
			ToolCalls: []api.ToolCall{{Name: "grep", ID: "c1", Args: map[string]any{"path": ".", "pattern": "DELETE"}}},
		},
		{Role: api.MessageRoleTool, Content: grepJSON, ToolResult: &api.ToolResult{Content: grepJSON, Outcome: api.ToolResultOutcomeCompleted}},
	}
	envelope := `<task job_id="j1" child_session_id="child-1" agent_type="` + orchestration.ProfileRepoResearcher + `" state="complete">
<task_result>done</task_result>
</task>`
	history := []api.Message{
		{Role: api.MessageRoleUser, Content: "go"},
		{Role: api.MessageRoleTool, Content: envelope, WorkerSummary: &api.WorkerSummaryMeta{
			ChildSessionID: "child-1",
			Status:         api.WorkerSummaryStatusComplete,
		}},
	}
	lookup := func(id string) []api.Message {
		if id == "child-1" {
			return child
		}
		return nil
	}
	ledger := ledgertest.CloseoutReader(root, []guidance.EvidenceLeg{{ChildSessionID: "child-1"}}, lookup)
	report := guidance.CoordinatorCompletionReport{
		Synthesis: "phantom path",
		CitedEvidence: []guidance.CoordinatorCitedEvidence{{
			Path: "phantom.go", Line: 1, Excerpt: "package phantom",
		}},
	}
	verdict, err := guard.EvaluateCoordinatorCloseoutGrounding(
		context.Background(), ledger, sess, history, "implement_synthesis", report, []string{"task"}, evidence.CitationRoots{ProjectDir: root},
	)
	testutil.FailErr(t, "guard.EvaluateCoordinatorCloseoutGrounding failed", err)
	if verdict.Code != guidance.SynthHandleNotInLegsCode {
		t.Fatalf("code=%q want %q", verdict.Code, guidance.SynthHandleNotInLegsCode)
	}
	if verdict.Grounding == nil || verdict.Grounding.Traced {
		t.Fatalf("grounding=%+v want traced=false", verdict.Grounding)
	}
}

func TestEvaluateCoordinatorCloseoutGroundingURLMismatchRequiresRepair(t *testing.T) {
	root := t.TempDir()
	sess := &api.Session{ID: "parent-1", WorkspacePath: root}
	grepJSON := `{"matches":[{"path":"internal/retention.go","line":1,"content":"package retention"}]}`
	child := []api.Message{
		{
			Role:      api.MessageRoleAssistant,
			ToolCalls: []api.ToolCall{{Name: "grep", ID: "c1", Args: map[string]any{"path": ".", "pattern": "retention"}}},
		},
		{Role: api.MessageRoleTool, Content: grepJSON, ToolResult: &api.ToolResult{Content: grepJSON, Outcome: api.ToolResultOutcomeCompleted}},
	}
	envelope := `<task job_id="j1" child_session_id="child-1" agent_type="` + orchestration.ProfileRepoResearcher + `" state="complete">
<task_result>done</task_result>
</task>`
	history := []api.Message{
		{Role: api.MessageRoleUser, Content: "go"},
		{Role: api.MessageRoleTool, Content: envelope, WorkerSummary: &api.WorkerSummaryMeta{
			ChildSessionID: "child-1",
			Status:         api.WorkerSummaryStatusComplete,
		}},
	}
	lookup := func(id string) []api.Message {
		if id == "child-1" {
			return child
		}
		return nil
	}
	ledger := ledgertest.CloseoutReader(root, []guidance.EvidenceLeg{{ChildSessionID: "child-1"}}, lookup)
	report := guidance.CoordinatorCompletionReport{
		Synthesis: "retention surveyed",
		CitedEvidence: []guidance.CoordinatorCitedEvidence{{
			Path: "internal/retention.go", Line: 1, Excerpt: "package retention",
		}},
		CitedURLs: []string{"https://unobserved.example/doc"},
	}
	verdict, err := guard.EvaluateCoordinatorCloseoutGrounding(
		context.Background(), ledger, sess, history, "implement_synthesis", report, []string{"task"}, evidence.CitationRoots{ProjectDir: root},
	)
	testutil.FailErr(t, "guard.EvaluateCoordinatorCloseoutGrounding failed", err)
	if verdict.Code != guidance.SynthURLNotObservedCode || len(verdict.UnobservedURLs) != 1 {
		t.Fatalf("URL verdict = %+v want unobserved URL repair", verdict)
	}
	if verdict.Grounding == nil || verdict.Grounding.Traced {
		t.Fatalf("unobserved URL grounding = %+v want untraced", verdict.Grounding)
	}
}

func TestEvaluateCoordinatorCloseoutGrounding_passesGroundedPath(t *testing.T) {
	root := t.TempDir()
	sess := &api.Session{ID: "parent-1", WorkspacePath: root}
	grepJSON := `{"matches":[{"path":"internal/retention.go","line":1,"content":"package retention"}]}`
	child := []api.Message{
		{
			Role:      api.MessageRoleAssistant,
			ToolCalls: []api.ToolCall{{Name: "grep", ID: "c1", Args: map[string]any{"path": ".", "pattern": "DELETE"}}},
		},
		{Role: api.MessageRoleTool, Content: grepJSON, ToolResult: &api.ToolResult{Content: grepJSON, Outcome: api.ToolResultOutcomeCompleted}},
	}
	envelope := `<task job_id="j1" child_session_id="child-1" agent_type="` + orchestration.ProfileRepoResearcher + `" state="complete">
<task_result>done</task_result>
</task>`
	history := []api.Message{
		{Role: api.MessageRoleUser, Content: "go"},
		{Role: api.MessageRoleTool, Content: envelope, WorkerSummary: &api.WorkerSummaryMeta{
			ChildSessionID: "child-1",
			Status:         api.WorkerSummaryStatusComplete,
		}},
	}
	lookup := func(id string) []api.Message {
		if id == "child-1" {
			return child
		}
		return nil
	}
	ledger := ledgertest.CloseoutReader(root, []guidance.EvidenceLeg{{ChildSessionID: "child-1"}}, lookup)
	report := guidance.CoordinatorCompletionReport{
		Synthesis: "retention surveyed",
		CitedEvidence: []guidance.CoordinatorCitedEvidence{{
			Path: "internal/retention.go", Line: 1, Excerpt: "package retention",
		}},
	}
	verdict, err := guard.EvaluateCoordinatorCloseoutGrounding(
		context.Background(), ledger, sess, history, "implement_synthesis", report, []string{"task"}, evidence.CitationRoots{ProjectDir: root},
	)
	testutil.FailErr(t, "guard.EvaluateCoordinatorCloseoutGrounding failed", err)
	if strings.TrimSpace(verdict.Code) != "" {
		t.Fatalf("code=%q want pass", verdict.Code)
	}
	if verdict.Grounding == nil || !verdict.Grounding.Traced {
		t.Fatalf("grounding=%+v want traced", verdict.Grounding)
	}
}

func TestEvaluateCoordinatorCloseoutGrounding_investigateSurface(t *testing.T) {
	root := t.TempDir()
	sess := &api.Session{ID: "parent-1", WorkspacePath: root}
	readJSON := `{"content":"package foo\n","path":"internal/foo.go"}`
	history := []api.Message{
		{Role: api.MessageRoleUser, Content: "go"},
		{
			Role:      api.MessageRoleAssistant,
			ToolCalls: []api.ToolCall{{Name: "read", ID: "r1", Args: map[string]any{"path": "internal/foo.go"}}},
		},
		{Role: api.MessageRoleTool, Content: readJSON, ToolResult: &api.ToolResult{Content: readJSON, Outcome: api.ToolResultOutcomeCompleted}},
	}
	ledger := investigateLedgerReader{root: root, sessionID: sess.ID, msgs: history[1:]}
	report := guidance.CoordinatorCompletionReport{
		Synthesis: "missing",
		CitedEvidence: []guidance.CoordinatorCitedEvidence{{
			Path: "internal/missing.go", Line: 1, Excerpt: "x",
		}},
	}
	verdict, err := guard.EvaluateCoordinatorCloseoutGrounding(
		context.Background(), ledger, sess, history, tools.SurfaceImplementInvestigate, report, []string{"read"}, evidence.CitationRoots{ProjectDir: root},
	)
	testutil.FailErr(t, "guard.EvaluateCoordinatorCloseoutGrounding failed", err)
	if verdict.Code != guidance.InvestHandleNotObservedCode {
		t.Fatalf("code=%q want %q", verdict.Code, guidance.InvestHandleNotObservedCode)
	}
}

func TestEvaluateCoordinatorCloseoutGrounding_investigateCoordinatorEvidence(t *testing.T) {
	root := t.TempDir()
	sess := &api.Session{ID: "parent-1", WorkspacePath: root}
	readJSON := `{"content":"1|package foo\n","path":"internal/foo.go","offset":1,"end_line":1}`
	history := []api.Message{
		{Role: api.MessageRoleUser, Content: "research"},
		{
			Role:      api.MessageRoleAssistant,
			ToolCalls: []api.ToolCall{{Name: "read", ID: "r1", Args: map[string]any{"path": "internal/foo.go"}}},
		},
		{Role: api.MessageRoleTool, Content: readJSON, ToolResult: &api.ToolResult{Content: readJSON, Outcome: api.ToolResultOutcomeCompleted}},
	}
	ledger := investigateLedgerReader{root: root, sessionID: sess.ID, msgs: history[1:]}
	report := guidance.CoordinatorCompletionReport{
		Synthesis: "found foo",
		CitedEvidence: []guidance.CoordinatorCitedEvidence{{
			Path: "internal/foo.go", Line: 1, Excerpt: "package foo",
		}},
	}
	verdict, err := guard.EvaluateCoordinatorCloseoutGrounding(
		context.Background(), ledger, sess, history, tools.SurfaceImplementInvestigate, report, []string{"read"}, evidence.CitationRoots{ProjectDir: root},
	)
	testutil.FailErr(t, "guard.EvaluateCoordinatorCloseoutGrounding failed", err)
	if strings.TrimSpace(verdict.Code) != "" {
		t.Fatalf("code=%q want pass", verdict.Code)
	}
	if verdict.Grounding == nil || !verdict.Grounding.Traced {
		t.Fatalf("grounding=%+v want traced", verdict.Grounding)
	}
}

func TestFormatCloseoutGroundingReject_rendersFullCitationKick(t *testing.T) {
	t.Parallel()
	rejectFmt := coordinatorRejectFmt(t)
	engine := prompts.NewFileTemplateEngineLayers(prompts.PromptLayers{})
	render := func(kickID string, data map[string]any) string {
		out, err := engine.RenderKick(context.Background(), strings.TrimSpace(kickID), data)
		if err != nil || strings.TrimSpace(out) == "" {
			return ""
		}
		return strings.TrimSpace(out)
	}
	const draft = "Painted Wolf Code is a local-first AI coding agent."
	nudge, err := guard.FormatCloseoutGroundingReject(
		t.Context(),
		rejectFmt,
		render,
		"coordinator-citation-grounding",
		1,
		3,
		&oar.Decision{Code: guidance.InvestHandleNotObservedCode, Data: map[string]any{
			"offenders_sample":        "missing.go:1 (\"fabricated\")",
			"offender_count":          1,
			"observed_handles_sample": "leg-a:list#1, leg-b:read#2",
			"observed_paths_sample":   "src/coropa, src/coropa/main.go",
			"retained_citations":      `{"cited_evidence":[{"evidence":"command#37"}]}`,
			"repair_observations":     `[{"evidence":"git_receipt#1","tool":"git_commit"}]`,
		}},
		draft,
	)
	testutil.FailErr(t, "guard.FormatCloseoutGroundingReject failed", err)
	for _, want := range []string{
		"[host:coordinator-citation-grounding]",
		"Emit only the citations fence",
		"Attempt 1/3",
		draft,
		"Pinned body:",
		"missing.go:1",
		"leg-a:list#1",
		"src/coropa/main.go",
		`{"cited_evidence":[{"evidence":"command#37"}]}`,
		`[{"evidence":"git_receipt#1","tool":"git_commit"}]`,
		"Code: INVEST_HANDLE_NOT_OBSERVED",
	} {
		if !strings.Contains(nudge, want) {
			t.Errorf("nudge missing %q; got:\n%s", want, nudge)
		}
	}
	if strings.Contains(nudge, "re-emit this text verbatim") {
		t.Fatalf("kick must not instruct verbatim re-emit, got:\n%s", nudge)
	}
	if strings.Count(nudge, "[host:coordinator-citation-grounding]") != 1 {
		t.Fatalf("expected single host marker, got %q", nudge)
	}
}

type investigateLedgerReader struct {
	root      string
	sessionID string
	msgs      []api.Message
}

func (r investigateLedgerReader) LoadLedger(_ context.Context, sessionID string) (evidence.Ledger, error) {
	if sessionID != r.sessionID {
		return evidence.Ledger{}, nil
	}
	return ledgertest.BuildFromMessages(r.root, r.msgs), nil
}

func (investigateLedgerReader) WorkerLegs(context.Context, string, time.Time) ([]guidance.EvidenceLeg, error) {
	return nil, nil
}

// [OAR-COPY-1] Closeout retry combines the live retry header with frozen policy copy.
func TestCloseoutGroundingKeepsFrozenCopy(t *testing.T) {
	coordinatorRejectFmt(t)
	d := &oar.Decision{Code: guidance.InvestHandleNotObservedCode, Copy: map[string]string{"what": "Observed 3 missing handles", "fix": "Keep {{ count }} literal"}, Data: map[string]any{"count": 99}}
	text, err := guard.FormatCloseoutGroundingReject(t.Context(), nil, nil, "", 1, 3, d, "draft")
	testutil.FailErr(t, "render frozen closeout retry", err)
	if !strings.Contains(text, "Observed 3 missing handles") || !strings.Contains(text, "{{ count }}") {
		t.Fatalf("lost frozen closeout copy: %s", text)
	}
}
