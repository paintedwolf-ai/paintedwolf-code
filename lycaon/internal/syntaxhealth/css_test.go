package syntaxhealth

import (
	"testing"

	"github.com/lycaon/lycaon/internal/tsparse"
)

func TestCSSSourceHealth(t *testing.T) {
	for _, tc := range []struct {
		name, source string
		want         Status
	}{
		{"stylesheet", "body { color: red; }", StatusClean},
		{"custom property", ":root { --accent: #aabbcc; } .card { color: var(--accent, #112233); }", StatusClean},
		{"media range gte", "@media (width >= 600px) { .card { display: grid; } }", StatusClean},
		{"media range lte", "@media (width <= 48rem) { .card { display: block; } }", StatusClean},
		{"media range reversed", "@media (600px <= width) { .card { display: flex; } }", StatusClean},
		{"media range bounded", "@media (100px <= width <= 600px) { .card { display: grid; } }", StatusClean},
		{"media range ratio", "@media (aspect-ratio >= 16 / 9) { .video { width: 100%; } }", StatusClean},
		{"media combined", "@media screen and (width >= 48rem) and (height < 90rem) { .grid { gap: 1rem; } }", StatusClean},
		{"container range", "@container (width >= 400px) { .card { padding: 1rem; } }", StatusClean},
		{"named container range", "@container sidebar (inline-size > 30em) { .nav { display: flex; } }", StatusClean},
		{"layer dotted", "@layer framework.base, framework.utilities;\n@layer framework.base { body { margin: 0; } }", StatusClean},
		{"css nesting", ".card { color: red; &:hover { color: blue; } .title { font-weight: bold; } }", StatusClean},
		{"modern functions", ".box { width: clamp(200px, 50vw, 800px); color: color-mix(in oklch, red 40%, blue); }", StatusClean},
		{"missing closing brace", "body { color: red;", StatusBroken},
		{"extra closing brace", "body { color: red; } }", StatusBroken},
		{"bare top-level declaration", "  background: #f0f0f0;\n}\n", StatusBroken},
		{"declaration missing colon", "body { color red; }", StatusBroken},
		{"declaration missing value", "body { color: ; }", StatusBroken},
		{"declaration truncated", "body { color }", StatusBroken},
		{"missing semicolon between declarations", "body {\n  color: red\n  margin: 0;\n}", StatusBroken},
		{"unclosed string", "body { content: \"unclosed; }\n", StatusBroken},
		{"unescaped string newline", "body { content: \"line1\nline2\"; }", StatusBroken},
		{"unclosed comment", "body { color: red; } /* unclosed", StatusBroken},
		{"malformed media range missing rhs", "@media (width >= ) { body { color: red; } }", StatusBroken},
		{"malformed media range missing lhs", "@media ( >= 600px) { body { color: red; } }", StatusBroken},
		{"malformed media range mixed directions", "@media (100px < width > 600px) { body { color: red; } }", StatusBroken},
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
	before := []byte("body {\n  margin: 0;\n  background: #f0f0f0;\n}\n")
	if change := Evaluate(t.Context(), "style.css", nil, false, []byte("<css>body { color: red; }</css>")); change.Transition != TransitionNewFileBroken {
		t.Fatalf("new file wrapper change = %+v, want %s", change, TransitionNewFileBroken)
	}
	for _, tc := range []struct {
		name  string
		after string
	}{
		{"html wrapper", "<css>body { color: red; }</css>"},
		{"stripped rule header", "  margin: 0;\n  background: #f0f0f0;\n}\n"},
		{"missing closing brace", "body {\n  margin: 0;\n  background: #f0f0f0;\n"},
		{"truncated declaration", "body {\n  margin: ;\n}\n"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			change := Evaluate(t.Context(), "style.css", before, true, []byte(tc.after))
			if change.Transition != TransitionBrokeClean {
				t.Fatalf("change = %+v, want %s", change, TransitionBrokeClean)
			}
			if FeedbackFor(change) == "" {
				t.Fatalf("expected syntax feedback for broken CSS mutation %q", tc.name)
			}
		})
	}
}

func TestCSSMarkupBoundary(t *testing.T) {
	for _, source := range []string{"<!-- body { color: red; } -->", "/* <css> */ body {}", "body::before { content: '<css>'; }"} {
		report := Analyze(t.Context(), "", "style.css", []byte(source), tsparse.Validation)
		if report.Status != StatusClean {
			t.Fatalf("valid CSS classified as %s: %+v", report.Status, report)
		}
	}
	report := Analyze(t.Context(), "", "style.css", []byte("\n  <style>body {}</style>"), tsparse.Validation)
	if report.Status != StatusBroken || report.Diagnostics[0].Row != 2 || report.Diagnostics[0].Col != 3 {
		t.Fatalf("wrapper diagnostic = %+v", report)
	}
}
