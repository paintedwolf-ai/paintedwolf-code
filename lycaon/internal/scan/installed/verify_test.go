package installed

import (
	"context"
	"errors"
	"path/filepath"
	"testing"

	scanoutput "github.com/lycaon/lycaon/internal/scan/output"
	"github.com/lycaon/lycaon/pkg/api"
)

func qualifiedResult(project string) *scanoutput.Result {
	result := &scanoutput.Result{}
	for _, fixture := range fixtures {
		if fixture.rule == "" {
			continue
		}
		location := api.SecurityFindingLocation{URI: filepath.Join(project, fixture.file), StartLine: fixture.line, StartColumn: 1, EndLine: fixture.line, EndColumn: 2}
		finding := api.SecurityFinding{RuleID: fixture.rule, Locations: []api.SecurityFindingLocation{location}}
		if fixture.file == "app.vue" {
			finding.Dataflow = &api.SecurityFindingDataflow{Source: &api.SecurityFindingCallTrace{Location: location}, Sink: &api.SecurityFindingCallTrace{Location: location}}
		}
		result.Findings = append(result.Findings, finding)
	}
	result.FindingsCount = len(result.Findings)
	return result
}

func TestInstalledCasesRequireMappedFindingsAndNegativeControls(t *testing.T) {
	project := t.TempDir()
	cases, err := verifyResult(project, qualifiedResult(project))
	if err != nil || len(cases) != len(fixtures) {
		t.Fatalf("complete product cases=%+v error=%v", cases, err)
	}
	for _, test := range cases {
		if !test.Passed || (test.ExpectedRule == "" && test.Findings != 0) {
			t.Fatalf("case result = %+v", test)
		}
	}
}

func TestInstalledCasesRefusePartialOrMisattributedResults(t *testing.T) {
	project := t.TempDir()
	for name, change := range map[string]func(*scanoutput.Result){
		"inconsistent count": func(result *scanoutput.Result) { result.FindingsCount++ },
		"partial warning": func(result *scanoutput.Result) {
			result.Warnings = []api.ScanWarning{{Kind: api.ScanWarningTargetUnscanned}}
		},
		"missing location":  func(result *scanoutput.Result) { result.Findings[0].Locations = nil },
		"wrong source line": func(result *scanoutput.Result) { result.Findings[0].Locations[0].StartLine++ },
		"wrong rule":        func(result *scanoutput.Result) { result.Findings[0].RuleID = "another-rule" },
		"outside source": func(result *scanoutput.Result) {
			result.Findings[0].Locations[0].URI = filepath.Join(t.TempDir(), "main.rs")
		},
		"missing flow": func(result *scanoutput.Result) { result.Findings[1].Dataflow = nil },
		"unmapped evidence": func(result *scanoutput.Result) {
			result.Findings[1].Dataflow.Source.Location.URI = "embedded/app.vue.script-1.ts"
		},
		"negative control finding": func(result *scanoutput.Result) { result.Findings[0].Locations[0].URI = "safe.html" },
		"duplicate finding": func(result *scanoutput.Result) {
			result.Findings = append(result.Findings, result.Findings[0])
			result.FindingsCount++
		},
		"omitted finding": func(result *scanoutput.Result) { result.Findings = result.Findings[:2]; result.FindingsCount-- },
	} {
		t.Run(name, func(t *testing.T) {
			result := qualifiedResult(project)
			change(result)
			if _, err := verifyResult(project, result); err == nil {
				t.Fatal("incomplete installed qualification passed")
			}
		})
	}
	if _, err := verifyResult(project, nil); err == nil {
		t.Fatal("absent results passed")
	}
}

func TestInstalledVerificationRequiresTheExplicitPackagedRoot(t *testing.T) {
	t.Setenv("LYCAON_OPENGREP_CANDIDATE", "")
	for _, root := range []string{"", "relative", filepath.Join(t.TempDir(), "missing")} {
		report, err := Verify(t.Context(), root)
		if err == nil || report.Status != "failed" || report.Code != "root_invalid" || report.Confined || report.Error == "" {
			t.Fatalf("invalid root %q report=%+v error=%v", root, report, err)
		}
	}
	report, err := Verify(t.Context(), t.TempDir())
	if err == nil || report.Status != "failed" || report.Code != "artifact_refused" || report.Confined || report.Binary != "" {
		t.Fatalf("missing packaged artifact report=%+v error=%v", report, err)
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	report, err = Verify(ctx, t.TempDir())
	if !errors.Is(err, context.Canceled) || report.Code != "canceled" || report.Confined {
		t.Fatalf("canceled qualification report=%+v error=%v", report, err)
	}
}
