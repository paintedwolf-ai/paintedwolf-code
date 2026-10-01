package definition

import (
	"testing"
)

func TestIsKnownCompleteWhenCompound(t *testing.T) {
	cases := []struct {
		expr string
		ok   bool
	}{
		{"delegation_closeout_complete and evidence_passed:verify", true},
		{"human_approval and unknown_leaf_xyz", false},
		{"plan_stub_valid", true},
		{"gates_satisfied", true},
	}
	for _, tc := range cases {
		if got := IsKnownCompleteWhen(tc.expr); got != tc.ok {
			t.Fatalf("IsKnownCompleteWhen(%q) = %v want %v", tc.expr, got, tc.ok)
		}
	}
}
