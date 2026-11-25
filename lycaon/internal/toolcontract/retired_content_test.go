package toolcontract

import "testing"

// Only a tool retired to published text sends the model to write that text;
// a tool retired to host-owned context does not.
func TestRetiredToPublishedTextFollowsTheContentContract(t *testing.T) {
	for _, tc := range []struct {
		name string
		want bool
	}{
		{"surface_note", true},
		{"state_query", false},
		{"state_close", false},
		{"workflow_user_feedback", false},
		{"state_start", false},
	} {
		if got := RetiredToPublishedText(tc.name); got != tc.want {
			t.Errorf("RetiredToPublishedText(%s) = %v, want %v", tc.name, got, tc.want)
		}
	}
}
