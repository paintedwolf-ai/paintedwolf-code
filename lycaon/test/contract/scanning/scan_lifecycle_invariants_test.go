package contract

import (
	"context"
	"slices"
	"testing"
	"time"

	"github.com/lycaon/lycaon/internal/db"
	"github.com/lycaon/lycaon/internal/project"
	"github.com/lycaon/lycaon/internal/scan"
	"github.com/lycaon/lycaon/internal/testdbfixture"
	"github.com/lycaon/lycaon/internal/testutil/scantest"
	"github.com/lycaon/lycaon/internal/timelayout"
	"github.com/lycaon/lycaon/pkg/api"
	contractcheck "github.com/lycaon/lycaon/test/contract/internal/check"
)

// I12: visible scans are exactly the current roots' scans; scan-once dedup;
// detaching a root hides its scans and retains their rows.
func TestMultiRootScanLifecycleInvariantI12(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	sqlDB := testdbfixture.Open(t, "i12.db")
	projects := project.NewSQLRegistry(sqlDB)
	proj, err := projects.Create(ctx, project.CreateParams{
		Roots: []project.AttachRootParams{{Path: t.TempDir()}, {Path: t.TempDir()}},
	})
	contractcheck.FailErr(t, "create two-root project", err)
	if len(proj.Roots) != 2 {
		t.Fatalf("project roots = %+v, want two", proj.Roots)
	}
	primary, secondary := proj.Roots[0].Path, proj.Roots[1].Path

	store := scan.NewSQLStore(sqlDB)
	coord := scantest.Coordinator(t, store, nil)
	insert := func(path, id, scanner string) {
		t.Helper()
		if err := store.Insert(ctx, api.CodeScan{
			ID:            id,
			CanonicalPath: path,
			Categories:    []api.ScanCategory{api.ScanCategorySecurity},
			ScannerID:     scanner,
			Status:        api.CodeScanStatusComplete,
			CreatedAt:     time.Now().UTC(),
			HeadSHA:       "head",
		}, nil, ""); err != nil {
			contractcheck.FailErr(t, "insert scan", err)
		}
	}
	insert(primary, "s1", "lycaon-sast")
	insert(secondary, "s2", "lycaon-sast")
	insert(secondary, "s2-dup", "lycaon-secrets")
	insert(t.TempDir(), "outside", "lycaon-sast")

	visible, err := coord.List(ctx, project.RootPaths(proj), 50)
	contractcheck.FailErr(t, "List visible", err)
	if got, want := scanIDs(visible), []string{"s1", "s2", "s2-dup"}; !slices.Equal(got, want) {
		t.Fatalf("visible scans = %v, want %v", got, want)
	}

	first, err := coord.Enqueue(ctx, scan.EnqueueRequest{
		ProjectDir: primary,
		Categories: []api.ScanCategory{api.ScanCategorySecurity},
		ScannerID:  "lycaon-sca",
		HeadSHA:    "head",
	})
	contractcheck.FailErr(t, "enqueue first", err)
	second, err := coord.Enqueue(ctx, scan.EnqueueRequest{
		ProjectDir: primary,
		Categories: []api.ScanCategory{api.ScanCategorySCA},
		ScannerID:  "lycaon-sca",
		HeadSHA:    "head",
	})
	contractcheck.FailErr(t, "enqueue second", err)
	if first.ID != second.ID {
		t.Fatalf("scan-once: %s vs %s", first.ID, second.ID)
	}

	change, err := projects.DetachRoot(ctx, proj.ID, proj.Roots[1].ID)
	contractcheck.FailErr(t, "detach secondary root", err)
	afterDetach, err := coord.List(ctx, project.RootPaths(change.After), 50)
	contractcheck.FailErr(t, "List after detach", err)
	want := sortedIDs([]string{"s1", first.ID})
	want = slices.Compact(want)
	if got := scanIDs(afterDetach); !slices.Equal(got, want) {
		t.Fatalf("scans after detach = %v, want %v", got, want)
	}
	for _, id := range []string{"s2", "s2-dup"} {
		retained, err := store.Get(ctx, id)
		contractcheck.FailErr(t, "Get retained "+id, err)
		if retained == nil || retained.CanonicalPath != secondary {
			t.Fatalf("detach must retain scan row %s, got %+v", id, retained)
		}
	}
}

func scanIDs(scans []api.CodeScan) []string {
	ids := make([]string, 0, len(scans))
	for _, s := range scans {
		ids = append(ids, s.ID)
	}
	return sortedIDs(ids)
}

func sortedIDs(ids []string) []string {
	out := slices.Clone(ids)
	slices.Sort(out)
	return out
}

func TestScanRetentionDefaultKeepsOrphanedRows(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	sqlDB := testdbfixture.Open(t, "retain-policy.db")

	orphanPath := t.TempDir()
	ts := timelayout.Format(time.Now().UTC().Add(-40 * 24 * time.Hour))
	_, err := sqlDB.ExecContext(ctx, `
		INSERT INTO code_scans (
			id, canonical_path, categories_json, scanner_id, status, created_at, completed_at, trigger, reuse_key
		) VALUES ('orphan-scan', ?, '["security"]', 'lycaon-sast', 'complete', ?, ?, 'project_open', 'orphan-scan')
	`, orphanPath, ts, ts)
	contractcheck.FailErr(t, "insert orphan scan", err)

	cfg := db.DefaultRetention()
	cfg.IncrementalVacuumMinFreelistPages = 9999
	_, err = db.RunRetention(ctx, sqlDB, cfg)
	contractcheck.FailErr(t, "RunRetention", err)
	var count int
	err = sqlDB.QueryRowContext(ctx, `SELECT COUNT(*) FROM code_scans WHERE id = 'orphan-scan'`).Scan(&count)
	contractcheck.FailErr(t, "count retained scan", err)
	if count != 1 {
		t.Fatal("retention removed durable scan history")
	}
}
