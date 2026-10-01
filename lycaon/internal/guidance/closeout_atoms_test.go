package guidance_test

import (
	"testing"

	"github.com/lycaon/lycaon/internal/evidence"
	"github.com/lycaon/lycaon/internal/guidance"
	"github.com/lycaon/lycaon/internal/guidance/ledgertest"
	"github.com/lycaon/lycaon/pkg/api"
)

// Text captured in a different observation cannot validate the cited record.
func TestEvaluateCloseoutCitations_excerptFromOtherRecordStillBlocks(t *testing.T) {
	msgs := []api.Message{
		{Role: api.MessageRoleAssistant, ToolCalls: []api.ToolCall{
			{Name: "read", ID: "ra", Args: map[string]any{"path": "src/a.go"}},
		}},
		{Role: api.MessageRoleTool, Content: `{"path":"src/a.go","content":"1|package alpha","offset":1,"end_line":1,"limit":1}`},
		{Role: api.MessageRoleAssistant, ToolCalls: []api.ToolCall{
			{Name: "read", ID: "rb", Args: map[string]any{"path": "src/b.go"}},
		}},
		{Role: api.MessageRoleTool, Content: `{"path":"src/b.go","content":"1|secret token here","offset":1,"end_line":1,"limit":1}`},
	}
	ev := guidance.CloseoutEvidence{Ledger: ledgertest.BuildFromMessages("", msgs)}
	report := guidance.CoordinatorCompletionReport{
		Synthesis: "cited wrong record",
		CitedEvidence: []guidance.CoordinatorCitedEvidence{{
			Path: "src/a.go", Line: 1, Excerpt: "secret token here",
		}},
	}
	eval := guidance.EvaluateCloseoutCitations(evidence.CitationRoots{ProjectDir: t.TempDir()}, "implement_synthesis", report, ev)
	if eval.Code != guidance.SynthCitationUnverifiableCode {
		t.Fatalf("code=%q want %q (excerpt-from-other-record atom must still block)", eval.Code, guidance.SynthCitationUnverifiableCode)
	}
}
