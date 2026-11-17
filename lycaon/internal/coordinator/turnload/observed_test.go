package turnload

import "testing"

func TestObservedKind(t *testing.T) {
	cases := []struct {
		called []string
		want   string
	}{
		{nil, KindAnswerOnly},
		{[]string{"read", "grep"}, KindInspect},
		{[]string{"read", "command"}, KindRun},
		{[]string{"command", "edit"}, KindChange},
		{[]string{"edit", "instruct_worker"}, KindDelegate},
		{[]string{"task"}, KindDelegate},
	}
	for _, c := range cases {
		if got := ObservedKind(c.called); got != c.want {
			t.Errorf("ObservedKind(%v) = %q, want %q", c.called, got, c.want)
		}
	}
}
