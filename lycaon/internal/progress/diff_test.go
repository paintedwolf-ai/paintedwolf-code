package progress_test

import (
	"testing"

	"github.com/lycaon/lycaon/internal/progress"
	api "github.com/lycaon/lycaon/pkg/api"
)

func snap(content string, ids ...string) progress.Snapshot {
	return progress.Snapshot{Content: content, ItemIDs: ids}
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
			prev: snap("- [ ] a\n- [x] b", "id-1", "id-2"),
			next: snap("- [ ] a\n- [x] b", "id-1", "id-2"),
			want: nil,
		},
		{
			name: "create from empty",
			prev: snap(""),
			next: snap("- [ ] a\n- [ ] b", "id-1", "id-2"),
			want: []api.ProgressChange{
				change(api.ProgressChangeCreated, "a", "", "pending"),
				change(api.ProgressChangeCreated, "b", "", "pending"),
			},
		},
		{
			name: "mark done",
			prev: snap("- [ ] a\n- [ ] b", "id-1", "id-2"),
			next: snap("- [ ] a\n- [x] b", "id-1", "id-2"),
			want: []api.ProgressChange{change(api.ProgressChangeDone, "b", "", "done")},
		},
		{
			name: "mark na",
			prev: snap("- [ ] a", "id-1"),
			next: snap("- [~] a", "id-1"),
			want: []api.ProgressChange{change(api.ProgressChangeNA, "a", "", "na")},
		},
		{
			name: "reopened",
			prev: snap("- [x] a", "id-1"),
			next: snap("- [ ] a", "id-1"),
			want: []api.ProgressChange{change(api.ProgressChangeReopened, "a", "", "pending")},
		},
		{
			name: "label edit pairs into updated",
			prev: snap("- [ ] old label", "id-1"),
			next: snap("- [ ] new label", "id-1"),
			want: []api.ProgressChange{change(api.ProgressChangeUpdated, "new label", "old label", "pending")},
		},
		{
			name: "relabel and reorder preserves identity",
			prev: snap("- [ ] first old\n- [ ] second old", "id-1", "id-2"),
			next: snap("- [ ] second new\n- [ ] first new", "id-2", "id-1"),
			want: []api.ProgressChange{
				change(api.ProgressChangeUpdated, "second new", "second old", "pending"),
				change(api.ProgressChangeUpdated, "first new", "first old", "pending"),
			},
		},
		{
			name: "duplicate labels track distinct item IDs",
			prev: snap("- [ ] same\n- [ ] same", "id-1", "id-2"),
			next: snap("- [ ] same\n- [x] same", "id-2", "id-1"),
			want: []api.ProgressChange{change(api.ProgressChangeDone, "same", "", "done")},
		},
		{
			name: "removed",
			prev: snap("- [ ] a\n- [ ] b", "id-1", "id-2"),
			next: snap("- [ ] a", "id-1"),
			want: []api.ProgressChange{change(api.ProgressChangeRemoved, "b", "", "pending")},
		},
		{
			name: "reorder is no change",
			prev: snap("- [ ] a\n- [x] b", "id-1", "id-2"),
			next: snap("- [x] b\n- [ ] a", "id-2", "id-1"),
			want: nil,
		},
		{
			name: "add and complete in one diff",
			prev: snap("- [ ] a", "id-1"),
			next: snap("- [x] a\n- [ ] b", "id-1", "id-2"),
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
