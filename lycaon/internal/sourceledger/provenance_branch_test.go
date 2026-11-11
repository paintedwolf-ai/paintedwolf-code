package sourceledger

import (
	"context"
	"fmt"
	"testing"

	"github.com/lycaon/lycaon/internal/sourcebranch"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/pkg/api"
)

const branchTestWorker = sourcebranch.ID("job-1")

// recordBranchEffects writes one effect per entry, in order, on a single file.
func recordBranchEffects(t *testing.T, store *Store, ctx context.Context, branches ...sourcebranch.ID) string {
	t.Helper()
	content := map[sourcebranch.ID]string{}
	for i, branch := range branches {
		before, ok := content[branch]
		if !ok {
			before = content[sourcebranch.Trunk]
		}
		after := fmt.Sprintf("%s %d\n", branch, i)
		op, origin, job := api.SourceChangeOpWrite, api.SourceChangeOriginUser, ""
		if branch.IsWorker() {
			origin, job = api.SourceChangeOriginAgent, branch.String()
		} else if before == "" {
			op = api.SourceChangeOpCreate
		}
		mustRecord(t, store, ctx, RecordInput{
			RecordLocation: RecordLocation{RootID: "r1", Path: "a.txt"},
			ProjectID:      "p1", BranchID: branch, JobID: job,
			Op: op, Origin: origin,
			Before: []byte(before), After: []byte(after)})
		content[branch] = after
	}
	fileID, _ := mustResolve(t, store, ctx, "a.txt")
	return fileID
}

func assertBranchPage(t *testing.T, page FileEffectsResult, branch sourcebranch.ID, rows int, wantCursor bool) {
	t.Helper()
	if len(page.Effects) != rows || (page.NextBeforeOrdinal != 0) != wantCursor {
		t.Fatalf("page = %d rows cursor %d, want %d rows cursor=%v", len(page.Effects), page.NextBeforeOrdinal, rows, wantCursor)
	}
	for _, effect := range page.Effects {
		if effect.BranchID != branch {
			t.Fatalf("effect on %q listed under %q", effect.BranchID, branch)
		}
	}
}

func TestQueryFileEffectsPagesOneBranch(t *testing.T) {
	store, ctx := openLedger(t)
	trunk, worker := sourcebranch.Trunk, branchTestWorker
	fileID := recordBranchEffects(t, store, ctx, trunk, worker, trunk, worker, worker, trunk)

	first, err := store.History.QueryFileEffects(ctx, "p1", trunk, fileID, 0, 0, 2)
	testutil.FailErr(t, "first trunk page", err)
	assertBranchPage(t, first, trunk, 2, true)
	rest, err := store.History.QueryFileEffects(ctx, "p1", trunk, fileID, 0, first.NextBeforeOrdinal, 2)
	testutil.FailErr(t, "last trunk page", err)
	assertBranchPage(t, rest, trunk, 1, false)

	// Older trunk rows lie beneath the worker's oldest row; they earn no cursor.
	own, err := store.History.QueryFileEffects(ctx, "p1", worker, fileID, 0, 0, 3)
	testutil.FailErr(t, "worker page", err)
	assertBranchPage(t, own, worker, 3, false)

	absent, err := store.History.QueryFileEffects(ctx, "p1", sourcebranch.ID("job-2"), fileID, 0, 0, 3)
	testutil.FailErr(t, "unrelated branch page", err)
	assertBranchPage(t, absent, sourcebranch.ID("job-2"), 0, false)
}

func TestLatestFileEffectStaysOnBranch(t *testing.T) {
	store, ctx := openLedger(t)
	trunk, worker := sourcebranch.Trunk, branchTestWorker
	fileID := recordBranchEffects(t, store, ctx, trunk, worker)

	all, err := recentEffects(ctx, store, 0, 0, 10)
	testutil.FailErr(t, "list effects", err)
	if len(all) != 2 || all[0].BranchID != worker || all[1].BranchID != trunk {
		t.Fatalf("effects = %+v, want the worker's newest over trunk's", all)
	}
	latest, found, err := store.History.LatestFileEffect(ctx, "p1", trunk, fileID)
	testutil.FailErr(t, "latest trunk effect", err)
	if !found || latest.Ordinal != all[1].Ordinal {
		t.Fatalf("trunk latest = (%v, %d), want ordinal %d", found, latest.Ordinal, all[1].Ordinal)
	}
	latest, found, err = store.History.LatestFileEffect(ctx, "p1", worker, fileID)
	testutil.FailErr(t, "latest worker effect", err)
	if !found || latest.Ordinal != all[0].Ordinal {
		t.Fatalf("worker latest = (%v, %d), want ordinal %d", found, latest.Ordinal, all[0].Ordinal)
	}
	if _, found, err := store.History.LatestFileEffect(ctx, "p1", sourcebranch.ID("job-2"), fileID); err != nil || found {
		t.Fatalf("unrelated branch latest = (%v, %v), want absent without error", found, err)
	}
}
