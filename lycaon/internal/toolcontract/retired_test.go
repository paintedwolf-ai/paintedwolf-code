package toolcontract

import (
	"errors"
	"slices"
	"testing"
)

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

// A retired name is refused with its successor; live tools and command
// operations that kept a retired tool's name are not.
func TestRetiredUseRefusesOnlyRetiredNames(t *testing.T) {
	for _, live := range []string{"state_start", "read", "git_commit", "", "unknown_tool"} {
		if err := RetiredUse("site", live); err != nil {
			t.Errorf("RetiredUse(%q) = %v", live, err)
		}
	}
	var retired *RetiredToolError
	err := RetiredUse("profile \"p\" grant", "state_close")
	if !errors.As(err, &retired) || retired.Tool != "state_close" || retired.Retirement.ContentContract != "paintedwolf.workflow_run_exit" || retired.Error() == "" {
		t.Fatalf("RetiredUse(state_close) = %v", err)
	}
	if r, ok := Retired("handoff_init"); !ok || !slices.Equal(r.Successors, []string{"handoff_reserve"}) {
		t.Fatalf("Retired(handoff_init) = %+v, %v", r, ok)
	}
}
