package progress_test

import (
	"testing"

	"github.com/lycaon/lycaon/internal/progress"
	api "github.com/lycaon/lycaon/pkg/api"
)

func row(id, state, label string) progress.Row {
	return progress.Row{ID: id, State: state, Label: label}
}

func change(kind api.ProgressChangeKind, label, prevLabel, state string) api.ProgressChange {
	return api.ProgressChange{Kind: kind, Label: label, PrevLabel: prevLabel, State: state}
}

func TestDiffSteps(t *testing.T) {
	cases := []struct {
		name string
		prev progress.Snapshot
		next progress.Snapshot
		want []api.ProgressChange
	}{
		{
			name: "no change",
			prev: progress.NewSnapshot("", row("p1", "pending", "a"), row("p2", "done", "b")),
			next: progress.NewSnapshot("", row("p1", "pending", "a"), row("p2", "done", "b")),
			want: nil,
		},
		{
			name: "create from empty",
			prev: progress.NewSnapshot(""),
			next: progress.NewSnapshot("", row("p1", "pending", "a"), row("p2", "pending", "b")),
			want: []api.ProgressChange{
				change(api.ProgressChangeCreated, "a", "", "pending"),
				change(api.ProgressChangeCreated, "b", "", "pending"),
			},
		},
		{
			name: "mark done",
			prev: progress.NewSnapshot("", row("p1", "pending", "a"), row("p2", "pending", "b")),
			next: progress.NewSnapshot("", row("p1", "pending", "a"), row("p2", "done", "b")),
			want: []api.ProgressChange{change(api.ProgressChangeDone, "b", "", "done")},
		},
		{
			name: "mark na",
			prev: progress.NewSnapshot("", row("p1", "pending", "a")),
			next: progress.NewSnapshot("", row("p1", "na", "a")),
			want: []api.ProgressChange{change(api.ProgressChangeNA, "a", "", "na")},
		},
		{
			name: "reopened",
			prev: progress.NewSnapshot("", row("p1", "done", "a")),
			next: progress.NewSnapshot("", row("p1", "pending", "a")),
			want: []api.ProgressChange{change(api.ProgressChangeReopened, "a", "", "pending")},
		},
		{
			name: "label edit pairs into updated",
			prev: progress.NewSnapshot("", row("p1", "pending", "old label")),
			next: progress.NewSnapshot("", row("p1", "pending", "new label")),
			want: []api.ProgressChange{change(api.ProgressChangeUpdated, "new label", "old label", "pending")},
		},
		{
			name: "relabel and reorder preserves identity",
			prev: progress.NewSnapshot("", row("p1", "pending", "first old"), row("p2", "pending", "second old")),
			next: progress.NewSnapshot("", row("p2", "pending", "second new"), row("p1", "pending", "first new")),
			want: []api.ProgressChange{
				change(api.ProgressChangeUpdated, "second new", "second old", "pending"),
				change(api.ProgressChangeUpdated, "first new", "first old", "pending"),
			},
		},
		{
			name: "duplicate labels track distinct item IDs",
			prev: progress.NewSnapshot("", row("p1", "pending", "same"), row("p2", "pending", "same")),
			next: progress.NewSnapshot("", row("p2", "pending", "same"), row("p1", "done", "same")),
			want: []api.ProgressChange{change(api.ProgressChangeDone, "same", "", "done")},
		},
		{
			name: "removed",
			prev: progress.NewSnapshot("", row("p1", "pending", "a"), row("p2", "pending", "b")),
			next: progress.NewSnapshot("", row("p1", "pending", "a")),
			want: []api.ProgressChange{change(api.ProgressChangeRemoved, "b", "", "pending")},
		},
		{
			name: "reorder is no change",
			prev: progress.NewSnapshot("", row("p1", "pending", "a"), row("p2", "done", "b")),
			next: progress.NewSnapshot("", row("p2", "done", "b"), row("p1", "pending", "a")),
			want: nil,
		},
		{
			name: "add and complete in one diff",
			prev: progress.NewSnapshot("", row("p1", "pending", "a")),
			next: progress.NewSnapshot("", row("p1", "done", "a"), row("p2", "pending", "b")),
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
