package guidance_test

import (
	"testing"

	"github.com/lycaon/lycaon/internal/evidence"
	"github.com/lycaon/lycaon/internal/guidance"
	"github.com/lycaon/lycaon/internal/guidance/ledgertest"
	"github.com/lycaon/lycaon/pkg/api"
)

func TestEvaluateCloseoutCitations_observedPathWrongRecordInvestUnverifiable(t *testing.T) {
	t.Parallel()
	msgs := []api.Message{
		{Role: api.MessageRoleAssistant, ToolCalls: []api.ToolCall{
			{Name: "read", ID: "ra", Args: map[string]any{"path": "src/a.go"}},
		}},
		{Role: api.MessageRoleTool, ToolResult: &api.ToolResult{
			Outcome: api.ToolResultOutcomeCompleted,
			Content: `{"path":"src/a.go","content":"1|package alpha","offset":1,"end_line":1,"limit":1}`,
		}},
		{Role: api.MessageRoleAssistant, ToolCalls: []api.ToolCall{
			{Name: "read", ID: "rb", Args: map[string]any{"path": "src/b.go"}},
		}},
		{Role: api.MessageRoleTool, ToolResult: &api.ToolResult{
			Outcome: api.ToolResultOutcomeCompleted,
			Content: `{"path":"src/b.go","content":"1|secret token here","offset":1,"end_line":1,"limit":1}`,
		}},
	}
	ev := guidance.CloseoutEvidence{Ledger: ledgertest.BuildFromMessages("", msgs)}
	report := guidance.CoordinatorCompletionReport{
		Synthesis: "Summary.",
		CitedEvidence: []guidance.CoordinatorCitedEvidence{{
			Path: "src/a.go", Line: 1, Excerpt: "secret token here",
		}},
	}
	eval := guidance.EvaluateCloseoutCitations(evidence.CitationRoots{ProjectDir: t.TempDir()}, "implement_investigate", report, ev)
	if eval.Code != guidance.InvestCitationUnverifiableCode {
		t.Fatalf("code = %q want %s", eval.Code, guidance.InvestCitationUnverifiableCode)
	}
}

func TestEvaluateCloseoutCitations_summarizeHandle(t *testing.T) {
	t.Parallel()
	content := `{"task":"Overview of packages/opencode","pack":{"identity":[{"path":"packages/opencode","kind":"dir_map"}]}}`
	msgs := []api.Message{
		{Role: api.MessageRoleAssistant, ToolCalls: []api.ToolCall{
			{Name: "summarize", ID: "c1", Args: map[string]any{"path": "packages/opencode"}},
		}},
		{Role: api.MessageRoleTool, ToolResult: &api.ToolResult{
			Outcome: api.ToolResultOutcomeCompleted,
			Content: content,
		}},
	}
	ev := guidance.CloseoutEvidence{Ledger: ledgertest.BuildFromMessages("", msgs)}
	report := guidance.CoordinatorCompletionReport{
		Synthesis: "Summary.",
		CitedEvidence: []guidance.CoordinatorCitedEvidence{{
			Evidence: "summarize#2", Line: 1, Excerpt: "Overview of packages/opencode",
		}},
	}
	eval := guidance.EvaluateCloseoutCitations(evidence.CitationRoots{}, "implement_investigate", report, ev)
	if eval.Code != "" {
		t.Fatalf("summarize handle should ground: code=%q offenders=%v", eval.Code, eval.Offenders)
	}
}

func TestNormalizeCoordinatorCitedEvidence_handle(t *testing.T) {
	t.Parallel()
	in := guidance.CoordinatorCitedEvidence{Evidence: "summarize#4", Line: 1, Excerpt: "task line"}
	ev := guidance.CloseoutEvidence{Ledger: evidence.InitLedger()}
	ev.Handles["summarize#4"] = evidence.Record{Handle: "summarize#4", Kind: "summarize", Shape: evidence.ShapeOpaque, Body: []string{"task line"}}
	report := guidance.CoordinatorCompletionReport{
		Synthesis:     "ok",
		CitedEvidence: []guidance.CoordinatorCitedEvidence{in},
	}
	result := guidance.EvaluateCloseoutCitations(evidence.CitationRoots{}, "implement_investigate", report, ev)
	if result.Code != "" {
		t.Fatalf("code=%q want grounded handle citation", result.Code)
	}
}
