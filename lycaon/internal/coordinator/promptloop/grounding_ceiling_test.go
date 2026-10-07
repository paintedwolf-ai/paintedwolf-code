package promptloop

import (
	"testing"

	"github.com/lycaon/lycaon/internal/coordinator/inject"
	"github.com/lycaon/lycaon/internal/guidance"
	"github.com/lycaon/lycaon/internal/jsonshape"
)

func citationBudget(attempt, limit, remaining int) closeoutRetryBudget {
	return closeoutRetryBudget{attempt: attempt, limit: limit, friction: &guidance.GroundingFriction{Remaining: remaining}}
}

func TestCloseoutRetryBudgetFrictionBoundsCitationRetries(t *testing.T) {
	const limit = 25
	const ceiling = 8
	firstExhausted := 0
	for attempt := 1; attempt <= limit+1; attempt++ {
		if citationBudget(attempt, limit, ceiling-attempt).exhausted() {
			firstExhausted = attempt
			break
		}
	}
	if firstExhausted != ceiling {
		t.Fatalf("first exhausted at attempt %d, want the friction ceiling %d", firstExhausted, ceiling)
	}
}

func TestCloseoutRetryBudgetLimitCapsRetries(t *testing.T) {
	const limit = 3
	for attempt := 1; attempt <= limit; attempt++ {
		if citationBudget(attempt, limit, 100).exhausted() {
			t.Fatalf("exhausted at attempt %d, want %d retries allowed", attempt, limit)
		}
	}
	if !citationBudget(limit+1, limit, 100).exhausted() {
		t.Fatalf("not exhausted at attempt %d, want the cap at %d", limit+1, limit)
	}
}

func TestCloseoutRetryBudgetStatesTheBudgetThatApplies(t *testing.T) {
	cases := []struct {
		name   string
		budget closeoutRetryBudget
		max    int
		done   bool
	}{
		{"friction tighter than the limit", citationBudget(2, 25, 2), 3, false},
		{"friction spent", citationBudget(3, 25, 0), 3, true},
		{"limit tighter than friction", citationBudget(2, 3, 50), 3, false},
		{"document limit without friction", closeoutRetryBudget{attempt: 2, limit: 3}, 3, false},
		{"document limit spent", closeoutRetryBudget{attempt: 4, limit: 3}, 4, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := tc.budget.displayMax(); got != tc.max {
				t.Fatalf("displayMax = %d want %d", got, tc.max)
			}
			if got := tc.budget.exhausted(); got != tc.done {
				t.Fatalf("exhausted = %v want %v", got, tc.done)
			}
		})
	}
}

func TestCloseoutRetryBudgetDocumentRepairCarriesTheFence(t *testing.T) {
	budget := closeoutRetryBudget{attempt: 1, limit: 3, document: `{"findings":[]}`}
	data := budget.hintData(map[string]any{"offender_count": 2}, []jsonshape.Issue{{Name: "headline_note"}})
	if data["retained_document"] != `{"findings":[]}` || data["offender_count"] != 2 {
		t.Fatalf("hint data = %v", data)
	}
	if offending, ok := data["offending_keys"].([]string); !ok || len(offending) != 1 || offending[0] != "headline_note" {
		t.Fatalf("offending_keys = %v", data["offending_keys"])
	}
	if allowed, ok := data["allowed_top_level_keys"].([]string); !ok || len(allowed) == 0 {
		t.Fatalf("allowed_top_level_keys = %v", data["allowed_top_level_keys"])
	}
	if plain := (closeoutRetryBudget{}).hintData(map[string]any{"k": "v"}); len(plain) != 1 {
		t.Fatalf("citation hint data gained fields: %v", plain)
	}
}

func TestCloseoutRetryBudget_WorkflowConfiguredLimit(t *testing.T) {
	loop := &PromptLoop{}
	stDefault := &promptLoopTurnState{
		coordinatorFrame: inject.CoordinatorTurnFrame{
			Runtime: inject.WorkflowRuntimeSnapshot{
				ReportDocumentEnabled: true,
			},
		},
	}
	budget := loop.closeoutRetryBudget(t.Context(), "sess-1", stDefault, guidance.ReportFenceUnreadableCode, "{}")
	if budget.limit != 3 {
		t.Fatalf("expected default limit 3, got %d", budget.limit)
	}

	stConfigured := &promptLoopTurnState{
		coordinatorFrame: inject.CoordinatorTurnFrame{
			Runtime: inject.WorkflowRuntimeSnapshot{
				ReportDocumentEnabled: true,
				CloseoutRetries:       5,
			},
		},
	}
	budget5 := loop.closeoutRetryBudget(t.Context(), "sess-1", stConfigured, guidance.ReportFenceUnreadableCode, "{}")
	if budget5.limit != 5 {
		t.Fatalf("expected configured limit 5, got %d", budget5.limit)
	}
}
