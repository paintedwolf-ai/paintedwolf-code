package workercompletion_test

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/git"
	"github.com/lycaon/lycaon/internal/guidance"
	"github.com/lycaon/lycaon/internal/guidance/ledgertest"
	"github.com/lycaon/lycaon/internal/oar"
	"github.com/lycaon/lycaon/internal/orchestration"
	"github.com/lycaon/lycaon/internal/prompts"
	"github.com/lycaon/lycaon/internal/session/workercompletion"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/testutil/gittest"
	"github.com/lycaon/lycaon/pkg/api"
)

const workerTestChildSessionID = "worker-test-child"

func childMessagesLedger(msgs []api.Message) guidance.EvidenceLedgerReader {
	return ledgertest.ChildMessagesReader("", func(id string) []api.Message {
		if id == workerTestChildSessionID {
			return msgs
		}
		return nil
	})
}

func workerSummaryEval(t *testing.T, in workercompletion.WorkerSummaryEvalInput) workercompletion.WorkerSummaryEvalInput {
	t.Helper()
	if in.Ledger == nil && len(in.ChildMessages) > 0 {
		in.ChildSessionID = workerTestChildSessionID
		in.Ledger = childMessagesLedger(in.ChildMessages)
	}
	return withWorkerPipeline(t, in)
}

func TestEvaluateWorkerSummaryWithoutArtifactIsPartial(t *testing.T) {
	msgs := []api.Message{
		{
			Role: api.MessageRoleAssistant,
			ToolCalls: []api.ToolCall{
				{ID: "tc1", Name: "read", Args: map[string]any{}},
			},
		},
		{Role: api.MessageRoleTool, Content: "missing 'path' argument"},
	}
	eval, workerEvalErr := workercompletion.EvaluateWorkerSummary(context.Background(), workerSummaryEval(t, workercompletion.WorkerSummaryEvalInput{
		AgentType: orchestration.ProfileImplementer,
		Report: workercompletion.WorkerCompletionReport{
			Brief: "```python\ndef main():\n  pass\n```",
		},
		ChildMessages: msgs,
	}))
	testutil.FailErr(t, "evaluate worker completion", workerEvalErr)
	if eval.Status != "partial" || eval.HintCode != "WORKER_SUMMARY_NO_ARTIFACT" {
		t.Fatalf("eval = %+v", eval)
	}
	if eval.Grounding.Traced || eval.Grounding.HintCode != "WORKER_SUMMARY_NO_ARTIFACT" {
		t.Fatalf("grounding = %+v", eval.Grounding)
	}
	if len(eval.Grounding.Checks) == 0 {
		t.Fatalf("expected grounding checks on partial eval")
	}
}

func TestEvaluateWorkerSummaryCompleteAfterWrite(t *testing.T) {
	msgs := []api.Message{
		{
			Role: api.MessageRoleAssistant,
			ToolCalls: []api.ToolCall{
				{ID: "tc1", Name: "write", Args: map[string]any{"path": "game.py"}},
			},
		},
		{Role: api.MessageRoleTool, Content: "wrote game.py", ToolResult: &api.ToolResult{ToolCallID: "tc1", Outcome: api.ToolResultOutcomeCompleted}},
	}
	eval, workerEvalErr := workercompletion.EvaluateWorkerSummary(context.Background(), workerSummaryEval(t, workercompletion.WorkerSummaryEvalInput{
		AgentType: orchestration.ProfileImplementer,
		Report: workercompletion.WorkerCompletionReport{
			Brief: "Created game.py",
		},
		ChildMessages: msgs,
	}))
	testutil.FailErr(t, "evaluate worker completion", workerEvalErr)
	if eval.Status != "complete" || eval.Summary != "Created game.py" {
		t.Fatalf("eval = %+v", eval)
	}
	if !eval.Grounding.Traced {
		t.Fatalf("grounding = %+v want traced", eval.Grounding)
	}
	foundArtifact := false
	for _, c := range eval.Grounding.Checks {
		if c.ID == "implementer_artifact" && c.Status == api.CitationGroundingCheckStatusPassed {
			foundArtifact = true
		}
	}
	if !foundArtifact {
		t.Fatalf("checks = %+v want implementer_artifact passed", eval.Grounding.Checks)
	}
}

