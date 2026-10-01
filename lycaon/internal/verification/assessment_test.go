package verification

import "testing"

func TestAssessmentRequiresScopeAndReason(t *testing.T) {
	for _, method := range []string{Inspection, Targeted, Project, Blocked} {
		if !(&Assessment{Method: method, Reason: "Task-specific explanation"}).Valid() {
			t.Errorf("valid method %q rejected", method)
		}
		if (&Assessment{Method: method}).Valid() {
			t.Errorf("method %q accepted without a reason", method)
		}
	}
	for _, method := range []string{"", "passed", "skip", "docs"} {
		if (&Assessment{Method: method, Reason: "Explanation"}).Valid() {
			t.Errorf("unknown method %q accepted", method)
		}
	}
	var absent *Assessment
	if absent.Valid() {
		t.Fatal("missing assessment accepted")
	}
}
