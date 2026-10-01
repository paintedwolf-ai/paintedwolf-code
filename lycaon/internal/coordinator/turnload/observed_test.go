package turnload

import (
	"testing"

	"github.com/lycaon/lycaon/internal/promptunit"
)

func TestObservedKind(t *testing.T) {
	cases := []struct {
		called []string
		want   string
	}{
		{nil, KindAnswerOnly},
		{[]string{"read", "grep"}, KindInspect},
		{[]string{"read", "command"}, KindRun},
		{[]string{"command", "edit"}, KindChange},
		{[]string{"edit", "delegate_dispatch"}, KindDelegate},
		{[]string{"task"}, KindDelegate},
	}
	for _, c := range cases {
		if got := ObservedKind(c.called); got != c.want {
			t.Errorf("ObservedKind(%v) = %q, want %q", c.called, got, c.want)
		}
	}
}

func TestGuideLabelsFollowAttachedAndNeededWithTools(t *testing.T) {
	scored := []promptunit.Unit{
		{ID: "git-guide", Attaches: []string{"git_status", "git_diff"}},
		{ID: "web-guide", Attaches: []string{"web_search"}},
		{ID: "evidence", NeededWith: []string{"capture_page", "verify"}},
		{ID: "survey", NeededWith: []string{"list_dir", "summarize"}},
		{ID: "style", Attaches: nil},
	}
	got := GuideLabels(scored, []string{"read", "git_diff", "verify"})
	if got["git-guide"] == nil || !*got["git-guide"] {
		t.Errorf("git-guide = %v, want true", got["git-guide"])
	}
	if got["web-guide"] == nil || *got["web-guide"] {
		t.Errorf("web-guide = %v, want false", got["web-guide"])
	}
	if got["evidence"] == nil || !*got["evidence"] {
		t.Errorf("evidence = %v, want true from a needed_with tool", got["evidence"])
	}
	if got["survey"] == nil || *got["survey"] {
		t.Errorf("survey = %v, want false when none of its needed_with tools ran", got["survey"])
	}
	if label, ok := got["style"]; !ok || label != nil {
		t.Errorf("style = %v (present %v), want an explicit nil", label, ok)
	}
}
