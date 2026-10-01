package sourcecomparison

import (
	"encoding/json"
	"os"
	"testing"

	"github.com/lycaon/lycaon/internal/testutil"
)

// Den runs these same fixtures against the live editor's incremental comparison.
func TestComparisonSemantics(t *testing.T) {
	raw, err := os.ReadFile("testdata/semantics.json")
	testutil.FailErr(t, "read shared semantics", err)
	var cases []struct {
		Name, Before, After string
		Added, Removed      int
	}
	testutil.FailErr(t, "decode shared semantics", json.Unmarshal(raw, &cases))
	for _, tc := range cases {
		t.Run(tc.Name, func(t *testing.T) {
			added, removed, err := Counts(tc.Before, tc.After)
			testutil.FailErr(t, "count comparison", err)
			if added != tc.Added || removed != tc.Removed {
				t.Fatalf("+%d -%d, want +%d -%d", added, removed, tc.Added, tc.Removed)
			}
		})
	}
}
