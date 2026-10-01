package progress_test

import (
	"testing"

	"github.com/lycaon/lycaon/internal/progress"
	api "github.com/lycaon/lycaon/pkg/api"
)

func step(state, label string) api.ProgressStep {
	return api.ProgressStep{State: state, Label: label}
}

func change(kind api.ProgressChangeKind, label, prevLabel, state string) api.ProgressChange {
	return api.ProgressChange{Kind: kind, Label: label, PrevLabel: prevLabel, State: state}
}

func TestDiffSteps(t *testing.T) {
	cases := []struct {
		name string
		prev []api.ProgressStep
		next []api.ProgressStep
		want []api.ProgressChange
	}{
		{
			name: "no change",
			prev: []api.ProgressStep{step("pending", "a"), step("done", "b")},
			next: []api.ProgressStep{step("pending", "a"), step("done", "b")},
			want: nil,
		},
		{
			name: "create from empty",
			prev: nil,
			next: []api.ProgressStep{step("pending", "a"), step("pending", "b")},
			want: []api.ProgressChange{
				change(api.ProgressChangeCreated, "a", "", "pending"),
				change(api.ProgressChangeCreated, "b", "", "pending"),
			},
		},
		{
			name: "mark done",
			prev: []api.ProgressStep{step("pending", "a"), step("pending", "b")},
			next: []api.ProgressStep{step("pending", "a"), step("done", "b")},
			want: []api.ProgressChange{change(api.ProgressChangeDone, "b", "", "done")},
		},
		{
			name: "mark na",
			prev: []api.ProgressStep{step("pending", "a")},
			next: []api.ProgressStep{step("na", "a")},
			want: []api.ProgressChange{change(api.ProgressChangeNA, "a", "", "na")},
		},
		{
			name: "reopened",
			prev: []api.ProgressStep{step("done", "a")},
			next: []api.ProgressStep{step("pending", "a")},
			want: []api.ProgressChange{change(api.ProgressChangeReopened, "a", "", "pending")},
		},
		{
			name: "label edit pairs into updated",
			prev: []api.ProgressStep{step("pending", "old label")},
			next: []api.ProgressStep{step("pending", "new label")},
			want: []api.ProgressChange{change(api.ProgressChangeUpdated, "new label", "old label", "pending")},
		},
		{
			name: "removed",
			prev: []api.ProgressStep{step("pending", "a"), step("pending", "b")},
			next: []api.ProgressStep{step("pending", "a")},
			want: []api.ProgressChange{change(api.ProgressChangeRemoved, "b", "", "pending")},
		},
		{
			name: "reorder is no change",
			prev: []api.ProgressStep{step("pending", "a"), step("done", "b")},
			next: []api.ProgressStep{step("done", "b"), step("pending", "a")},
			want: nil,
		},
		{
			name: "add and complete in one diff",
			prev: []api.ProgressStep{step("pending", "a")},
			next: []api.ProgressStep{step("done", "a"), step("pending", "b")},
			want: []api.ProgressChange{
				change(api.ProgressChangeDone, "a", "", "done"),
				change(api.ProgressChangeCreated, "b", "", "pending"),
			},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := progress.DiffSteps(tc.prev, tc.next)
			if len(got) != len(tc.want) {
				t.Fatalf("change count = %d, want %d: %+v", len(got), len(tc.want), got)
			}
			for i := range got {
				if got[i] != tc.want[i] {
					t.Errorf("change[%d] = %+v, want %+v", i, got[i], tc.want[i])
				}
			}
		})
	}
}
