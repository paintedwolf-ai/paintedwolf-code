package bundleddriver

import (
	"fmt"
	"testing"

	scanoutput "github.com/lycaon/lycaon/internal/scan/output"
	"github.com/lycaon/lycaon/internal/scan/rules"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/pkg/api"
)

func TestCoverageObservationsStayInternal(t *testing.T) {
	cfg, err := rules.LoadOpengrepGates()
	testutil.FailErr(t, "load selection", err)
	bundle, err := rules.CompileGateRules(cfg, "")
	testutil.FailErr(t, "compile selection", err)
	result := &scanoutput.Result{Warnings: []api.ScanWarning{{Kind: api.ScanWarningFilePartialParse, File: "broken.go"}}}
	for i := 0; i < 1000; i++ {
		result.Findings = append(result.Findings, api.SecurityFinding{RuleID: "opengrep:lycaon.coverage.proto", Locations: []api.SecurityFindingLocation{{URI: fmt.Sprintf("schema%d.proto", i)}}})
	}
	result.FindingsCount = len(result.Findings)
	testutil.FailErr(t, "filter observations", filterNonCode(t.Context(), result, t.TempDir(), bundle))
	if len(result.Findings) != 0 || result.FindingsCount != 0 || len(result.Warnings) != 1 || result.Warnings[0].Kind != api.ScanWarningFilePartialParse {
		t.Fatalf("catalog observations leaked or real diagnostic disappeared: %+v", result)
	}
}