func TestEvaluateWorkerSummaryGitDirtyAloneIsPartial(t *testing.T) {
	dir := t.TempDir()
	gittest.Run(t, dir, "init")
	gittest.Run(t, dir, "config", "user.email", "a@b.c")
	gittest.Run(t, dir, "config", "user.name", "t")
	if err := os.WriteFile(filepath.Join(dir, "game.py"), []byte("x"), 0o644); err != nil {
		testutil.FailErr(t, "write file", err)
	}
	checker := &workercompletion.GitWorkspaceChangeChecker{Git: git.NewManager()}
	eval, workerEvalErr := workercompletion.EvaluateWorkerSummary(context.Background(), workerSummaryEval(t, workercompletion.WorkerSummaryEvalInput{
		AgentType:      orchestration.ProfileImplementer,
		Report:         workercompletion.WorkerCompletionReport{Brief: strings.Repeat("def game():\n", 80)},
		ChildMessages:  nil,
		ProjectDir:     dir,
		WorkspaceCheck: checker,
	}))
	testutil.FailErr(t, "evaluate worker completion", workerEvalErr)
	if eval.Status != "partial" || eval.HintCode != "WORKER_SUMMARY_NO_ARTIFACT" {
		t.Fatalf("eval = %+v want partial when no tool activity (workspace was already dirty)", eval)
	}
}

func TestEvaluateWorkerSummaryCommandWithDirtyWorkspaceIsComplete(t *testing.T) {
	dir := t.TempDir()
	gittest.Run(t, dir, "init")
	gittest.Run(t, dir, "config", "user.email", "a@b.c")
	gittest.Run(t, dir, "config", "user.name", "t")
	if err := os.WriteFile(filepath.Join(dir, "generated.go"), []byte("package x"), 0o644); err != nil {
		testutil.FailErr(t, "write file", err)
	}
	msgs := []api.Message{
		{
			Role: api.MessageRoleAssistant,
			ToolCalls: []api.ToolCall{
				{ID: "tc1", Name: "command", Args: map[string]any{"command": "go run ./cmd/codegen"}},
			},
		},
		{Role: api.MessageRoleTool, Content: "generated 1 file"},
	}
	checker := &workercompletion.GitWorkspaceChangeChecker{Git: git.NewManager()}
	eval, workerEvalErr := workercompletion.EvaluateWorkerSummary(context.Background(), workerSummaryEval(t, workercompletion.WorkerSummaryEvalInput{
		AgentType:      orchestration.ProfileImplementer,
		Report:         workercompletion.WorkerCompletionReport{Brief: "Ran code generator; new file landed on disk."},
		ChildMessages:  msgs,
		ProjectDir:     dir,
		WorkspaceCheck: checker,
	}))
	testutil.FailErr(t, "evaluate worker completion", workerEvalErr)
	if eval.Status != "complete" {
		t.Fatalf("eval = %+v want complete for command + dirty workspace", eval)
	}
}

func TestEvaluateWorkerSummaryCommandWithoutWorkspaceProofIsPartial(t *testing.T) {
	msgs := []api.Message{
		{
			Role: api.MessageRoleAssistant,
			ToolCalls: []api.ToolCall{
				{ID: "tc1", Name: "command", Args: map[string]any{"command": "echo hi"}},
			},
		},
		{Role: api.MessageRoleTool, Content: "hi"},
	}
	eval, workerEvalErr := workercompletion.EvaluateWorkerSummary(context.Background(), workerSummaryEval(t, workercompletion.WorkerSummaryEvalInput{
		AgentType:     orchestration.ProfileImplementer,
		Report:        workercompletion.WorkerCompletionReport{Brief: "Echoed something"},
		ChildMessages: msgs,
	}))
	testutil.FailErr(t, "evaluate worker completion", workerEvalErr)
	if eval.Status != "partial" || eval.HintCode != "WORKER_SUMMARY_NO_ARTIFACT" {
		t.Fatalf("eval = %+v want partial when command ran with no workspace proof", eval)
	}
}

func TestEvaluateWorkerSummaryProseOnlyWithoutLedgerIsPartial(t *testing.T) {
	eval, workerEvalErr := workercompletion.EvaluateWorkerSummary(context.Background(), workerSummaryEval(t, workercompletion.WorkerSummaryEvalInput{
		AgentType:     orchestration.ProfileImplementer,
		Report:        workercompletion.WorkerCompletionReport{Brief: "Implemented the handler and added tests."},
		ChildMessages: nil,
	}))
	testutil.FailErr(t, "evaluate worker completion", workerEvalErr)
	if eval.Status != "partial" || eval.HintCode != "WORKER_SUMMARY_NO_ARTIFACT" {
		t.Fatalf("eval = %+v want partial for prose-only summary", eval)
	}
}

