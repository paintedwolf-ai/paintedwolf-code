package main

import (
	"testing"

	"github.com/lycaon/lycaon/internal/scan/opengrep"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/pkg/api"
)

func TestProjectDiagnosticsKeepAnalysisModesDistinct(t *testing.T) {
	t.Parallel()
	intra := expectedProjectDiagnostic{File: "app.js", expectedDiagnostic: expectedDiagnostic{Kind: api.ScanWarningFilePartialSemantics, Construct: "closure_call_limit", Line: 1, Column: 1}}
	file := intra
	file.Construct = "recursive_signature_limit"
	project := projectCase{Name: "recursive", Files: map[string]string{"app.js": "recurse();\n"}, Diagnostics: map[opengrep.Mode][]expectedProjectDiagnostic{
		opengrep.Intraprocedural: {intra}, opengrep.Intrafile: {file},
	}}
	testutil.FailErr(t, "validate mode-specific diagnostics", validateProjectDiagnostics(project))
	testutil.FailErr(t, "compare intraprocedural diagnostics", compareProjectDiagnostics(project, opengrep.Intraprocedural, []expectedProjectDiagnostic{intra}))
	testutil.FailErr(t, "compare intrafile diagnostics", compareProjectDiagnostics(project, opengrep.Intrafile, []expectedProjectDiagnostic{file}))
	for _, actual := range [][]expectedProjectDiagnostic{nil, {intra}, {file, intra}} {
		if err := compareProjectDiagnostics(project, opengrep.Intrafile, actual); err == nil {
			t.Fatalf("accepted different analysis diagnostics: %+v", actual)
		}
	}
}

func TestProjectDiagnosticModesRejectIncompleteOrUnknownPolicies(t *testing.T) {
	t.Parallel()
	for name, diagnostics := range map[string]map[opengrep.Mode][]expectedProjectDiagnostic{
		"missing intrafile": {opengrep.Intraprocedural: nil},
		"unknown mode":      {opengrep.Intraprocedural: nil, opengrep.Intrafile: nil, "other": nil},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			if err := validateProjectDiagnostics(projectCase{Name: name, Diagnostics: diagnostics}); err == nil {
				t.Fatal("invalid mode expectations accepted")
			}
		})
	}
}

func TestProjectDiagnosticSchemaRejectsFlatLists(t *testing.T) {
	t.Parallel()
	var project projectCase
	if err := decodeCorpus([]byte("name: legacy\ndiagnostics: []\n"), &project); err == nil {
		t.Fatal("flat diagnostic list accepted")
	}
}
