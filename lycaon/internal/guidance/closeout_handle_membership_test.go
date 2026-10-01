package guidance_test

import (
	"testing"

	"github.com/lycaon/lycaon/internal/evidence"
	"github.com/lycaon/lycaon/internal/guidance"
	"github.com/lycaon/lycaon/internal/guidance/ledgertest"
	"github.com/lycaon/lycaon/pkg/api"
)

// handleMembershipLedger observes only read#1.
func handleMembershipLedger(t *testing.T) guidance.CloseoutEvidence {
	t.Helper()
	body := `{"path":"src/a.go","content":"1|package main\n2|func Handler() {}","offset":1,"end_line":2,"limit":2}`
	msgs := []api.Message{
		{Role: api.MessageRoleAssistant, ToolCalls: []api.ToolCall{
			{Name: "read", ID: "c1", Args: map[string]any{"path": "src/a.go", "offset": 1, "limit": 2}},
		}},
		{Role: api.MessageRoleTool, Content: body, ToolResult: &api.ToolResult{
			Content: body, Outcome: api.ToolResultOutcomeCompleted,
		}},
	}
	return guidance.CloseoutEvidence{Ledger: ledgertest.BuildFromMessages("", msgs)}
}

// mutationMembershipLedger observes js/alerts.js through a write receipt.
func mutationMembershipLedger(t *testing.T) guidance.CloseoutEvidence {
	t.Helper()
	msgs := []api.Message{
		{Role: api.MessageRoleAssistant, ToolCalls: []api.ToolCall{
			{Name: "write", ID: "w1", Args: map[string]any{"path": "js/alerts.js", "content": "export function loadAlerts() {}\n"}},
		}},
		{Role: api.MessageRoleTool, Content: "Wrote 32 bytes to js/alerts.js", ToolResult: &api.ToolResult{
			Content: "Wrote 32 bytes to js/alerts.js", Outcome: api.ToolResultOutcomeCompleted,
		}},
	}
	return guidance.CloseoutEvidence{Ledger: ledgertest.BuildFromMessages("", msgs)}
}

func TestCloseoutMutationMembership(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		name     string
		cite     guidance.CoordinatorCitedEvidence
		wantCode string
	}{
		{
			name: "path written this session grounds with a line",
			cite: guidance.CoordinatorCitedEvidence{Path: "js/alerts.js", Line: 1},
		},
		{
			name: "write handle minted this session grounds bare",
			cite: guidance.CoordinatorCitedEvidence{Evidence: "write#1"},
		},
		{
			name:     "path never touched stays unobserved",
			cite:     guidance.CoordinatorCitedEvidence{Path: "js/other.js", Line: 1},
			wantCode: guidance.SynthHandleNotInLegsCode,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			ev := mutationMembershipLedger(t)
			report := guidance.CoordinatorCompletionReport{
				Synthesis:     "A claim about the authored data layer.",
				CitedEvidence: []guidance.CoordinatorCitedEvidence{tc.cite},
			}
			report.Normalize()
			eval := guidance.EvaluateCloseoutCitations(evidence.CitationRoots{}, "implement_synthesis", report, ev)
			if eval.Code != tc.wantCode {
				t.Fatalf("code = %q want %q (offenders %v)", eval.Code, tc.wantCode, eval.Offenders)
			}
		})
	}
}

func TestCloseoutHandleMembership(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		name      string
		cite      guidance.CoordinatorCitedEvidence
		wantCode  string
		wantBound bool
	}{
		{
			name: "handle minted in this session grounds bare",
			cite: guidance.CoordinatorCitedEvidence{Evidence: "read#1"},
		},
		{
			name: "handle minted in this session grounds with a line",
			cite: guidance.CoordinatorCitedEvidence{Evidence: "read#1", Line: 1},
		},
		{
			name:     "invented handle does not ground bare",
			cite:     guidance.CoordinatorCitedEvidence{Evidence: "command#12"},
			wantCode: guidance.SynthHandleNotInLegsCode,
		},
		{
			name:     "invented handle does not ground on a line alone",
			cite:     guidance.CoordinatorCitedEvidence{Evidence: "command#12", Line: 1},
			wantCode: guidance.SynthHandleNotInLegsCode,
		},
		{
			name:     "invented handle with an out-of-range line",
			cite:     guidance.CoordinatorCitedEvidence{Evidence: "command#12", Line: 900},
			wantCode: guidance.SynthHandleNotInLegsCode,
		},
		{
			name:      "wrong handle with a verbatim excerpt still host-binds",
			cite:      guidance.CoordinatorCitedEvidence{Evidence: "command#12", Line: 2, Excerpt: "func Handler() {}"},
			wantBound: true,
		},
		{
			name:     "unobserved path",
			cite:     guidance.CoordinatorCitedEvidence{Path: "src/nope.go", Line: 1},
			wantCode: guidance.SynthHandleNotInLegsCode,
		},
		{
			name:     "handle-shaped path is not an evidence alias",
			cite:     guidance.CoordinatorCitedEvidence{Path: "read#1"},
			wantCode: guidance.SynthHandleNotInLegsCode,
		},
		{
			name:     "bare path is unverifiable, unlike a bare handle",
			cite:     guidance.CoordinatorCitedEvidence{Path: "src/a.go"},
			wantCode: guidance.SynthCitationUnverifiableCode,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			ev := handleMembershipLedger(t)
			report := guidance.CoordinatorCompletionReport{
				Synthesis:     "A claim about the handler.",
				CitedEvidence: []guidance.CoordinatorCitedEvidence{tc.cite},
			}
			report.Normalize()
			eval := guidance.EvaluateCloseoutCitations(evidence.CitationRoots{}, "implement_synthesis", report, ev)
			if eval.Code != tc.wantCode {
				t.Fatalf("code = %q want %q (offenders %v)", eval.Code, tc.wantCode, eval.Offenders)
			}
			if tc.wantCode != "" && len(eval.Offenders) == 0 {
				t.Fatalf("code %q reported no offender to fix", eval.Code)
			}
			if got := eval.BindAdvisoryCount > 0; got != tc.wantBound {
				t.Fatalf("host-bound = %v want %v (tokens %v)", got, tc.wantBound, eval.BindAdvisoryTokens)
			}
		})
	}
}
