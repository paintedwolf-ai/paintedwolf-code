package invocation

import (
	"errors"
	"testing"

	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/pkg/api"
)

func TestSelectActivityExecutionsMatchesExactAndFoldedExecutionIDs(t *testing.T) {
	activities := []api.CommandActivity{
		{ID: "cmd-activity", ExecutionIDs: []string{"cmd-activity", "read-stage", "exact-wins"}},
		{ID: "exact-wins", ExecutionIDs: []string{"exact-wins"}},
		{ID: "empty-execs"},
	}

	t.Run("empty id selects unfiltered", func(t *testing.T) {
		got, err := selectActivityExecutions(activities, "")
		testutil.FailErr(t, "select empty activity id", err)
		if got.ids != "" {
			t.Fatalf("selection = %q, want empty", got.ids)
		}
	})

	t.Run("exact activity id match", func(t *testing.T) {
		got, err := selectActivityExecutions(activities, "cmd-activity")
		testutil.FailErr(t, "select exact activity id", err)
		want := `["cmd-activity","read-stage","exact-wins"]`
		if got.ids != want {
			t.Fatalf("selection = %q, want %q", got.ids, want)
		}
		if matched := findActivity(activities, "cmd-activity"); matched == nil || matched.ID != "cmd-activity" {
			t.Fatalf("findActivity(cmd-activity) = %+v, want cmd-activity", matched)
		}
	})

	t.Run("exact activity id takes precedence over execution id membership", func(t *testing.T) {
		got, err := selectActivityExecutions(activities, "exact-wins")
		testutil.FailErr(t, "select exact activity id over folded member", err)
		want := `["exact-wins"]`
		if got.ids != want {
			t.Fatalf("selection = %q, want %q", got.ids, want)
		}
		if matched := findActivity(activities, "exact-wins"); matched == nil || matched.ID != "exact-wins" {
			t.Fatalf("findActivity(exact-wins) = %+v, want exact-wins", matched)
		}
	})

	t.Run("folded execution id member match", func(t *testing.T) {
		got, err := selectActivityExecutions(activities, "read-stage")
		testutil.FailErr(t, "select folded execution id", err)
		want := `["cmd-activity","read-stage","exact-wins"]`
		if got.ids != want {
			t.Fatalf("selection = %q, want %q", got.ids, want)
		}
		if matched := findActivity(activities, "read-stage"); matched == nil || matched.ID != "cmd-activity" {
			t.Fatalf("findActivity(read-stage) = %+v, want cmd-activity", matched)
		}
	})

	t.Run("exact match with no execution ids", func(t *testing.T) {
		got, err := selectActivityExecutions(activities, "empty-execs")
		testutil.FailErr(t, "select activity with empty execution ids", err)
		if got.ids != "" {
			t.Fatalf("selection = %q, want empty", got.ids)
		}
	})

	t.Run("unknown activity id returns ErrProgramNotFound", func(t *testing.T) {
		_, err := selectActivityExecutions(activities, "unknown-activity")
		if !errors.Is(err, ErrProgramNotFound) {
			t.Fatalf("err = %v, want ErrProgramNotFound", err)
		}
		if matched := findActivity(activities, "unknown-activity"); matched != nil {
			t.Fatalf("findActivity(unknown-activity) = %+v, want nil", matched)
		}
	})
}
