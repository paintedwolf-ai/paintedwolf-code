package progress

import (
	"fmt"
	"testing"

	wire "github.com/lycaon/lycaon/pkg/api"
)

func TestBuildProgressUpdate(t *testing.T) {
	t.Run("initial 0 to N carries the full plan and its counts", func(t *testing.T) {
		before := NewSnapshot("")
		after := NewSnapshot("",
			Row{ID: "p1", State: ProgressStatePending, Label: "a"},
			Row{ID: "p2", State: ProgressStatePending, Label: "b"},
		)
		meta, ok := buildProgressUpdate(before, after)
		if !ok {
			t.Fatal("expected an update")
		}
		if !meta.Initial || len(meta.Steps) != 2 || len(meta.Changes) != 0 {
			t.Fatalf("meta = %+v", meta)
		}
		if meta.Summary == nil || meta.Summary.TotalSteps != 2 || meta.Summary.Pending != 2 || meta.Summary.Done != 0 {
			t.Errorf("Summary = %+v", meta.Summary)
		}
	})

	t.Run("mid-run delta carries its changes and the plan counts", func(t *testing.T) {
		before := NewSnapshot("",
			Row{ID: "p1", State: ProgressStatePending, Label: "a"},
			Row{ID: "p2", State: ProgressStatePending, Label: "b"},
		)
		after := NewSnapshot("",
			Row{ID: "p1", State: ProgressStateDone, Label: "a"},
			Row{ID: "p2", State: ProgressStatePending, Label: "b"},
		)
		meta, ok := buildProgressUpdate(before, after)
		if !ok {
			t.Fatal("expected an update")
		}
		if meta.Initial || len(meta.Changes) != 1 || meta.Changes[0].Kind != wire.ProgressChangeDone {
			t.Fatalf("meta = %+v", meta)
		}
		if meta.Summary == nil || meta.Summary.ChangeCount != 1 || meta.Summary.TotalSteps != 2 || meta.Summary.Done != 1 || meta.Summary.Pending != 1 {
			t.Errorf("Summary = %+v", meta.Summary)
		}
	})

	t.Run("large initial plan carries only its counts", func(t *testing.T) {
		before := NewSnapshot("")
		after := NewSnapshot("", numberedRows(MaxDetailedProgressUpdateRows+1, 0)...)
		meta, ok := buildProgressUpdate(before, after)
		if !ok || !meta.Initial || meta.Summary == nil {
			t.Fatalf("meta = %+v", meta)
		}
		if meta.Summary.ChangeCount != 13 || meta.Summary.TotalSteps != 13 || meta.Summary.Pending != 13 {
			t.Fatalf("Summary = %+v", meta.Summary)
		}
		if len(meta.Steps) != 0 || len(meta.Changes) != 0 {
			t.Fatalf("summary update carried details: steps=%d changes=%d", len(meta.Steps), len(meta.Changes))
		}
	})

	t.Run("large mid-run delta carries only the plan counts", func(t *testing.T) {
		const changed = MaxDetailedProgressUpdateRows + 2
		before := NewSnapshot("", numberedRows(changed+1, 0)...)
		after := NewSnapshot("", numberedRows(changed+1, changed)...)
		meta, ok := buildProgressUpdate(before, after)
		if !ok || meta.Initial || meta.Summary == nil {
			t.Fatalf("meta = %+v", meta)
		}
		if meta.Summary.ChangeCount != changed || meta.Summary.Done != changed || meta.Summary.Pending != 1 {
			t.Fatalf("Summary = %+v", meta.Summary)
		}
		if len(meta.Changes) != 0 {
			t.Fatalf("large delta listed %d changes", len(meta.Changes))
		}
	})

	t.Run("completing write is superseded by completion bar", func(t *testing.T) {
		before := NewSnapshot("", Row{ID: "p1", State: ProgressStatePending, Label: "a"})
		after := NewSnapshot("", Row{ID: "p1", State: ProgressStateDone, Label: "a"})
		if _, ok := buildProgressUpdate(before, after); ok {
			t.Error("completing window should not emit a progress_update")
		}
	})

	t.Run("no change is silent", func(t *testing.T) {
		before := NewSnapshot("", Row{ID: "p1", State: ProgressStatePending, Label: "a"})
		if _, ok := buildProgressUpdate(before, before); ok {
			t.Error("no-op write should not emit")
		}
	})

	t.Run("already-complete baseline still emits non-completing delta", func(t *testing.T) {
		before := NewSnapshot("", Row{ID: "p1", State: ProgressStateDone, Label: "a"})
		after := NewSnapshot("",
			Row{ID: "p1", State: ProgressStateDone, Label: "a"},
			Row{ID: "p2", State: ProgressStatePending, Label: "b"},
		)
		meta, ok := buildProgressUpdate(before, after)
		if !ok || len(meta.Changes) != 1 || meta.Changes[0].Kind != wire.ProgressChangeCreated {
			t.Fatalf("meta = %+v", meta)
		}
	})
}

func numberedRows(total, doneCount int) []Row {
	out := make([]Row, total)
	for i := range out {
		state := ProgressStatePending
		if i < doneCount {
			state = ProgressStateDone
		}
		out[i] = Row{ID: fmt.Sprintf("p%d", i+1), State: state, Label: fmt.Sprintf("step %d", i+1)}
	}
	return out
}
