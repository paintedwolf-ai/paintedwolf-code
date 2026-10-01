package output

import (
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/pkg/api"
)

func TestSemanticGapRetainsLocationAndMakesCoveragePartial(t *testing.T) {
	raw := `{"results":[],"errors":[{"type":["PartialSemantics","unsupported_ast_to_il"],"level":"warn","path":"app.py","message":"Unsupported dataflow semantics","spans":[{"file":"app.py","start":{"line":3,"col":7},"end":{"line":3,"col":10}}]}]}`
	result, err := ParseOpengrepJSON(strings.NewReader(raw))
	testutil.FailErr(t, "parse semantic gap", err)
	if len(result.Findings) != 0 || len(result.Warnings) != 1 {
		t.Fatalf("semantic gap became a finding or was lost: %+v", result)
	}
	warning := result.Warnings[0]
	if warning.Kind != api.ScanWarningFilePartialSemantics || warning.Construct != "unsupported_ast_to_il" || warning.File != "app.py" || warning.StartLine != 3 || warning.StartColumn != 7 {
		t.Fatalf("semantic evidence changed: %+v", warning)
	}
	if CoverageForResult(result, "exact", "") != api.ScanCoveragePartial {
		t.Fatal("incomplete semantics were reported as complete coverage")
	}
	for name, invalid := range map[string]string{
		"missing construct": strings.Replace(raw, `["PartialSemantics","unsupported_ast_to_il"]`, `"PartialSemantics"`, 1),
		"inconsistent path": strings.Replace(raw, `"file":"app.py"`, `"file":"other.py"`, 1),
		"invalid line":      strings.Replace(raw, `"line":3`, `"line":0`, 1),
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := ParseOpengrepJSON(strings.NewReader(invalid)); err == nil {
				t.Fatal("malformed semantic evidence accepted")
			}
		})
	}
}

func TestSemanticGapPreservesUsableFindings(t *testing.T) {
	raw := `{"results":[{"check_id":"test.sql","path":"app.py","start":{"line":1,"col":1},"end":{"line":1,"col":12},"extra":{"message":"SQL concatenation","severity":"ERROR"}}],"errors":[{"type":["PartialSemantics","unsupported_ast_to_il:Slices"],"level":"warn","path":"app.py","message":"Unsupported dataflow semantics","spans":[{"file":"app.py","start":{"line":3,"col":7},"end":{"line":3,"col":10}}]}]}`
	result, err := ParseOpengrepJSON(strings.NewReader(raw))
	testutil.FailErr(t, "parse findings with semantic gap", err)
	if result.FindingsCount != 1 || len(result.Findings) != 1 || len(result.Warnings) != 1 {
		t.Fatalf("findings or diagnostics lost: %+v", result)
	}
	if result.Findings[0].RuleID != "opengrep:test.sql" || CoverageForResult(result, "exact", "") != api.ScanCoveragePartial {
		t.Fatalf("finding identity or partial coverage changed: %+v", result)
	}
}

func TestTargetFailureCoverageDoesNotDependOnUnrelatedFindings(t *testing.T) {
	for _, kind := range []string{"Lexical error", "Syntax error", "Other syntax error", "AST builder error", "Internal matching error", "Fatal error", "Too many matches", "Timeout", "Out of memory", "Stack overflow"} {
		t.Run(kind, func(t *testing.T) {
			for _, finding := range []string{"", oneOpengrepFinding} {
				diagnostic := `{"type":"` + kind + `","path":"bad.py","message":"target contains \\u0000"}`
				result, err := ParseOpengrepJSON(strings.NewReader(opengrepReport(finding, diagnostic)))
				testutil.FailErr(t, "parse target-local failure", err)
				if len(result.Warnings) != 1 || result.Warnings[0].File != "bad.py" || CoverageForResult(result, "exact", "") != api.ScanCoveragePartial {
					t.Fatalf("lost target gap: %+v", result)
				}
				diagnostic = `{"type":"` + kind + `","message":"global failure"}`
				if _, err := ParseOpengrepJSON(strings.NewReader(opengrepReport(finding, diagnostic))); err == nil {
					t.Fatal("unlocalized engine failure accepted")
				}
			}
		})
	}
}
