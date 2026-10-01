package syntaxhealth_test

import (
	"testing"

	"github.com/lycaon/lycaon/internal/syntaxhealth"
	"github.com/lycaon/lycaon/internal/tsparse"
)

func TestAnalysisDiagnosticsCleanGo(t *testing.T) {
	src := []byte("package main\n\nfunc main() {\n\tprintln(\"ok\")\n}\n")
	parsed := syntaxhealth.Analyze(t.Context(), "go", "", src, tsparse.Analysis)
	diags := parsed.Diagnostics
	if parsed.Status != syntaxhealth.StatusClean && parsed.Status != syntaxhealth.StatusBroken {
		t.Fatal("parsed = false for supported grammar")
	}
	if len(diags) != 0 {
		t.Fatalf("diags = %+v want none", diags)
	}
}

func TestAnalysisDiagnosticsBrokenGo(t *testing.T) {
	src := []byte("package main\n\nfunc main() {\n\tprintln(\"ok\")\n\n")
	parsed := syntaxhealth.Analyze(t.Context(), "", "main.go", src, tsparse.Analysis)
	diags := parsed.Diagnostics
	if parsed.Status != syntaxhealth.StatusClean && parsed.Status != syntaxhealth.StatusBroken {
		t.Fatal("parsed = false for supported grammar")
	}
	if len(diags) == 0 {
		t.Fatal("expected diagnostics for unterminated function")
	}
	d := diags[0]
	if d.Row < 1 || d.Col < 1 {
		t.Fatalf("diag location not 1-based: %+v", d)
	}
}

func TestAnalysisDiagnosticsBrokenJavaScript(t *testing.T) {
	src := []byte("function update() {\n  scene.add(edge);\n\nfunction next() {}\n")
	parsed := syntaxhealth.Analyze(t.Context(), "", "app.js", src, tsparse.Analysis)
	diags := parsed.Diagnostics
	if parsed.Status != syntaxhealth.StatusClean && parsed.Status != syntaxhealth.StatusBroken {
		t.Fatal("parsed = false for supported grammar")
	}
	if len(diags) == 0 {
		t.Fatal("expected diagnostics for missing closing brace")
	}
	if diags[0].Snippet == "" {
		t.Fatalf("diag snippet empty: %+v", diags[0])
	}
}

func TestAnalysisDiagnosticsKindPopulated(t *testing.T) {
	// Fault kinds distinguish unexpected tokens from missing syntax.
	src := []byte("package main\n\nfunc main() {\n\tprintln(\"ok\")\n\n")
	parsed := syntaxhealth.Analyze(t.Context(), "", "main.go", src, tsparse.Analysis)
	diags := parsed.Diagnostics
	if parsed.Status != syntaxhealth.StatusClean && parsed.Status != syntaxhealth.StatusBroken {
		t.Fatal("parsed = false for supported grammar")
	}
	if len(diags) == 0 {
		t.Fatal("expected diagnostics")
	}
	for _, d := range diags {
		if d.Kind != syntaxhealth.DiagnosticError && d.Kind != syntaxhealth.DiagnosticMissing {
			t.Fatalf("diag kind = %q want error|missing: %+v", d.Kind, d)
		}
	}
}

func TestAnalysisDiagnosticsUnknownGrammar(t *testing.T) {
	parsed := syntaxhealth.Analyze(t.Context(), "", "notes.qqq", []byte("anything"), tsparse.Analysis)
	diags := parsed.Diagnostics
	if parsed.Status == syntaxhealth.StatusClean || parsed.Status == syntaxhealth.StatusBroken {
		t.Fatal("parsed = true for unknown grammar")
	}
	if diags != nil {
		t.Fatalf("diags = %+v want nil", diags)
	}
}

func TestAnalysisDiagnosticsSameRowCollapsed(t *testing.T) {
	// Diagnostics share the cap by source row.
	src := []byte("package main\n\nvar x = ) ) ) ) )\n")
	parsed := syntaxhealth.Analyze(t.Context(), "go", "", src, tsparse.Analysis)
	diags := parsed.Diagnostics
	if parsed.Status != syntaxhealth.StatusClean && parsed.Status != syntaxhealth.StatusBroken {
		t.Fatal("parsed = false for supported grammar")
	}
	rows := map[int]int{}
	for _, d := range diags {
		rows[d.Row]++
		if rows[d.Row] > 1 {
			t.Fatalf("row %d reported %d times: %+v", d.Row, rows[d.Row], diags)
		}
	}
}

func TestAnalysisDiagnosticsMultiLanguageMatrix(t *testing.T) {
	cases := []struct {
		name     string
		lang     string
		filename string
		clean    string
		broken   string
	}{
		{
			name: "typescript", lang: "typescript", filename: "app.ts",
			clean:  "export function ok(): void {\n  return;\n}\n",
			broken: "export function bad(): void {\n  return;\n\nexport function next() {}\n",
		},
		{
			name: "python", lang: "python", filename: "app.py",
			clean:  "def ok():\n    return\n",
			broken: "'''unclosed\n",
		},
		{
			name: "rust", lang: "rust", filename: "app.rs",
			clean:  "fn ok() {\n    let x = 1;\n}\n",
			broken: "fn bad() {\n    let x = 1;\n\nfn next() {}\n",
		},
	}
	for _, c := range cases {
		t.Run(c.name+"/clean", func(t *testing.T) {
			parsed := syntaxhealth.Analyze(t.Context(), c.lang, c.filename, []byte(c.clean), tsparse.Analysis)
			diags := parsed.Diagnostics
			if parsed.Status != syntaxhealth.StatusClean && parsed.Status != syntaxhealth.StatusBroken {
				t.Fatal("parsed = false for supported grammar")
			}
			if len(diags) != 0 {
				t.Fatalf("diags = %+v want none", diags)
			}
		})
		t.Run(c.name+"/broken", func(t *testing.T) {
			parsed := syntaxhealth.Analyze(t.Context(), c.lang, c.filename, []byte(c.broken), tsparse.Analysis)
			diags := parsed.Diagnostics
			if parsed.Status != syntaxhealth.StatusClean && parsed.Status != syntaxhealth.StatusBroken {
				t.Fatal("parsed = false for supported grammar")
			}
			if len(diags) == 0 {
				t.Fatal("expected diagnostics for broken source")
			}
			if diags[0].Row < 1 || diags[0].Col < 1 {
				t.Fatalf("diag not 1-based: %+v", diags[0])
			}
		})
	}
}
