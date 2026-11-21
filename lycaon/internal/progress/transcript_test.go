package progress

import (
	"fmt"
	"strings"
	"testing"

	wire "github.com/lycaon/lycaon/pkg/api"
)

func TestBuildProgressUpdate(t *testing.T) {
	t.Run("initial 0 to N carries the full plan and its counts", func(t *testing.T) {
		before, after := snapshotPair("", "- [ ] a\n- [ ] b\n")
		meta, ok := buildProgressUpdate(before, after)
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
		if meta.Summary == nil || meta.Summary.TotalSteps != 2 || meta.Summary.Pending != 2 || meta.Summary.Done != 0 {
			t.Errorf("Summary = %+v, want two pending of two", meta.Summary)
		}
	})

	t.Run("mid-run delta carries its changes and the plan counts", func(t *testing.T) {
		before, after := snapshotPair("- [ ] a\n- [ ] b\n", "- [x] a\n- [ ] b\n")
		meta, ok := buildProgressUpdate(before, after)
		if !ok {
			t.Fatal("expected an update")
		}
		if meta.Initial {
			t.Error("expected Initial=false mid-run")
		}
		if len(meta.Changes) != 1 || meta.Changes[0].Kind != wire.ProgressChangeDone {
			t.Errorf("Changes = %+v, want one done", meta.Changes)
		}
		if meta.Summary == nil || meta.Summary.ChangeCount != 1 || meta.Summary.TotalSteps != 2 || meta.Summary.Done != 1 || meta.Summary.Pending != 1 {
			t.Errorf("Summary = %+v, want one change leaving one done of two", meta.Summary)
		}
	})

	t.Run("large initial plan carries only its counts", func(t *testing.T) {
		latest := checklistRows(MaxDetailedProgressUpdateRows + 1)
		before, after := snapshotPair("", latest)
		meta, ok := buildProgressUpdate(before, after)
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

	t.Run("large mid-run delta carries only the plan counts", func(t *testing.T) {
		const changed = MaxDetailedProgressUpdateRows + 2
		baseline := checklistRows(changed + 1)
		latest := strings.Replace(baseline, "- [ ]", "- [x]", changed)
		before, after := snapshotPair(baseline, latest)
		meta, ok := buildProgressUpdate(before, after)
		if !ok {
			t.Fatal("expected an update")
		}
		if meta.Initial || meta.Summary == nil {
			t.Fatalf("meta = %+v, want delta summary", meta)
		}
		if meta.Summary.ChangeCount != changed || meta.Summary.Done != changed || meta.Summary.Pending != 1 {
			t.Fatalf("Summary = %+v, want 14 completed and one pending row", meta.Summary)
		}
		if len(meta.Changes) != 0 {
			t.Fatalf("large delta listed %d changes", len(meta.Changes))
		}
	})

	t.Run("completing write is superseded by completion bar", func(t *testing.T) {
		before, after := snapshotPair("- [ ] a\n- [ ] b\n", "- [x] a\n- [x] b\n")
		if _, ok := buildProgressUpdate(before, after); ok {
			t.Error("completing window should not emit a progress_update")
		}
	})

	t.Run("no change is silent", func(t *testing.T) {
		before, after := snapshotPair("- [ ] a\n", "- [ ] a\n")
		if _, ok := buildProgressUpdate(before, after); ok {
			t.Error("no-op write should not emit")
		}
	})

	t.Run("already-complete baseline still emits non-completing delta", func(t *testing.T) {
		// Baseline already terminal, latest adds a new pending step: not a completing edge.
		before, after := snapshotPair("- [x] a\n", "- [x] a\n- [ ] b\n")
		meta, ok := buildProgressUpdate(before, after)
		if !ok {
			t.Fatal("expected an update when a new step is added to a done plan")
		}
		if len(meta.Changes) != 1 || meta.Changes[0].Kind != wire.ProgressChangeCreated {
			t.Errorf("Changes = %+v, want one created", meta.Changes)
		}
	})
}

func snapshotPair(beforeContent, afterContent string) (Snapshot, Snapshot) {
	beforeCount := len(DeriveProgress(beforeContent, DefaultProgressCap).Items)
	afterCount := len(DeriveProgress(afterContent, DefaultProgressCap).Items)
	beforeIDs := make([]string, beforeCount)
	for i := range beforeIDs {
		beforeIDs[i] = fmt.Sprintf("item-%d", i+1)
	}
	afterIDs := make([]string, afterCount)
	for i := range afterIDs {
		afterIDs[i] = fmt.Sprintf("item-%d", i+1)
	}
	return Snapshot{Content: beforeContent, ItemIDs: beforeIDs},
		Snapshot{Content: afterContent, ItemIDs: afterIDs}
}

func checklistRows(count int) string {
	var rows strings.Builder
	for i := 0; i < count; i++ {
		fmt.Fprintf(&rows, "- [ ] step %d\n", i+1)
	}
	return rows.String()
}
