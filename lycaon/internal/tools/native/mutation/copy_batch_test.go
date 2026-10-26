package mutation

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/sourceeffect"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/toolcontract"
	"github.com/lycaon/lycaon/internal/tools/native"
)

type observedCopyJournal struct {
	sourceeffect.Journal
	afterPrepare func(sourceeffect.PendingEffects, []sourceeffect.Plan)
}

func (j observedCopyJournal) PrepareEffects(ctx context.Context, memory sourceeffect.Memory, plans []sourceeffect.Plan) (sourceeffect.PendingEffects, error) {
	pending, err := j.Journal.PrepareEffects(ctx, memory, plans)
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
	h.owners.Revisions.Memory = memory
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
			h.owners.Revisions.Memory = memory
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
	h.owners.Revisions.Memory = memory
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

func TestCopyDirectoryCreationRemainsAnEffectAfterMemoryRefusal(t *testing.T) {
	h := newHarness(t, map[string]string{"tree/": ""})
	memory, err := toolcontract.NewProgramMemory(toolcontract.ProgramMemoryLimits{Working: 1 << 20, State: int64(2*128*(2048+len("tree")+len(".")) + 100)})
	testutil.FailErr(t, "create constrained memory", err)
	h.owners.Revisions.Memory = memory
	r := h.run("", "cp", "-r", "tree", "target")
	var limit *toolcontract.ProgramMemoryLimitError
	if !errors.As(r.err, &limit) || r.result.EffectCertainty != toolcontract.EffectCompleted {
		t.Fatalf("post-mkdir refusal lost effect: result=%+v error=%v", r.result, r.err)
	}
	info, err := os.Stat(h.path("target"))
	testutil.FailErr(t, "inspect actual created directory", err)
	if !info.IsDir() {
		t.Fatal("target is not a directory")
	}
	if use := memory.InUse(); use.State != 0 || use.Working != 0 {
		t.Fatalf("admission leaked: %+v", use)
	}
}

func TestCopyEmptyTreeReportsActualDirectoryEffect(t *testing.T) {
	h := newHarness(t, map[string]string{"tree/empty/": ""})
	r := h.run("", "cp", "-r", "tree", "target")
	if r.err != nil || r.result.Status != 0 || r.result.EffectCertainty != toolcontract.EffectCompleted {
		t.Fatalf("empty tree copy result=%+v error=%v", r.result, r.err)
	}
	info, err := os.Stat(h.path("target/empty"))
	testutil.FailErr(t, "inspect copied empty directory", err)
	if !info.IsDir() {
		t.Fatal("empty directory missing")
	}
}

type failedCopySettlement struct{ sourceeffect.Journal }
type failedCopyPending struct{ sourceeffect.Pending }

func (j failedCopySettlement) PrepareEffect(ctx context.Context, plan sourceeffect.Plan) (sourceeffect.Pending, error) {
	pending, err := j.Journal.PrepareEffect(ctx, plan)
	if err != nil {
		return nil, err
	}
	return failedCopyPending{pending}, nil
}
func (p failedCopyPending) Finish(ctx context.Context, err error) error {
	return errors.Join(p.Pending.Finish(ctx, err), errors.New("settlement failure"))
}

func TestCopyScalarPreservesCommittedEffectOnSettlementFailure(t *testing.T) {
	for _, recursive := range []bool{false, true} {
		t.Run(map[bool]string{false: "scalar", true: "existing_tree_file"}[recursive], func(t *testing.T) {
			h := newHarness(t, map[string]string{"source": "new bytes", "tree/a": "new bytes", "target": "old", "dest/tree/a": "old"})
			h.tc.Source.SourceMutations = failedCopySettlement{h.service}
			args := []string{"cp", "source", "target"}
			path := "target"
			if recursive {
				args = []string{"cp", "-r", "tree", "dest"}
				path = "dest/tree/a"
			}
			r := h.run("", args...)
			if r.err == nil || r.result.EffectCertainty != toolcontract.EffectCompleted || h.read(path) != "new bytes" {
				t.Fatalf("committed copy result=%+v error=%v", r.result, r.err)
			}
		})
	}
}

func TestMkdirPreservesObservedCreationAfterSettlementFailure(t *testing.T) {
	h := newHarness(t, nil)
	h.tc.Source.SourceMutations = failedCopySettlement{h.service}
	r := h.run("", "mkdir", "-p", "new/child")
	if r.err == nil || r.result.EffectCertainty != toolcontract.EffectCompleted {
		t.Fatalf("created directory lost on settlement failure: %+v error=%v", r.result, r.err)
	}
	info, err := os.Stat(h.path("new/child"))
	testutil.FailErr(t, "inspect created directory", err)
	if !info.IsDir() {
		t.Fatal("directory missing")
	}
}

func TestRecursiveCopyCancellationSettlesEarlierPublicationPages(t *testing.T) {
	files := make(map[string]string, 70)
	for i := range 70 {
		files[fmt.Sprintf("tree/f%03d", i)] = "original"
	}
	h := newHarness(t, files)
	memory := h.owners.Revisions.Memory
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	var pages [][]string
	h.tc.Source.SourceMutations = observedCopyJournal{Journal: h.service, afterPrepare: func(pending sourceeffect.PendingEffects, _ []sourceeffect.Plan) {
		pages = append(pages, pending.IDs())
		if len(pages) == 2 {
			cancel()
		}
	}}
	op, err := h.owners.Operations.Copy.Begin(native.CopyInput{Recursive: true}, memory)
	testutil.FailErr(t, "begin actual shared copy", err)
	result, err := op.CopyPair(ctx, native.FilePair{From: h.path("tree"), To: h.path("copy")}, h.tc)
	op.Close()
	if !errors.Is(err, context.Canceled) || len(pages) != 2 || len(pages[0]) != 64 || len(pages[1]) != 6 || result.Effects != 65 {
		t.Fatalf("canceled page result=%+v err=%v pages=%v", result, err, pages)
	}
	for page, ids := range pages {
		for _, id := range ids {
			var status string
			var count int
			testutil.FailErr(t, "read settled publication", h.db.QueryRowContext(t.Context(), `SELECT status FROM source_mutations WHERE id=?`, id).Scan(&status))
			testutil.FailErr(t, "count definite attribution", h.db.QueryRowContext(t.Context(), `SELECT count(*) FROM source_operations WHERE operation_key=?`, id).Scan(&count))
			wantStatus, wantCount := "committed", 1
			if page == 1 {
				wantStatus, wantCount = "failed", 0
			}
			if status != wantStatus || count != wantCount {
				t.Fatalf("page=%d journal=%s attributed=%d", page, status, count)
			}
		}
	}
	entries, err := os.ReadDir(h.path("copy"))
	testutil.FailErr(t, "read actual partial destination", err)
	if len(entries) != 64 || len(result.Copied) != 64 {
		t.Fatalf("actual files=%d copied receipts=%d", len(entries), len(result.Copied))
	}
	for _, entry := range entries {
		if got := h.read("copy/" + entry.Name()); got != "original" {
			t.Fatalf("partial publication bytes=%q", got)
		}
	}
	if used := memory.InUse(); used != (toolcontract.ProgramMemoryUsage{}) {
		t.Fatalf("canceled copy retained memory: %+v", used)
	}
}
