package contract

import (
	"context"
	"testing"
	"time"

	"github.com/lycaon/lycaon/internal/db"
	"github.com/lycaon/lycaon/internal/project"
	"github.com/lycaon/lycaon/internal/scan"
	"github.com/lycaon/lycaon/internal/testdbfixture"
	"github.com/lycaon/lycaon/internal/testutil/scantest"
	"github.com/lycaon/lycaon/pkg/api"
	contractcheck "github.com/lycaon/lycaon/test/contract/internal/check"
)

// I12: visible scans ⊆ current roots' canonical paths; scan-once dedup; detach retains rows.
func TestMultiRootScanLifecycleInvariantI12(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	sqlDB := testdbfixture.Open(t, "i12.db")

	primary := t.TempDir()
	secondary := t.TempDir()
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

	roots := []string{primary, secondary}
	visible, err := coord.List(ctx, roots, 50)
	contractcheck.FailErr(t, "List visible", err)
	for _, s := range visible {
		if !pathInSet(s.CanonicalPath, roots) {
			t.Fatalf("visible scan %q path %q not in roots %v", s.ID, s.CanonicalPath, roots)
		}
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

	detachedRoots := []string{primary}
	afterDetach, err := coord.List(ctx, detachedRoots, 50)
	contractcheck.FailErr(t, "List after detach", err)
	for _, s := range afterDetach {
		if s.CanonicalPath == secondary {
			t.Fatalf("detached root scan %q still visible", s.ID)
		}
	}

	retained, err := store.Get(ctx, "s2")
	contractcheck.FailErr(t, "Get retained", err)
	if retained == nil {
		t.Fatal("detach must retain scan row")
	}

	_ = project.RootPaths(&project.Project{
		Roots: []project.Root{{Path: primary}, {Path: secondary}},
	})
}

func pathInSet(path string, set []string) bool {
	for _, p := range set {
		if p == path {
			return true
		}
	}
	return false
}

func TestScanRetentionDefaultKeepsOrphanedRows(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	sqlDB := testdbfixture.Open(t, "retain-policy.db")

	orphanPath := t.TempDir()
	ts := db.FormatTime(time.Now().UTC().Add(-40 * 24 * time.Hour))
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
