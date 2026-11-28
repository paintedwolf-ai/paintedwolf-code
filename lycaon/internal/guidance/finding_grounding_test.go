package guidance_test

import (
	"testing"

	"github.com/lycaon/lycaon/internal/evidence"

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
				Body:       []string{"10\tfunc handle() {}"},
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

func TestEvaluateFindingSummary_refShapeFailClosedInvariant(t *testing.T) {
	t.Parallel()

	ev := evidence.AssembleLedger([]evidence.Record{
		{
			Handle:     "read#1",
			Kind:       "read",
			Shape:      evidence.ShapeFileRegion,
			Fidelity:   evidence.FidelityStructured,
			SourceTool: "read",
			Path:       "src/handler.go",
			LineRanges: []evidence.LineRange{{Start: 10, End: 12}},
			Body: []string{
				"10\tfunc handle() int {",
				"11\t\treturn 404",
				"12\t}",
			},
		},
		{
			Handle:     "web#1",
			Kind:       "web",
			Shape:      evidence.ShapeURL,
			Fidelity:   evidence.FidelityStructured,
			SourceTool: "fetch_url",
			URL:        "https://example.com/spec",
			Body:       []string{"spec text"},
		},
	})

	cases := []struct {
		name         string
		ref          string
		wantGrounded bool
	}{
		// Empty ref retains existing behavior (summary prose is advisory only).
		{name: "empty_ref", ref: "", wantGrounded: true},
		// Valid accepted forms.
		{name: "valid_bare_handle", ref: "read#1", wantGrounded: true},
		{name: "valid_handle_line", ref: "read#1:11", wantGrounded: true},
		{name: "valid_handle_line_excerpt", ref: `read#1:11 "return 404"`, wantGrounded: true},
		{name: "valid_path_line", ref: "src/handler.go:11", wantGrounded: true},
		{name: "valid_path_line_excerpt", ref: `src/handler.go:11 "return 404"`, wantGrounded: true},
		{name: "valid_url", ref: "https://example.com/spec", wantGrounded: true},
		// Fabricated / unobserved / unparsable forms MUST fail closed with FINDING_UNGROUNDED.
		{name: "fabricated_handle_line_excerpt", ref: `read#1:11 "panic()"`, wantGrounded: false},
		{name: "unobserved_handle_with_excerpt", ref: `read#99:11 "return 404"`, wantGrounded: false},
		{name: "fabricated_path_line_excerpt", ref: `src/handler.go:11 "panic()"`, wantGrounded: false},
		{name: "unobserved_path_line", ref: "src/missing.go:1", wantGrounded: false},
		{name: "unobserved_url", ref: "https://example.com/unobserved", wantGrounded: false},
		{name: "unobserved_bare_path", ref: "decisions/0001-notes.md", wantGrounded: false},
		{name: "free_text_ref", ref: "not a citation at all", wantGrounded: false},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			eval := guidance.EvaluateFindingSummary("", "Observed handler behavior.", tc.ref, ev)
			if eval.Grounded != tc.wantGrounded {
				t.Fatalf("ref=%q: Grounded=%v (code=%q offenders=%v), want %v",
					tc.ref, eval.Grounded, eval.Code, eval.Offenders, tc.wantGrounded)
			}
			if !tc.wantGrounded && eval.Code != guidance.FindingUngroundedCode {
				t.Fatalf("ref=%q: Code=%q, want %q", tc.ref, eval.Code, guidance.FindingUngroundedCode)
			}
		})
	}
}
