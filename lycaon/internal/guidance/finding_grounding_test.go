package guidance_test

import (
	"github.com/lycaon/lycaon/internal/evidence"
	"testing"

	"github.com/lycaon/lycaon/internal/guidance"
)

func TestEvaluateFindingSummary_groundedTypedCitation(t *testing.T) {
	ev := evidence.Ledger{
		Handles: map[string]evidence.Record{
			"read#1": {
				Handle:     "read#1",
				Kind:       "read",
				Shape:      evidence.ShapeFileRegion,
				Path:       "src/handler.go",
				Body:       []string{"func handle() {}"},
				LineRanges: []evidence.LineRange{{Start: 10, End: 10}},
			},
		},
		ByPath: map[string][]string{"src/handler.go": {"read#1"}},
	}
	eval := guidance.EvaluateFindingSummary("", "Handler returns 404.", "src/handler.go:10", ev)
	if !eval.Grounded {
		t.Fatalf("eval = %+v want grounded", eval)
	}
}

func TestEvaluateFindingSummary_proseCitationIsAdvisory(t *testing.T) {
	ev := evidence.Ledger{
		Handles: map[string]evidence.Record{
			"read#1": {
				Handle: "read#1",
				Kind:   "read",
				Shape:  evidence.ShapeFileRegion,
				Path:   "src/handler.go",
				Body:   []string{"func handle() {}"},
			},
		},
		ByPath: map[string][]string{"src/handler.go": {"read#1"}},
	}
	eval := guidance.EvaluateFindingSummary("", "Observed src/handler.go:10 in read output.", "", ev)
	if !eval.Grounded {
		t.Fatalf("eval = %+v want grounded (prose leak is advisory)", eval)
	}
	if eval.ProseAdvisoryCount != 1 || len(eval.ProseAdvisoryTokens) != 1 {
		t.Fatalf("prose advisories = %d %v want one advisory", eval.ProseAdvisoryCount, eval.ProseAdvisoryTokens)
	}
}

func TestEvaluateFindingSummary_ungroundedTypedHandle(t *testing.T) {
	eval := guidance.EvaluateFindingSummary("", "see grep result", "grep#99", evidence.Ledger{})
	if eval.Grounded || eval.Code != guidance.FindingUngroundedCode {
		t.Fatalf("eval = %+v want ungrounded", eval)
	}
}

func TestEvaluateFindingSummary_ignoresPageRef(t *testing.T) {
	ev := evidence.Ledger{}
	eval := guidance.EvaluateFindingSummary("", "Finished decision notes.", "decisions/0001-notes.md", ev)
	if !eval.Grounded {
		t.Fatalf("page ref should not require evidence grounding: %+v", eval)
	}
}