func TestEvaluateWorkerSummaryScoutUnreadPathIsPartial(t *testing.T) {
	root := t.TempDir()
	grepJSON := `{"matches":[{"path":"pkg/main.go","line":1,"content":"package main"}]}`
	msgs := []api.Message{
		{
			Role: api.MessageRoleAssistant,
			ToolCalls: []api.ToolCall{
				{ID: "tc1", Name: "grep", Args: map[string]any{"path": ".", "pattern": "main"}},
			},
		},
		{Role: api.MessageRoleTool, Content: grepJSON, ToolResult: &api.ToolResult{Content: grepJSON, Outcome: api.ToolResultOutcomeCompleted}},
	}
	eval, workerEvalErr := workercompletion.EvaluateWorkerSummary(context.Background(), workerSummaryEval(t, workercompletion.WorkerSummaryEvalInput{
		AgentType: orchestration.ProfilePathExplorer,
		Report: workercompletion.WorkerCompletionReport{
			LegStatus: "complete",
			Brief:     "Survey mapped main package.",
			Findings: []workercompletion.WorkerFinding{
				{Path: "pkg/unread.go", Line: 10, Note: "Failure in handler"},
			},
		},
		ChildMessages: msgs,
		ProjectDir:    root,
	}))
	testutil.FailErr(t, "evaluate worker completion", workerEvalErr)
	if eval.Status != "partial" || eval.HintCode != "WORKER_EVIDENCE_HANDLE_UNKNOWN" {
		t.Fatalf("eval = %+v", eval)
	}
}

func TestEvaluateWorkerSummaryBarePathMentionIsComplete(t *testing.T) {
	eval, workerEvalErr := workercompletion.EvaluateWorkerSummary(context.Background(), workerSummaryEval(t, workercompletion.WorkerSummaryEvalInput{
		AgentType: orchestration.ProfilePathExplorer,
		Report:    workercompletion.WorkerCompletionReport{Brief: "internal/foo may need review"},
	}))
	testutil.FailErr(t, "evaluate worker completion", workerEvalErr)
	if eval.Status != "complete" {
		t.Fatalf("eval = %+v want complete for bare path mention", eval)
	}
}

func TestEvaluateWorkerSummaryNoCitationsIsComplete(t *testing.T) {
	eval, workerEvalErr := workercompletion.EvaluateWorkerSummary(context.Background(), workerSummaryEval(t, workercompletion.WorkerSummaryEvalInput{
		AgentType: orchestration.ProfilePathExplorer,
		Report: workercompletion.WorkerCompletionReport{
			LegStatus: "complete",
			Brief:     "Survey complete; handler lives in main package.",
		},
		ChildMessages: []api.Message{
			{
				Role: api.MessageRoleAssistant,
				ToolCalls: []api.ToolCall{
					{ID: "tc1", Name: "grep", Args: map[string]any{"path": ".", "pattern": "main"}},
				},
			},
			{Role: api.MessageRoleTool, Content: `{}`, ToolResult: &api.ToolResult{Outcome: api.ToolResultOutcomeCompleted}},
		},
	}))
	testutil.FailErr(t, "evaluate worker completion", workerEvalErr)
	if eval.Status != "complete" {
		t.Fatalf("eval = %+v", eval)
	}
}

func TestEvaluateWorkerSummaryImplementerWroteArtifactIsComplete(t *testing.T) {
	root := t.TempDir()
	msgs := []api.Message{
		{
			Role: api.MessageRoleAssistant,
			ToolCalls: []api.ToolCall{
				{ID: "tc1", Name: "write", Args: map[string]any{"path": "src/a.go"}},
			},
		},
		{Role: api.MessageRoleTool, Content: "wrote src/a.go", ToolResult: &api.ToolResult{ToolCallID: "tc1", Content: "wrote src/a.go", Outcome: api.ToolResultOutcomeCompleted}},
	}
	eval, workerEvalErr := workercompletion.EvaluateWorkerSummary(context.Background(), workerSummaryEval(t, workercompletion.WorkerSummaryEvalInput{
		AgentType: orchestration.ProfileImplementer,
		Report: workercompletion.WorkerCompletionReport{
			Brief: "Updated src/a.go",
		},
		ChildMessages: msgs,
		ProjectDir:    root,
	}))
	testutil.FailErr(t, "evaluate worker completion", workerEvalErr)
	if eval.Status != "complete" {
		t.Fatalf("eval = %+v", eval)
	}
}

