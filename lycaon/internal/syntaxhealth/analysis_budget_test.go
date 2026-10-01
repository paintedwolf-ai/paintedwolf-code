package syntaxhealth_test

import (
	"strings"
	"testing"

	"github.com/lycaon/lycaon/config"
	"github.com/lycaon/lycaon/config/configtest"
	"github.com/lycaon/lycaon/internal/syntaxhealth"
	"github.com/lycaon/lycaon/internal/tsparse"
)

// pathologicalBash drives sustained parser recovery.
func pathologicalBash(lines int) []byte {
	var b strings.Builder
	b.WriteString("  log) printf \"%s\\n\" \"x\" ;;\n")
	for i := 0; i < lines; i++ {
		b.WriteString("  local va=\"${arr[@]:-}\"\n")
		b.WriteString("  if [[ -n \"${v}\" ]]; then printf '%s' \"${v}\"; fi\n")
	}
	return []byte(b.String())
}

// An incomplete parse returns no syntax verdict.
func TestAnalysisDiagnosticsMakesNoClaimWhenParseExceedsBudget(t *testing.T) {
	configtest.Overlay(t, map[config.Rel]string{config.SourceParsing: "version: 1\nvalidation_timeout_ms: 30000\nanalysis_timeout_ms: 1\n"})
	parsed := syntaxhealth.Analyze(t.Context(), "bash", "", pathologicalBash(160), tsparse.Analysis)
	if parsed.Status == syntaxhealth.StatusClean || parsed.Status == syntaxhealth.StatusBroken {
		t.Fatal("analysis claimed parse health for a budget-exhausted parse")
	}
}

func TestAnalysisDiagnosticsStillReportsOnOrdinarySource(t *testing.T) {
	parsed := syntaxhealth.Analyze(t.Context(), "bash", "", []byte("echo hello\nls -la\n"), tsparse.Analysis)
	diags := parsed.Diagnostics
	if parsed.Status != syntaxhealth.StatusClean && parsed.Status != syntaxhealth.StatusBroken {
		t.Fatal("analysis declined a clean, ordinary source")
	}
	if len(diags) != 0 {
		t.Fatalf("clean source reported %d diagnostic(s): %+v", len(diags), diags)
	}
}
