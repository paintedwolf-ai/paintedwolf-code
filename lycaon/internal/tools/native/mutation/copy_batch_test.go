package mutation

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
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

func TestCopyAdmissionRefusesBeforeUnadmittedStorageOrPublication(t *testing.T) {
	for _, tc := range []struct {
		name   string
		limits toolcontract.ProgramMemoryLimits
		argv   []string
	}{
		{"result", toolcontract.ProgramMemoryLimits{Working: 32 << 20, State: 1}, []string{"cp", "source.txt", "target"}},
		{"traversal", toolcontract.ProgramMemoryLimits{Working: 32 << 20, State: 1}, []string{"cp", "-r", "tree", "target"}},
		{"stream", toolcontract.ProgramMemoryLimits{Working: 1, State: 64 << 20}, []string{"cp", "source.txt", "target"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h := newHarness(t, map[string]string{"source.txt": "data", "tree/a": "first"})
			memory, err := toolcontract.NewProgramMemory(tc.limits)
			testutil.FailErr(t, "create constrained program memory", err)
			h.program.Memory = memory
			result := h.run("", tc.argv...)
			var limit *toolcontract.ProgramMemoryLimitError
			if !errors.As(result.err, &limit) {
				t.Fatalf("admission outcome=%+v error=%v", result.result, result.err)
			}
			if !h.absent("target") {
				t.Fatal("unadmitted copy published a destination")
			}
			if usage := memory.InUse(); usage.Working != 0 || usage.State != 0 {
				t.Fatalf("refused copy leaked admissions: %+v", usage)
			}
		})
	}
}

func TestRecursiveCopyFlushesRetainedBodiesBeforeTheNextLargeFile(t *testing.T) {
	payload := strings.Repeat("x", 2<<20)
	h := newHarness(t, map[string]string{"tree/a": payload, "tree/b": payload, "tree/c": payload, "tree/d": payload, "tree/e": payload})
	limits, err := toolcontract.CatalogProgramMemoryLimits()
	testutil.FailErr(t, "load program memory", err)
	memory, err := toolcontract.NewProgramMemory(limits)
	testutil.FailErr(t, "create program memory", err)
	h.program.Memory = memory
	result := h.run("", "cp", "-r", "tree", "copy")
	if result.err != nil || result.result.Status != 0 {
		t.Fatalf("large copy=%+v error=%v", result.result, result.err)
	}
	for _, name := range []string{"a", "b", "c", "d", "e"} {
		if got := h.read("copy/" + name); got != payload {
			t.Fatalf("large file %s changed", name)
		}
	}
	if usage := memory.InUse(); usage.State != 0 || usage.Working != 0 {
		t.Fatalf("large copy leaked admission=%+v", usage)
	}
}