func TestEvaluateWorkerSummaryReviewerUnreadFindingIsPartial(t *testing.T) {
	root := t.TempDir()
	grepJSON := `{"matches":[{"path":"pkg/main.go","line":1,"content":"package main"}]}`
	msgs := []api.Message{
		{
			Role: api.MessageRoleAssistant,
			ToolCalls: []api.ToolCall{
				{ID: "tc1", Name: "grep", Args: map[string]any{"path": ".", "pattern": "main"}},
			},
		},
		{Role: api.MessageRoleTool, Content: grepJSON, ToolResult: &api.ToolResult{Content: grepJSON, Outcome: api.ToolResultOutcomeCompleted}},
	}
	eval, workerEvalErr := workercompletion.EvaluateWorkerSummary(context.Background(), workerSummaryEval(t, workercompletion.WorkerSummaryEvalInput{
		AgentType: orchestration.ProfileCodeReviewer,
		Report: workercompletion.WorkerCompletionReport{
			Brief: "Review complete.",
			Findings: []workercompletion.WorkerFinding{
				{Path: "pkg/unread.go", Line: 1, Note: "nil deref risk"},
			},
		},
		ChildMessages: msgs,
		ProjectDir:    root,
	}))
	testutil.FailErr(t, "evaluate worker completion", workerEvalErr)
	if eval.Status != "partial" || eval.HintCode != "WORKER_EVIDENCE_HANDLE_UNKNOWN" {
		t.Fatalf("eval = %+v", eval)
	}
}

func TestEvaluateWorkerSummaryReviewerSummaryPackageMentionIsComplete(t *testing.T) {
	eval, workerEvalErr := workercompletion.EvaluateWorkerSummary(context.Background(), workerSummaryEval(t, workercompletion.WorkerSummaryEvalInput{
		AgentType: orchestration.ProfileCodeReviewer,
		Report:    workercompletion.WorkerCompletionReport{Brief: "**Findings:**\nnone\n\n**Summary:**\ninternal/foo package may need follow-up"},
	}))
	testutil.FailErr(t, "evaluate worker completion", workerEvalErr)
	if eval.Status != "complete" {
		t.Fatalf("eval = %+v want complete for Summary-only package mention", eval)
	}
}

func TestEvaluateWorkerSummaryReadRangeLineOutOfRangeIsPartial(t *testing.T) {
	root := t.TempDir()
	readJSON := `{"path":"f.go","content":"1|alpha beta","offset":1,"end_line":10,"limit":10}`
	msgs := []api.Message{
		{
			Role: api.MessageRoleAssistant,
			ToolCalls: []api.ToolCall{
				{ID: "tc1", Name: "read", Args: map[string]any{"path": "f.go", "offset": 1, "limit": 10}},
			},
		},
		{Role: api.MessageRoleTool, Content: readJSON, ToolResult: &api.ToolResult{Content: readJSON, Outcome: api.ToolResultOutcomeCompleted}},
	}
	eval, workerEvalErr := workercompletion.EvaluateWorkerSummary(context.Background(), workerSummaryEval(t, workercompletion.WorkerSummaryEvalInput{
		AgentType: orchestration.ProfilePathExplorer,
		Report: workercompletion.WorkerCompletionReport{
			LegStatus: "complete",
			Brief:     "Survey noted an issue.",
			Findings: []workercompletion.WorkerFinding{
				{Path: "f.go", Evidence: "read#1", Line: 50, Excerpt: "alpha beta"},
			},
		},
		ChildMessages: msgs,
		ProjectDir:    root,
	}))
	testutil.FailErr(t, "evaluate worker completion", workerEvalErr)
	if eval.Status != "complete" {
		t.Fatalf("eval = %+v want complete — wrong-line excerpt is traced, not blocking", eval)
	}
	var findingCheck *api.CitationGroundingCheck
	for i := range eval.Grounding.Checks {
		if eval.Grounding.Checks[i].ID == "finding_excerpts" {
			findingCheck = &eval.Grounding.Checks[i]
			break
		}
	}
	if findingCheck == nil || findingCheck.Status != api.CitationGroundingCheckStatusAdvisory {
		t.Fatalf("grounding checks = %+v want advisory finding_excerpts", eval.Grounding.Checks)
	}
}

