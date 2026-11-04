package guard_test

import (
	"github.com/lycaon/lycaon/internal/toolcontract"

	"context"
	"time"

	"github.com/lycaon/lycaon/internal/oar"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/citationmd"
	"github.com/lycaon/lycaon/internal/coordinator/guard"
	"github.com/lycaon/lycaon/internal/evidence"
	"github.com/lycaon/lycaon/internal/guidance"
	"github.com/lycaon/lycaon/internal/guidance/ledgertest"
	"github.com/lycaon/lycaon/internal/orchestration"
	"github.com/lycaon/lycaon/internal/prompts"
	"github.com/lycaon/lycaon/internal/testutil"
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
	report := markdownDraft(`A phantom path [phantom.go:1 "package phantom"].`)
	verdict, err := guard.EvaluateCoordinatorCloseoutGrounding(
		context.Background(), ledger, sess, history, "implement_synthesis", report, evidence.CitationRoots{ProjectDir: root},
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
	report := markdownDraft(`Retention surveyed [internal/retention.go:1 "package retention"], per [the doc](https://unobserved.example/doc).`)
	verdict, err := guard.EvaluateCoordinatorCloseoutGrounding(
		context.Background(), ledger, sess, history, "implement_synthesis", report, evidence.CitationRoots{ProjectDir: root},
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
	report := markdownDraft(`Retention surveyed [internal/retention.go:1 "package retention"].`)
	verdict, err := guard.EvaluateCoordinatorCloseoutGrounding(
		context.Background(), ledger, sess, history, "implement_synthesis", report, evidence.CitationRoots{ProjectDir: root},
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
	report := markdownDraft(`Missing [internal/missing.go:1 "x"].`)
	verdict, err := guard.EvaluateCoordinatorCloseoutGrounding(
		context.Background(), ledger, sess, history, toolcontract.SurfaceImplementInvestigate, report, evidence.CitationRoots{ProjectDir: root},
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
	report := markdownDraft(`Found foo [internal/foo.go:1 "package foo"].`)
	verdict, err := guard.EvaluateCoordinatorCloseoutGrounding(
		context.Background(), ledger, sess, history, toolcontract.SurfaceImplementInvestigate, report, evidence.CitationRoots{ProjectDir: root},
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
			"repair_targets":          []map[string]any{{"id": "c2", "token": "missing.go:1", "defect": "not_observed", "web_link": false}},
			"repair_observations":     `[{"evidence":"git_receipt#1","tool":"git_commit"}]`,
		}},
		draft,
	)
	testutil.FailErr(t, "guard.FormatCloseoutGroundingReject failed", err)
	for _, want := range []string{
		"[host:coordinator-citation-grounding]",
		"valid citations stay",
		"[cite: c1 => read#5:42]",
		"- c2: `missing.go:1` (not_observed)",
		"Repair citations using existing observations.",
		"Attempt 1/3",
		draft,
		"Retained answer:",
		"missing.go:1",
		"leg-a:list#1",
		"src/coropa/main.go",
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
	// D8: the kick has no JSON fallback left to teach.
	for _, retired := range []string{"citations fence", "cited_evidence", "cited_urls", "artifact_ids"} {
		if strings.Contains(nudge, retired) {
			t.Errorf("nudge teaches retired %q:\n%s", retired, nudge)
		}
	}
	if strings.Count(nudge, "[host:coordinator-citation-grounding]") != 1 {
		t.Fatalf("expected single host marker, got %q", nudge)
	}
}

func markdownDraft(markdown string) guidance.CloseoutDraft {
	return guidance.ParseCloseoutDraft(markdown, citationmd.Options{})
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
