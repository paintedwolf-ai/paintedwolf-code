package scan

import (
	"testing"
	"time"

	"github.com/lycaon/lycaon/pkg/api"
)

func scanAt(id, scanner string, at time.Time, status api.CodeScanStatus, coverage api.ScanCoverageStatus, warnings ...api.ScanWarning) api.CodeScan {
	return api.CodeScan{ID: id, ScannerID: scanner, CreatedAt: at, Status: status, CoverageStatus: coverage, Warnings: warnings}
}

// A file that changed mid-scan is a gap until a later complete scan by the
// same scanner covers it without seeing it move again; engine limits are
// counted apart because scanning again never closes them.
func TestRunCoverageGaps(t *testing.T) {
	t0 := time.Date(2026, 9, 18, 22, 9, 50, 0, time.UTC)
	moved := func(file string) api.ScanWarning {
		return api.ScanWarning{Kind: api.ScanWarningSourceMoved, File: file}
	}
	semantics := func(file string) api.ScanWarning {
		return api.ScanWarning{Kind: api.ScanWarningFilePartialSemantics, File: file}
	}
	full := scanAt("full", "secrets", t0, api.CodeScanStatusComplete, api.ScanCoveragePartial, moved("a.go"), moved("b.go"))
	sast := scanAt("sast", "sast", t0, api.CodeScanStatusComplete, api.ScanCoveragePartial, semantics("x.rs"), semantics("x.rs"), semantics("y.rs"))

	gaps := RunCoverageGaps([]api.CodeScan{full, sast})
	if len(gaps[0].Moved) != 2 || gaps[1].Open() || gaps[1].Standing != 3 || gaps[1].StandingFiles != 2 {
		t.Fatalf("gaps = %+v, want two open moved files and three standing limits over two files", gaps)
	}

	rescan := scanAt("rescan", "secrets", t0.Add(time.Minute), api.CodeScanStatusComplete, api.ScanCoverageComplete)
	rescan.TargetKind, rescan.TargetPaths = api.ScanTargetPaths, []string{"a.go"}
	if !ClosesMovedFiles([]api.CodeScan{full, sast}, rescan) {
		t.Fatal("a later rescan of a moved file must close it")
	}
	gaps = RunCoverageGaps([]api.CodeScan{full, sast, rescan})
	if len(gaps[0].Moved) != 1 || gaps[0].Moved[0] != "b.go" {
		t.Fatalf("moved after rescan = %v, want only b.go", gaps[0].Moved)
	}

	earlier := scanAt("earlier", "secrets", t0.Add(-time.Minute), api.CodeScanStatusComplete, api.ScanCoverageComplete)
	other := scanAt("other", "sca", t0.Add(time.Minute), api.CodeScanStatusComplete, api.ScanCoverageComplete)
	movedAgain := scanAt("again", "secrets", t0.Add(time.Minute), api.CodeScanStatusComplete, api.ScanCoveragePartial, moved("b.go"))
	// An earlier scan, another scanner, and a scan whose own reading moved
	// under it close nothing.
	for _, c := range []api.CodeScan{earlier, other, movedAgain} {
		if ClosesMovedFiles([]api.CodeScan{full}, c) {
			t.Fatalf("scan %s closed a gap it cannot close", c.ID)
		}
	}
}