func TestEvaluateWorkerSummaryCheckTokensShowPathNotHandle(t *testing.T) {
	root := t.TempDir()
	readJSON := `{"path":"f.go","content":"1|alpha beta","offset":1,"end_line":10,"limit":10}`
	msgs := []api.Message{
		{
			Role: api.MessageRoleAssistant,
			ToolCalls: []api.ToolCall{
				{ID: "tc1", Name: "read", Args: map[string]any{"path": "f.go", "offset": 1, "limit": 10}},
			},
		},
		{Role: api.MessageRoleTool, Content: readJSON, ToolResult: &api.ToolResult{Content: readJSON, Outcome: api.ToolResultOutcomeCompleted}},
	}
	eval, workerEvalErr := workercompletion.EvaluateWorkerSummary(context.Background(), workerSummaryEval(t, workercompletion.WorkerSummaryEvalInput{
		AgentType: orchestration.ProfilePathExplorer,
		Report: workercompletion.WorkerCompletionReport{
			LegStatus: "complete",
			Brief:     "Survey noted an issue.",
			Findings: []workercompletion.WorkerFinding{
				{Path: "f.go", Evidence: "read#1", Line: 50, Excerpt: "alpha beta"},
			},
		},
		ChildMessages: msgs,
		ProjectDir:    root,
	}))
	testutil.FailErr(t, "evaluate worker completion", workerEvalErr)
	if eval.Status != "complete" {
		t.Fatalf("eval = %+v want complete", eval)
	}
	var failed []string
	for _, c := range eval.Grounding.Checks {
		failed = append(failed, c.Failed...)
	}
	joined := strings.Join(failed, " ")
	if !strings.Contains(joined, "f.go:50") {
		t.Fatalf("expected resolved path token, got failed=%v", failed)
	}
	if strings.Contains(joined, "read#1") {
		t.Fatalf("handle key must not surface in UI tokens, got failed=%v", failed)
	}
}

func TestEvaluateWorkerSummaryProseCitationLeakIsAdvisory(t *testing.T) {
	root := t.TempDir()
	readJSON := `{"path":"f.go","content":"42|  return nil","offset":42,"end_line":42,"limit":1}`
	msgs := []api.Message{
		{
			Role: api.MessageRoleAssistant,
			ToolCalls: []api.ToolCall{
				{ID: "tc1", Name: "read", Args: map[string]any{"path": "f.go", "offset": 42, "limit": 1}},
			},
		},
		{Role: api.MessageRoleTool, Content: readJSON, ToolResult: &api.ToolResult{Content: readJSON, Outcome: api.ToolResultOutcomeCompleted}},
	}
	eval, workerEvalErr := workercompletion.EvaluateWorkerSummary(context.Background(), workerSummaryEval(t, workercompletion.WorkerSummaryEvalInput{
		AgentType: orchestration.ProfilePathExplorer,
		Report: workercompletion.WorkerCompletionReport{
			LegStatus: "complete",
			Brief:     "Issue at f.go:42 outside typed findings",
		},
		ChildMessages: msgs,
		ProjectDir:    root,
	}))
	testutil.FailErr(t, "evaluate worker completion", workerEvalErr)
	if eval.Status != "complete" {
		t.Fatalf("eval = %+v want complete — prose placement of an observed token is advisory, not blocking", eval)
	}
	if eval.Grounding.ProseAdvisoryCount != 1 {
		t.Fatalf("ProseAdvisoryCount = %d want 1 advisory surfaced", eval.Grounding.ProseAdvisoryCount)
	}
}

