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
		{"stylesheet", "body { color: red; }", StatusUnsupported},
		{"custom property", ":root { --accent: #aabbcc; } .card { color: var(--accent); }", StatusUnsupported},
		{"media", "@media (width >= 600px) { .card { display: grid; } }", StatusUnsupported},
		{"missing brace", "body { color: red;", StatusUnsupported},
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
	after := []byte("<css>body { color: red; }</css>")
	change := Evaluate(t.Context(), "style.css", before, false, after)
	if change.Transition != TransitionNewFileBroken {
		t.Fatalf("change = %+v", change)
	}
}

func TestCSSMarkupBoundary(t *testing.T) {
	for _, source := range []string{"<!-- body { color: red; } -->", "/* <css> */ body {}", "body::before { content: '<css>'; }"} {
		report := Analyze(t.Context(), "", "style.css", []byte(source), tsparse.Validation)
		if report.Status != StatusUnsupported {
			t.Fatalf("valid CSS classified as broken: %+v", report)
		}
	}
	report := Analyze(t.Context(), "", "style.css", []byte("\n  <style>body {}</style>"), tsparse.Validation)
	if report.Status != StatusBroken || report.Diagnostics[0].Row != 2 || report.Diagnostics[0].Col != 3 {
		t.Fatalf("wrapper diagnostic = %+v", report)
	}
}
