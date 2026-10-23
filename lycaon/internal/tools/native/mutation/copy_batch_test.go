package mutation

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/lycaon/lycaon/internal/sourceeffect"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/toolcontract"
)

type observedCopyJournal struct {
	sourceeffect.Journal
	afterPrepare func(sourceeffect.PendingEffects, []sourceeffect.Plan)
}

func (j observedCopyJournal) PrepareEffects(ctx context.Context, plans []sourceeffect.Plan) (sourceeffect.PendingEffects, error) {
	pending, err := j.Journal.PrepareEffects(ctx, plans)
	if err == nil {
		j.afterPrepare(pending, plans)
	}
	return pending, err
}

func TestRecursiveCopyPageAdmitsIntentBeforeEffectsAndSettlesPartialPublication(t *testing.T) {
	h := newHarness(t, map[string]string{"tree/a": "first", "tree/b": "second", "tree/c": "third"})
	limits, err := toolcontract.CatalogProgramMemoryLimits()
	testutil.FailErr(t, "load memory limits", err)
	memory, err := toolcontract.NewProgramMemory(limits)
	testutil.FailErr(t, "create memory", err)
	h.program.Memory = memory
	var ids []string
	var plans []sourceeffect.Plan
	h.tc.Source.SourceMutations = observedCopyJournal{Journal: h.service, afterPrepare: func(pending sourceeffect.PendingEffects, page []sourceeffect.Plan) {
		ids = pending.IDs()
		plans = append(plans, page...)
		if len(ids) != 3 {
			t.Fatalf("page intents = %d", len(ids))
		}
		for i, id := range ids {
			var status string
			testutil.FailErr(t, "read durable page intent", h.db.QueryRowContext(t.Context(), `SELECT status FROM source_mutations WHERE id=?`, id).Scan(&status))
			if status != "prepared" {
				t.Fatalf("intent before publication = %s", status)
			}
			if _, err := os.Lstat(page[i].Change.AbsPath); !errors.Is(err, os.ErrNotExist) {
				t.Fatalf("destination published before durable intent: %v", err)
			}
		}
		// The first publication succeeds, the second conflicts, and the third was
		// never attempted despite a matching file introduced after preparation.
		testutil.FailErr(t, "race second destination", os.WriteFile(page[1].Change.AbsPath, []byte("racing edit"), 0o644))
		testutil.FailErr(t, "introduce matching unattempted destination", os.WriteFile(page[2].Change.AbsPath, page[2].Record.After, 0o644))
	}}
	r := h.run("", "cp", "-r", "tree", "copy")
	if r.err == nil || len(ids) != 3 {
		t.Fatalf("partial page result=%+v error=%v intents=%v", r.result, r.err, ids)
	}
	for i, id := range ids {
		var count int
		testutil.FailErr(t, "count actual attributed publications", h.db.QueryRowContext(t.Context(), `SELECT count(*) FROM source_operations WHERE operation_key=?`, id).Scan(&count))
		want := 0
		if i == 0 {
			want = 1
		}
		if count != want {
			t.Fatalf("%s attribution=%d want=%d", filepath.Base(plans[i].Change.AbsPath), count, want)
		}
	}
	got, err := os.ReadFile(plans[0].Change.AbsPath)
	testutil.FailErr(t, "read published prefix", err)
	if string(got) != string(plans[0].Record.After) {
		t.Fatalf("first published bytes=%q", got)
	}
	if used := memory.InUse(); used.Working != 0 || used.State != 0 {
		t.Fatalf("copy leaked memory leases: %+v", used)
	}
}