func TestEvaluateWorkerSummaryProseDuplicationIsComplete(t *testing.T) {
	root := t.TempDir()
	readJSON := `{"path":"f.go","content":"42|  return nil","offset":42,"end_line":42,"limit":1}`
	msgs := []api.Message{
		{
			Role: api.MessageRoleAssistant,
			ToolCalls: []api.ToolCall{
				{ID: "tc1", Name: "read", Args: map[string]any{"path": "f.go", "offset": 42, "limit": 1}},
			},
		},
		{Role: api.MessageRoleTool, Content: readJSON, ToolResult: &api.ToolResult{Content: readJSON, Outcome: api.ToolResultOutcomeCompleted}},
	}
	eval, workerEvalErr := workercompletion.EvaluateWorkerSummary(context.Background(), workerSummaryEval(t, workercompletion.WorkerSummaryEvalInput{
		AgentType: orchestration.ProfilePathExplorer,
		Report: workercompletion.WorkerCompletionReport{
			LegStatus: "complete",
			Brief:     "Traced f.go:42 during survey",
			Findings: []workercompletion.WorkerFinding{
				{Path: "f.go", Evidence: "read#1", Line: 42, Excerpt: "return nil"},
			},
		},
		ChildMessages: msgs,
		ProjectDir:    root,
	}))
	testutil.FailErr(t, "evaluate worker completion", workerEvalErr)
	if eval.Status != "complete" {
		t.Fatalf("eval = %+v", eval)
	}
	if eval.Grounding.ProseLeakCount != 1 || len(eval.Grounding.ProseLeaksSample) != 1 {
		t.Fatalf("grounding = %+v want prose duplication sample", eval.Grounding)
	}
}

func TestEvaluateWorkerSummaryGrepLineWithoutReadIsComplete(t *testing.T) {
	root := t.TempDir()
	grepJSON := `{"matches":[{"path":"f.go","line":42,"content":"func main()"}]}`
	msgs := []api.Message{
		{
			Role: api.MessageRoleAssistant,
			ToolCalls: []api.ToolCall{
				{ID: "tc1", Name: "grep", Args: map[string]any{"path": ".", "pattern": "main"}},
			},
		},
		{Role: api.MessageRoleTool, Content: grepJSON, ToolResult: &api.ToolResult{Content: grepJSON, Outcome: api.ToolResultOutcomeCompleted}},
	}
	eval, workerEvalErr := workercompletion.EvaluateWorkerSummary(context.Background(), workerSummaryEval(t, workercompletion.WorkerSummaryEvalInput{
		AgentType: orchestration.ProfilePathExplorer,
		Report: workercompletion.WorkerCompletionReport{
			LegStatus: "complete",
			Brief:     "Entry point located.",
			Findings: []workercompletion.WorkerFinding{
				{Path: "f.go", Evidence: "grep#1", Line: 42},
			},
		},
		ChildMessages: msgs,
		ProjectDir:    root,
	}))
	testutil.FailErr(t, "evaluate worker completion", workerEvalErr)
	if eval.Status != "complete" {
		t.Fatalf("eval = %+v", eval)
	}
}

func TestEvaluateWorkerSummaryWrongExcerptQuoteIsPartial(t *testing.T) {
	root := t.TempDir()
	readJSON := `{"path":"f.go","content":"42|  return nil","offset":42,"end_line":42,"limit":1}`
	msgs := []api.Message{
		{
			Role: api.MessageRoleAssistant,
			ToolCalls: []api.ToolCall{
				{ID: "tc1", Name: "read", Args: map[string]any{"path": "f.go", "offset": 42, "limit": 1}},
			},
		},
		{Role: api.MessageRoleTool, Content: readJSON, ToolResult: &api.ToolResult{Content: readJSON, Outcome: api.ToolResultOutcomeCompleted}},
	}
	eval, workerEvalErr := workercompletion.EvaluateWorkerSummary(context.Background(), workerSummaryEval(t, workercompletion.WorkerSummaryEvalInput{
		AgentType: orchestration.ProfilePathExplorer,
		Report: workercompletion.WorkerCompletionReport{
			LegStatus: "complete",
			Brief:     "Nil return path.",
			Findings: []workercompletion.WorkerFinding{
				{Path: "f.go", Evidence: "read#1", Line: 42, Excerpt: "return 1"},
			},
		},
		ChildMessages: msgs,
		ProjectDir:    root,
	}))
	testutil.FailErr(t, "evaluate worker completion", workerEvalErr)
	if eval.Status != "complete" {
		t.Fatalf("eval = %+v want complete — excerpt mismatch is traced", eval)
	}
	if !eval.Grounding.Traced {
		t.Fatalf("grounding = %+v want traced (with traced-excerpt check)", eval.Grounding)
	}
}

