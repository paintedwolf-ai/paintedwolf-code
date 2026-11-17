package workerlane

import (
	"testing"

	"github.com/lycaon/lycaon/internal/workerrecords"
)

// A rejection returns the lane to what it held before the rejected result,
// whatever happened to the results before it.
func TestRejectionResetsToWhatTheLaneHeldBefore(t *testing.T) {
	type earlier struct {
		checkpoint string
		fate       ChangeState
	}
	for _, tc := range []struct {
		name    string
		earlier []earlier
		base    string
		want    string
	}{
		{name: "first result", want: ""},
		{name: "first result of a stacked worker", base: "cp-base", want: "cp-base"},
		{name: "pending result before", earlier: []earlier{{"cp-1", ChangeNone}}, want: "cp-1"},
		{name: "shelved result before", earlier: []earlier{{"cp-1", ChangeShelved}}, want: "cp-1"},
		{name: "landed result before", earlier: []earlier{{"cp-1", ChangeLanded}}, base: "cp-base", want: ""},
		{name: "round without changes between", earlier: []earlier{{"cp-1", ChangeNone}, {"", ChangeNone}}, want: "cp-1"},
		{name: "second rejection in a row", earlier: []earlier{{"cp-1", ChangeNone}, {"cp-2", ChangeRejected}}, want: "cp-1"},
		{name: "every result rejected", earlier: []earlier{{"cp-1", ChangeRejected}, {"cp-2", ChangeRejected}}, base: "cp-base", want: "cp-base"},
		{name: "landing carried the pending result before it", earlier: []earlier{{"cp-1", ChangeNone}, {"cp-2", ChangeLanded}}, want: ""},
		{name: "rejection after a landing", earlier: []earlier{{"cp-1", ChangeLanded}, {"cp-2", ChangeRejected}}, want: ""},
		{name: "pending result after a landing", earlier: []earlier{{"cp-1", ChangeLanded}, {"cp-2", ChangeNone}}, want: "cp-2"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			rounds := make([]workerrecords.Round, len(tc.earlier))
			changes := map[string]string{}
			for i, e := range tc.earlier {
				id := "result-" + string(rune('a'+i))
				rounds[i] = workerrecords.Round{ResultID: id, CheckpointID: e.checkpoint}
				changes[id] = string(e.fate)
			}
			if got := checkpointBefore(rounds, changes, tc.base); got != tc.want {
				t.Fatalf("lane before the rejected result = %q, want %q", got, tc.want)
			}
		})
	}
}
