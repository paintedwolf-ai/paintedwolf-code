package evidence_test

import (
	"testing"

	"github.com/lycaon/lycaon/internal/evidence"
)

func TestCanonicalSnapshotHashStable(t *testing.T) {
	ledger := evidence.AssembleLedger([]evidence.Record{
		{Handle: "read#2", Kind: "read", Path: "b.go", Body: []string{"2| b"}, LineRanges: []evidence.LineRange{{Start: 2, End: 2}}},
		{Handle: "read#1", Kind: "read", Path: "a.go", Body: []string{"1| a"}, LineRanges: []evidence.LineRange{{Start: 1, End: 1}}},
	})
	h1 := evidence.CanonicalSnapshotHash(ledger)
	h2 := evidence.CanonicalSnapshotHash(ledger)
	if h1 == "" || h1 != h2 {
		t.Fatalf("hash = %q %q", h1, h2)
	}
}

func TestCurationCacheKeyFocusSuffix(t *testing.T) {
	ledger := evidence.AssembleLedger([]evidence.Record{
		{Handle: "read#1", Kind: "read", Path: "a.go", Body: []string{"1| a"}},
	})
	a := evidence.CurationCacheKey(ledger, "pattern")
	b := evidence.CurationCacheKey(ledger, "other")
	if a == b {
		t.Fatalf("focus should change cache key")
	}
}

func TestSnapshotHashIncludesFileBindingCurrency(t *testing.T) {
	records := []evidence.Record{
		{Handle: "read#1", Kind: "read", Path: "a.go", Body: []string{"old contents"}},
		{Handle: "read#2", Kind: "read", Path: "a.go", Body: []string{"new contents"}},
	}
	before := evidence.CanonicalSnapshotHash(evidence.AssembleLedger(records))
	records[0].SupersededBy = "read#2"
	after := evidence.CanonicalSnapshotHash(evidence.AssembleLedger(records))
	if before == after {
		t.Fatal("changed file bindings reused a cached snapshot")
	}
}
