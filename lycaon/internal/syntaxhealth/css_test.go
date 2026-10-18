package syntaxhealth

import (
	"github.com/lycaon/lycaon/internal/tsparse"
	"testing"
)

func TestCSSSourceHealth(t *testing.T) {
	for _, tc := range []struct {
		name, source string
		want         Status
	}{
		{"stylesheet", "body { color: red; }", StatusClean},
		{"custom property", ":root { --accent: #aabbcc; } .card { color: var(--accent); }", StatusClean},
		{"media", "@media (width >= 600px) { .card { display: grid; } }", StatusClean},
		{"missing brace", "body { color: red;", StatusBroken},
		{"html wrapper", "<css>\nbody { color: red; }\n</css>", StatusBroken},
	} {
		t.Run(tc.name, func(t *testing.T) {
			report := Analyze(t.Context(), "", "style.css", []byte(tc.source), tsparse.Validation)
			if report.Status != tc.want || report.Language != "css" {
				t.Fatalf("report = %+v, want %s/css", report, tc.want)
			}
		})
	}
}

func TestCSSMutationMustPreserveSyntax(t *testing.T) {
	before := []byte("body { color: red; }")
	after := []byte("body { color: red;")
	change := Evaluate(t.Context(), "style.css", before, true, after)
	if change.Transition != TransitionBrokeClean {
		t.Fatalf("change = %+v", change)
	}
}
