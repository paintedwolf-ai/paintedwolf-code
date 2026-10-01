package app

import (
	"fmt"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/progress"
	wire "github.com/lycaon/lycaon/pkg/api"
)

func TestBuildProgressUpdate(t *testing.T) {
	t.Run("initial 0 to N carries full plan", func(t *testing.T) {
		meta, ok := buildProgressUpdate("", "- [ ] a\n- [ ] b\n", 1)
		if !ok {
			t.Fatal("expected an update")
		}
		if !meta.Initial {
			t.Error("expected Initial=true on 0 → N")
		}
		if len(meta.Steps) != 2 {
			t.Errorf("Steps len = %d, want 2", len(meta.Steps))
		}
		if len(meta.Changes) != 0 {
			t.Errorf("Changes should be empty on initial, got %d", len(meta.Changes))
		}
	})

	t.Run("mid-run delta carries changes only", func(t *testing.T) {
		meta, ok := buildProgressUpdate("- [ ] a\n- [ ] b\n", "- [x] a\n- [ ] b\n", 2)
		if !ok {
			t.Fatal("expected an update")
		}
		if meta.Initial {
			t.Error("expected Initial=false mid-run")
		}
		if len(meta.Changes) != 1 || meta.Changes[0].Kind != wire.ProgressChangeDone {
			t.Errorf("Changes = %+v, want one done", meta.Changes)
		}
	})

	t.Run("large initial plan carries summary", func(t *testing.T) {
		latest := checklistRows(progress.MaxDetailedProgressUpdateRows + 1)
		meta, ok := buildProgressUpdate("", latest, 2)
		if !ok {
			t.Fatal("expected an update")
		}
		if !meta.Initial || meta.Summary == nil {
			t.Fatalf("meta = %+v, want initial summary", meta)
		}
		if meta.Summary.ChangeCount != 13 || meta.Summary.TotalSteps != 13 || meta.Summary.Pending != 13 {
			t.Fatalf("Summary = %+v, want 13 created pending rows", meta.Summary)
		}
		if len(meta.Steps) != 0 || len(meta.Changes) != 0 {
			t.Fatalf("summary update carried details: steps=%d changes=%d", len(meta.Steps), len(meta.Changes))
		}
	})

	t.Run("large mid-run delta carries summary", func(t *testing.T) {
		const changed = progress.MaxDetailedProgressUpdateRows + 2
		baseline := checklistRows(changed + 1)
		latest := strings.Replace(baseline, "- [ ]", "- [x]", changed)
		meta, ok := buildProgressUpdate(baseline, latest, 3)
		if !ok {
			t.Fatal("expected an update")
		}
		if meta.Initial || meta.Summary == nil {
			t.Fatalf("meta = %+v, want delta summary", meta)
		}
		if meta.Summary.ChangeCount != changed || meta.Summary.Done != changed || meta.Summary.Pending != 1 {
			t.Fatalf("Summary = %+v, want 14 completed and one pending row", meta.Summary)
		}
	})

	t.Run("completing write is superseded by completion bar", func(t *testing.T) {
		if _, ok := buildProgressUpdate("- [ ] a\n- [ ] b\n", "- [x] a\n- [x] b\n", 3); ok {
			t.Error("completing window should not emit a progress_update")
		}
	})

	t.Run("no change is silent", func(t *testing.T) {
		if _, ok := buildProgressUpdate("- [ ] a\n", "- [ ] a\n", 4); ok {
			t.Error("no-op write should not emit")
		}
	})

	t.Run("already-complete baseline still emits non-completing delta", func(t *testing.T) {
		// Baseline already terminal, latest adds a new pending step: not a completing edge.
		meta, ok := buildProgressUpdate("- [x] a\n", "- [x] a\n- [ ] b\n", 5)
		if !ok {
			t.Fatal("expected an update when a new step is added to a done plan")
		}
		if len(meta.Changes) != 1 || meta.Changes[0].Kind != wire.ProgressChangeCreated {
			t.Errorf("Changes = %+v, want one created", meta.Changes)
		}
	})
}

func checklistRows(count int) string {
	var rows strings.Builder
	for i := 0; i < count; i++ {
		fmt.Fprintf(&rows, "- [ ] step %d\n", i+1)
	}
	return rows.String()
}
