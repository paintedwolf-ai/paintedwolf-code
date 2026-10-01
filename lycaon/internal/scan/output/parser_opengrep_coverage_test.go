package output

import (
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/pkg/api"
)

// Timed-out targets produce coverage warnings alongside findings.
func TestOpengrepFatalsSurfaceAsWarningsWhenFindingsExist(t *testing.T) {
	raw := []byte(`{
	  "results": [
	    {"check_id": "rules.sql-injection", "path": "app/db.go",
	     "start": {"line": 12, "col": 3}, "end": {"line": 12, "col": 4},
	     "extra": {"message": "possible sql injection", "severity": "ERROR"}}
	  ],
	  "errors": [
	    {"type": "Timeout", "level": "error", "path": "vendor/huge_generated.go",
	     "message": "Timeout when running rules on target"}
	  ]
	}`)

	result, err := opengrepJSONParser{}.Parse(raw)
	testutil.FailErr(t, "parse scan result", err)
	if result.FindingsCount != 1 {
		t.Fatalf("findings = %d want 1", result.FindingsCount)
	}
	if len(result.Warnings) == 0 {
		t.Fatal("a timed-out target produced no warning: the coverage gap is invisible")
	}
	var found bool
	for _, w := range result.Warnings {
		if w.Kind != api.ScanWarningTargetUnscanned {
			continue
		}
		if strings.Contains(w.File, "huge_generated.go") && strings.Contains(w.Message, "Timeout") {
			found = true
		}
	}
	if !found {
		t.Fatalf("warnings %+v carry no target_unscanned row naming the timed-out target", result.Warnings)
	}
}

// A fatal with no results fails the scan outright.
func TestOpengrepFatalWithNoResultsStillFails(t *testing.T) {
	raw := []byte(`{
	  "results": [],
	  "errors": [
	    {"type": "Fatal error", "level": "error", "message": "engine crashed"}
	  ]
	}`)

	parser := opengrepJSONParser{}
	if _, err := parser.Parse(raw); err == nil {
		t.Fatal("a fatal with no results must fail the scan")
	}
}

// Mapped warning types keep their kinds and are not duplicated.
func TestOpengrepMappedWarningsAreNotDuplicatedAsFatals(t *testing.T) {
	raw := []byte(`{
	  "results": [
	    {"check_id": "rules.x", "path": "a.go", "start": {"line": 1, "col": 1}, "end": {"line": 1, "col": 2},
	     "extra": {"message": "m", "severity": "WARNING"}}
	  ],
	  "errors": [
	    {"type": "PartialParsing", "level": "warn", "path": "b.go", "message": "partial"}
	  ]
	}`)

	result, err := opengrepJSONParser{}.Parse(raw)
	testutil.FailErr(t, "parse scan result", err)
	if len(result.Warnings) != 1 {
		t.Fatalf("warnings = %+v want exactly the one mapped PartialParsing warning", result.Warnings)
	}
}
