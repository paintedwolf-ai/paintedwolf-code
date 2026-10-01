package pagedview

import (
	"fmt"
	"testing"

	"github.com/lycaon/lycaon/internal/testutil"
)

func TestRangeFingerprintSurvivesPageSplitsAndReplacements(t *testing.T) {
	index := &RangeIndex[string]{Store: &MemoryPages[string]{}}
	var expected, baseline Fingerprint
	for i := 0; i < 1000; i++ {
		key := fmt.Sprintf("%04d", i)
		flat := FingerprintOf([]byte("closed:" + key))
		expanded := FingerprintOf([]byte("open:" + key))
		expected = expected.Combine(expanded)
		baseline = baseline.Combine(flat)
		testutil.FailErr(t, "insert fingerprinted range", index.Set(t.Context(), RangeItem[string]{Key: key, Value: key, Weight: 2, Fingerprint: expanded, BaselineFingerprint: flat}))
	}
	got, err := index.Fingerprint(t.Context(), false)
	testutil.FailErr(t, "read recursive summary", err)
	if got != expected {
		t.Fatal("recursive summary differs after page splits")
	}
	got, err = index.Fingerprint(t.Context(), true)
	testutil.FailErr(t, "read baseline summary", err)
	if got != baseline {
		t.Fatal("baseline summary differs after page splits")
	}
	testutil.FailErr(t, "remove addressed row", index.Remove(t.Context(), "0500"))
	expected = expected.Combine(FingerprintOf([]byte("open:0500")))
	got, err = index.Fingerprint(t.Context(), false)
	testutil.FailErr(t, "read summary after removal", err)
	if got != expected {
		t.Fatal("removal left a stale summary")
	}
}
