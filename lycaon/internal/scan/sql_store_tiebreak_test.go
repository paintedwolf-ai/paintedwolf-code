package scan

import (
	"context"
	"github.com/lycaon/lycaon/internal/testdbfixture"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/pkg/api"
	"testing"
	"time"
)

// Equal creation timestamps are ordered by descending row ID.

func newScanStore(t *testing.T) (*SQLStore, context.Context) {
	t.Helper()
	sqlDB := testdbfixture.Open(t, "store.db")
	return NewSQLStore(sqlDB), context.Background()
}

func insertScan(t *testing.T, store *SQLStore, ctx context.Context, rec api.CodeScan) {
	t.Helper()
	testutil.FailErr(t, "Insert "+rec.ID, store.Insert(ctx, rec, nil, ""))
}

func TestSQLStoreListByCanonicalPathTieBreaksByRowidDesc(t *testing.T) {
	store, ctx := newScanStore(t)
	stamp := time.Date(2026, 6, 7, 12, 0, 0, 0, time.UTC)
	cats := []api.ScanCategory{api.ScanCategorySecurity}

	// Equal timestamps isolate the row-ID tie break.
	for _, id := range []string{"a", "b", "c"} {
		insertScan(t, store, ctx, api.CodeScan{
			ID:            "scan-" + id,
			CanonicalPath: "/tmp/p",
			Categories:    cats,
			Status:        api.CodeScanStatusPending,
			CreatedAt:     stamp,
			Trigger:       api.ScanTriggerManual,
		})
	}

	list, err := store.ListByCanonicalPath(ctx, "/tmp/p")
	testutil.FailErr(t, "ListByCanonicalPath", err)
	if len(list) != 3 {
		t.Fatalf("len=%d want 3", len(list))
	}
	if list[0].ID != "scan-c" || list[1].ID != "scan-b" || list[2].ID != "scan-a" {
		t.Fatalf("ListByCanonicalPath order=%v want scan-c, scan-b, scan-a", []string{list[0].ID, list[1].ID, list[2].ID})
	}
}

func TestSQLStoreLatestCompleteForDelegationSnapshotTieBreaks(t *testing.T) {
	store, ctx := newScanStore(t)
	stamp := time.Date(2026, 6, 7, 12, 0, 0, 0, time.UTC)
	cats := []api.ScanCategory{api.ScanCategorySecurity}

	for _, id := range []string{"first", "second"} {
		insertScan(t, store, ctx, api.CodeScan{
			ID:               "scan-" + id,
			CanonicalPath:    "/tmp/p",
			Categories:       cats,
			Status:           api.CodeScanStatusComplete,
			CreatedAt:        stamp,
			DelegationID:     "dep-1",
			HeadSHA:          "head-1",
			SourceSnapshotID: "snapshot-1",
			Trigger:          api.ScanTriggerManual,
		})
	}
	got, err := store.LatestCompleteForDelegationSnapshot(ctx, "dep-1", "snapshot-1", cats)
	testutil.FailErr(t, "LatestCompleteForDelegationSnapshot", err)
	if got == nil || got.ID != "scan-second" {
		got := "<nil>"
		t.Fatalf("LatestCompleteForDelegationSnapshot got=%v want scan-second", got)
	}
}

func TestSQLStoreLatestForDelegationSurvivesMultiTie(t *testing.T) {
	store, ctx := newScanStore(t)
	stamp := time.Date(2026, 6, 7, 12, 0, 0, 0, time.UTC)
	cats := []api.ScanCategory{api.ScanCategorySecurity}

	// Four equal timestamps exercise repeated tie breaks.
	for _, id := range []string{"a", "b", "c", "d"} {
		insertScan(t, store, ctx, api.CodeScan{
			ID:            "scan-" + id,
			CanonicalPath: "/tmp/p",
			Categories:    cats,
			Status:        api.CodeScanStatusPending,
			CreatedAt:     stamp,
			DelegationID:  "dep-1",
			Trigger:       api.ScanTriggerManual,
		})
	}
	got, err := store.LatestForDelegation(ctx, "dep-1", cats)
	testutil.FailErr(t, "LatestForDelegation", err)
	if got.ID != "scan-d" {
		t.Fatalf("LatestForDelegation got=%s want scan-d (last insert wins)", got.ID)
	}
}

func TestSQLStoreListByCanonicalPathMixesCreatedAtAndTieBreak(t *testing.T) {
	// Creation time takes precedence over row ID.
	store, ctx := newScanStore(t)
	earlier := time.Date(2026, 6, 7, 12, 0, 0, 0, time.UTC)
	later := earlier.Add(time.Minute)
	cats := []api.ScanCategory{api.ScanCategorySecurity}

	insertScan(t, store, ctx, api.CodeScan{
		ID:            "scan-old-1",
		CanonicalPath: "/tmp/p",
		Categories:    cats,
		Status:        api.CodeScanStatusPending,
		CreatedAt:     earlier,
		Trigger:       api.ScanTriggerManual,
	})
	insertScan(t, store, ctx, api.CodeScan{
		ID:            "scan-new-1",
		CanonicalPath: "/tmp/p",
		Categories:    cats,
		Status:        api.CodeScanStatusPending,
		CreatedAt:     later,
		Trigger:       api.ScanTriggerManual,
	})
	insertScan(t, store, ctx, api.CodeScan{
		ID:            "scan-new-2",
		CanonicalPath: "/tmp/p",
		Categories:    cats,
		Status:        api.CodeScanStatusPending,
		CreatedAt:     later,
		Trigger:       api.ScanTriggerManual,
	})

	list, err := store.ListByCanonicalPath(ctx, "/tmp/p")
	testutil.FailErr(t, "ListByCanonicalPath", err)
	if len(list) != 3 {
		t.Fatalf("len=%d want 3", len(list))
	}
	if list[0].ID != "scan-new-2" || list[1].ID != "scan-new-1" || list[2].ID != "scan-old-1" {
		t.Fatalf("order=%v want scan-new-2, scan-new-1, scan-old-1", []string{list[0].ID, list[1].ID, list[2].ID})
	}
}

func TestSQLStoreOpenScansForPathOrdersByCreatedAtAscending(t *testing.T) {
	store, ctx := newScanStore(t)
	earlier := time.Date(2026, 6, 7, 12, 0, 0, 0, time.UTC)
	later := earlier.Add(time.Minute)

	insertScan(t, store, ctx, api.CodeScan{
		ID:            "scan-later",
		CanonicalPath: "/tmp/p",
		Status:        api.CodeScanStatusPending,
		CreatedAt:     later,
		Trigger:       api.ScanTriggerWriteBurst,
	})
	insertScan(t, store, ctx, api.CodeScan{
		ID:            "scan-earlier",
		CanonicalPath: "/tmp/p",
		Status:        api.CodeScanStatusPending,
		CreatedAt:     earlier,
		Trigger:       api.ScanTriggerWriteBurst,
	})
	list, err := store.OpenScansForPath(ctx, "/tmp/p")
	testutil.FailErr(t, "OpenScansForPath", err)
	if len(list) != 2 || list[0].ID != "scan-earlier" || list[1].ID != "scan-later" {
		t.Fatalf("order=%v want scan-earlier, scan-later (ASC)", []string{list[0].ID, list[1].ID})
	}
}