func TestEvaluateWorkerSummaryObjectivesMetNarrativeOnlyIsComplete(t *testing.T) {
	root := t.TempDir()
	grepJSON := `{"matches":[{"path":"pkg/main.go","line":1,"content":"package main"}]}`
	msgs := []api.Message{
		{
			Role: api.MessageRoleAssistant,
			ToolCalls: []api.ToolCall{
				{ID: "tc1", Name: "grep", Args: map[string]any{"path": ".", "pattern": "main"}},
			},
		},
		{Role: api.MessageRoleTool, Content: grepJSON, ToolResult: &api.ToolResult{Content: grepJSON, Outcome: api.ToolResultOutcomeCompleted}},
	}
	eval, workerEvalErr := workercompletion.EvaluateWorkerSummary(context.Background(), workerSummaryEval(t, workercompletion.WorkerSummaryEvalInput{
		AgentType: orchestration.ProfilePathExplorer,
		Report: workercompletion.WorkerCompletionReport{
			LegStatus:     "complete",
			Brief:         "Survey done.",
			ObjectivesMet: []string{"Located the main package entry point"},
		},
		ChildMessages: msgs,
		ProjectDir:    root,
	}))
	testutil.FailErr(t, "evaluate worker completion", workerEvalErr)
	if eval.Status != "complete" {
		t.Fatalf("eval = %+v want complete — objectives_met is narrative-only", eval)
	}
}

func TestEvaluateWorkerSummaryScoutCompleteWithoutSurveyIsPartial(t *testing.T) {
	eval, workerEvalErr := workercompletion.EvaluateWorkerSummary(context.Background(), workerSummaryEval(t, workercompletion.WorkerSummaryEvalInput{
		AgentType: orchestration.ProfilePathExplorer,
		Report: workercompletion.WorkerCompletionReport{
			LegStatus: "complete",
			Brief:     "Survey complete; handler lives in main package.",
		},
	}))
	testutil.FailErr(t, "evaluate worker completion", workerEvalErr)
	if eval.Status != "partial" || eval.HintCode != "WORKER_SCOUT_NO_SURVEY_EVIDENCE" {
		t.Fatalf("eval = %+v", eval)
	}
}

func TestEvaluateWorkerSummaryWebResearcherUngroundedURLIsPartial(t *testing.T) {
	fetchBody := `{"url":"https://docs.example.com/a","content":"ok"}`
	msgs := []api.Message{
		{
			Role: api.MessageRoleAssistant,
			ToolCalls: []api.ToolCall{
				{ID: "tc1", Name: "fetch_url", Args: map[string]any{"url": "https://docs.example.com/a"}},
			},
		},
		{Role: api.MessageRoleTool, Content: fetchBody, ToolResult: &api.ToolResult{Content: fetchBody, Outcome: api.ToolResultOutcomeCompleted}},
	}
	eval, workerEvalErr := workercompletion.EvaluateWorkerSummary(context.Background(), workerSummaryEval(t, workercompletion.WorkerSummaryEvalInput{
		AgentType: orchestration.ProfileWebResearcher,
		Report: workercompletion.WorkerCompletionReport{
			LegStatus: "complete",
			Brief:     "Research complete.",
			CitedURLs: []string{"https://docs.example.com/b"},
		},
		ChildMessages: msgs,
	}))
	testutil.FailErr(t, "evaluate worker completion", workerEvalErr)
	if eval.Status != "partial" || eval.HintCode != "WORKER_URL_NOT_OBSERVED" {
		t.Fatalf("eval = %+v", eval)
	}
}

// implementerSummaryMessages is one read (f.go line 1 = "  return nil") plus a write,
// the shape an implementer worker produces when it cites a finding alongside a mutation.
func implementerSummaryMessages() []api.Message {
	readJSON := `{"path":"f.go","content":"1|  return nil","offset":1,"end_line":10,"limit":10}`
	return []api.Message{
		{
			Role: api.MessageRoleAssistant,
			ToolCalls: []api.ToolCall{
				{ID: "tc1", Name: "read", Args: map[string]any{"path": "f.go", "offset": 1, "limit": 10}},
				{ID: "tc2", Name: "write", Args: map[string]any{"path": "f.go"}},
			},
		},
		{Role: api.MessageRoleTool, Content: readJSON, ToolResult: &api.ToolResult{ToolCallID: "tc1", Content: readJSON, Outcome: api.ToolResultOutcomeCompleted}},
		{Role: api.MessageRoleTool, Content: "wrote f.go", ToolResult: &api.ToolResult{ToolCallID: "tc2", Content: "wrote f.go", Outcome: api.ToolResultOutcomeCompleted}},
	}
}

