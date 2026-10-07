package guidance

import (
	"testing"

	"github.com/lycaon/lycaon/internal/evidence"
)

func TestCitedHandleRangesDescribesResolvedHandlesOnly(t *testing.T) {
	ev := evidence.AssembleLedger([]evidence.Record{
		{Handle: "read#3", Kind: "read", Shape: evidence.ShapeFileRegion, Path: "internal/auth.go", LineRanges: []evidence.LineRange{{Start: 1, End: 120}, {Start: 300, End: 340}}},
	})
	got := CitedHandleRanges([]evidence.Resolution{
		{Handle: "read#3", Path: "internal/auth.go", Line: 412, Verdict: evidence.VerdictUnverifiable},
		{Handle: "read#9", Verdict: evidence.VerdictUnverifiable},
		{Handle: "read#3", Line: 500, Verdict: evidence.VerdictUnverifiable},
		{Path: "other.go", Line: 1, Verdict: evidence.VerdictUnverifiable},
	}, ev)
	if got != "read#3 internal/auth.go lines 1-120,300-340" {
		t.Fatalf("ranges = %q", got)
	}
}
