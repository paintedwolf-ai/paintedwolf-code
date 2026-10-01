package api

import "testing"

func TestFindingLevelBucketsContainUnknownExactlyOnce(t *testing.T) {
	seen := map[string]bool{}
	for _, level := range DefaultFindingLevelBucketKeys() {
		if seen[level] {
			t.Fatalf("duplicate histogram bucket %q", level)
		}
		seen[level] = true
	}
	if !seen[string(FindingLevelUnknown)] || len(seen) != len(AllFindingLevelValues()) {
		t.Fatalf("buckets differ from the wire vocabulary: %v", seen)
	}
}
