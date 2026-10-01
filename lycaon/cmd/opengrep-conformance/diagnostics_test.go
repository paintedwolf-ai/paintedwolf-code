package main

import (
	"testing"

	"github.com/lycaon/lycaon/internal/scan/opengrep"
	"github.com/lycaon/lycaon/pkg/api"
)

func TestExpectedDiagnosticDoesNotPermitAnotherLostAnalysisRegion(t *testing.T) {
	diagnostic := expectedDiagnostic{Kind: api.ScanWarningFilePartialSemantics, Construct: "unsupported_ast_to_il", Line: 3, Column: 2}
	cases := map[string]sourceCase{"source/app.py": {Diagnostics: []expectedDiagnostic{diagnostic}}}
	warnings := []api.ScanWarning{{Kind: diagnostic.Kind, Construct: diagnostic.Construct, File: "source/app.py", StartLine: 3, StartColumn: 2}}
	if err := compareCaseDiagnostics(warnings, cases); err != nil {
		t.Fatalf("declared limitation rejected: %v", err)
	}
	warnings[0].StartLine = 5
	if err := compareCaseDiagnostics(warnings, cases); err == nil {
		t.Fatal("a different unsupported region was silently accepted")
	}
	if err := compareCaseDiagnostics(nil, cases); err == nil {
		t.Fatal("a disappeared diagnostic bypassed fixture review")
	}
}

func TestExpectedCoverageGapStillRejectsAdmission(t *testing.T) {
	measurements := []projectMeasurement{{Mode: opengrep.Intrafile, Run: 1, TruePositive: 40,
		Diagnostics:   []expectedProjectDiagnostic{{expectedDiagnostic: expectedDiagnostic{Kind: api.ScanWarningFilePartialSemantics, Construct: "unsupported"}}},
		MemorySamples: 1, PeakRSSBytes: 100, OutputBytes: 100, Milliseconds: 1}}
	summary := summarizeProjects(opengrep.Intrafile, measurements)
	precision, lower := 1.0, 0.99
	summary.Precision, summary.PrecisionLowerBound = &precision, &lower
	policy := admissionPolicy{MinimumTruePositives: 1, MinimumProjectPrecisionLowerBound: 0.9, MaximumP95Milliseconds: 100, MaximumPeakRSSBytes: 1000, MaximumOutputBytes: 1000}
	if reasons := policy.reject(summary); len(reasons) != 1 || reasons[0] != "coverage diagnostics remain in the admitted scope" {
		t.Fatalf("declared gap was admitted: %v", reasons)
	}
}