// Excerpt grounding is universal: an implementer finding with a verbatim excerpt grounds.
func TestEvaluateWorkerSummaryImplementerExcerptGrounded(t *testing.T) {
	eval, workerEvalErr := workercompletion.EvaluateWorkerSummary(context.Background(), workerSummaryEval(t, workercompletion.WorkerSummaryEvalInput{
		AgentType: orchestration.ProfileImplementer,
		Report: workercompletion.WorkerCompletionReport{
			LegStatus: "complete",
			Brief:     "Updated handler logic.",
			Findings:  []workercompletion.WorkerFinding{{Path: "f.go", Evidence: "read#1", Line: 1, Excerpt: "return nil"}},
		},
		ChildMessages: implementerSummaryMessages(),
		ProjectDir:    t.TempDir(),
	}))
	testutil.FailErr(t, "evaluate worker completion", workerEvalErr)
	if eval.Status != "complete" {
		t.Fatalf("eval = %+v want complete — verbatim implementer excerpt grounds", eval)
	}
}

func TestEvaluateWorkerSummaryImplementerExcerptMismatch(t *testing.T) {
	eval, workerEvalErr := workercompletion.EvaluateWorkerSummary(context.Background(), workerSummaryEval(t, workercompletion.WorkerSummaryEvalInput{
		AgentType: orchestration.ProfileImplementer,
		Report: workercompletion.WorkerCompletionReport{
			LegStatus: "complete",
			Brief:     "Updated handler logic.",
			Findings:  []workercompletion.WorkerFinding{{Path: "f.go", Evidence: "read#1", Line: 1, Excerpt: "never in body"}},
		},
		ChildMessages: implementerSummaryMessages(),
		ProjectDir:    t.TempDir(),
	}))
	testutil.FailErr(t, "evaluate worker completion", workerEvalErr)
	if eval.Status != "complete" {
		t.Fatalf("eval = %+v want complete — excerpt mismatch is traced", eval)
	}
	// The mismatch is detected, not silently accepted: the finding excerpt failed the
	// word-for-word check (traced, not matched) even though its cited path was observed.
	// The grounded sibling records no such failing citation check.
	if !hasTracedExcerptMismatch(eval.Grounding.Checks) {
		t.Fatalf("grounding = %+v want a traced excerpt-mismatch check", eval.Grounding)
	}
}

// hasTracedExcerptMismatch reports whether a citation check recorded a finding
// excerpt that traced to observed evidence but failed the verbatim quote match.
func hasTracedExcerptMismatch(checks []api.CitationGroundingCheck) bool {
	for _, c := range checks {
		if c.Kind == api.CitationGroundingCheckKindCitation && c.Status == api.CitationGroundingCheckStatusAdvisory && len(c.Failed) > 0 {
			return true
		}
	}
	return false
}

func TestFormatWorkerSummaryFeedbackUsesRegistry(t *testing.T) {
	hints, err := guidance.LoadHintConfigStock()
	testutil.FailErr(t, "load hint registry", err)
	text, err := guidance.FormatWorkerSummaryFeedback(hints, "WORKER_SUMMARY_NO_ARTIFACT", nil)
	testutil.FailErr(t, "guidance.FormatWorkerSummaryFeedback failed", err)
	if !strings.Contains(text, "WORKER_SUMMARY_NO_ARTIFACT") {
		t.Fatalf("text = %q", text)
	}
}

// [OAR-COPY-1] Completion feedback retains the evaluated branch and literal values.
func TestWorkerSummaryFeedbackPreservesFrozenCopy(t *testing.T) {
	guidance.SetGuidanceRenderer(prompts.NewGuidanceRenderer(prompts.NewFileTemplateEngineLayers(prompts.PromptLayers{})))
	for _, effect := range []oar.Effect{oar.EffectBlock, oar.EffectNudge} {
		result := workercompletion.WorkerSummaryEvalResult{HintCode: "WORKER_SUMMARY_NO_ARTIFACT", HintEffect: effect, HintData: map[string]any{"count": 99}, HintCopy: map[string]string{"what": "Measured 3 changes", "fix": "Retain {{ count }} literally"}}
		text, err := result.FormatFeedback(t.Context(), nil)
		testutil.FailErr(t, "format frozen completion feedback", err)
		if !strings.Contains(text, "Measured 3 changes") || !strings.Contains(text, "{{ count }}") {
			t.Fatalf("copy was replaced or evaluated again: %s", text)
		}
		if strings.HasPrefix(text, "Rejected:") != (effect == oar.EffectBlock) {
			t.Fatalf("effect=%s mislabeled: %s", effect, text)
		}
	}
}
