package integration

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/lycaon/lycaon/internal/project"
	"github.com/lycaon/lycaon/internal/scan"
	"github.com/lycaon/lycaon/internal/testdbfixture"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/pkg/api"
)

type lifecycleFixedHeadSHA struct{ sha string }

func (f lifecycleFixedHeadSHA) HeadSHA(context.Context, string) (string, error) { return f.sha, nil }

func TestScanOnceSharedAcrossCanonicalPath(t *testing.T) {
	sqlDB := testdbfixture.Open(t, "scan-once.db")

	dir := t.TempDir()
	canonical, err := scan.CanonicalPath(dir)
	testutil.FailErr(t, "CanonicalPath", err)

	store := scan.NewSQLStore(sqlDB)
	coord := newTestCoordinator(t, store, lifecycleFixedHeadSHA{sha: "abc"})
	ctx := context.Background()

	first, err := coord.Enqueue(ctx, scan.EnqueueRequest{
		ProjectDir: canonical,
		Categories: []api.ScanCategory{api.ScanCategorySecurity},
		ScannerID:  "lycaon-sast",
		HeadSHA:    "abc",
		Trigger:    api.ScanTriggerManual,
	})
	testutil.FailErr(t, "first Enqueue", err)

	second, err := coord.Enqueue(ctx, scan.EnqueueRequest{
		ProjectDir: canonical,
		Categories: []api.ScanCategory{api.ScanCategorySecurity},
		ScannerID:  "lycaon-sast",
		HeadSHA:    "abc",
		Trigger:    api.ScanTriggerManual,
	})
	testutil.FailErr(t, "second Enqueue", err)
	if first.ID != second.ID {
		t.Fatalf("scan-once failed: %s vs %s", first.ID, second.ID)
	}

	rows, err := store.ListByCanonicalPath(ctx, canonical)
	testutil.FailErr(t, "ListByCanonicalPath", err)
	if len(rows) != 1 {
		t.Fatalf("want 1 scan row, got %d", len(rows))
	}
}

func TestScanListPageKeepsFirstPageWatermark(t *testing.T) {
	sqlDB := testdbfixture.Open(t, "scan-page.db")
	store := scan.NewSQLStore(sqlDB)
	ctx := context.Background()
	root := t.TempDir()
	base := time.Date(2026, 2, 1, 0, 0, 0, 0, time.UTC)
	insert := func(id string, at time.Time) {
		t.Helper()
		testutil.FailErr(t, "insert scan", store.Insert(ctx, api.CodeScan{ID: id, CanonicalPath: root, Categories: []api.ScanCategory{api.ScanCategorySecurity}, Status: api.CodeScanStatusComplete, CreatedAt: at}, nil, ""))
	}
	insert("00000000-0000-4000-8000-000000000001", base)
	insert("00000000-0000-4000-8000-000000000003", base)
	insert("00000000-0000-4000-8000-000000000004", base)

	first, err := store.ListPageByCanonicalPaths(ctx, []string{root}, scan.PageQuery{Limit: 2})
	testutil.FailErr(t, "first page", err)
	if len(first.Scans) != 2 || first.NextCursor == "" {
		t.Fatalf("first page = %+v", first)
	}
	insert("00000000-0000-4000-8000-000000000002", base)
	second, err := store.ListPageByCanonicalPaths(ctx, []string{root}, scan.PageQuery{Limit: 2, Cursor: first.NextCursor})
	testutil.FailErr(t, "second page", err)
	if len(second.Scans) != 1 || second.Scans[0].ID != "00000000-0000-4000-8000-000000000001" {
		t.Fatalf("second page crossed watermark: %+v", second.Scans)
	}
	if _, err := store.ListPageByCanonicalPaths(ctx, []string{t.TempDir()}, scan.PageQuery{Limit: 2, Cursor: first.NextCursor}); !errors.Is(err, scan.ErrInvalidScanPageCursor) {
		t.Fatalf("cursor reused for another root set: %v", err)
	}
}

func TestDerivedVisibilityFromCurrentRoots(t *testing.T) {
	sqlDB := testdbfixture.Open(t, "derived.db")

	primary := t.TempDir()
	secondary := t.TempDir()
	other := t.TempDir()

	store := scan.NewSQLStore(sqlDB)
	coord := newTestCoordinator(t, store, nil)
	ctx := context.Background()

	insertComplete := func(path, id string) {
		t.Helper()
		if err := store.Insert(ctx, api.CodeScan{
			ID:            id,
			CanonicalPath: path,
			Categories:    []api.ScanCategory{api.ScanCategorySecurity},
			ScannerID:     "lycaon-sast",
			Status:        api.CodeScanStatusComplete,
			CreatedAt:     time.Now().UTC(),
			HeadSHA:       "head1",
		}, nil, ""); err != nil {
			t.Fatal(err)
		}
	}
	insertComplete(primary, "scan-primary")
	insertComplete(secondary, "scan-secondary")
	insertComplete(other, "scan-other")

	visible, err := coord.List(ctx, []string{primary, secondary}, 20)
	testutil.FailErr(t, "List", err)
	if len(visible) != 2 {
		t.Fatalf("visible scans = %d want 2", len(visible))
	}

	detached, err := coord.List(ctx, []string{primary}, 20)
	testutil.FailErr(t, "List after detach", err)
	if len(detached) != 1 || detached[0].ID != "scan-primary" {
		t.Fatalf("after detach got %+v want scan-primary only", detached)
	}
}

func TestDetachAndProjectDeleteRetainScanRows(t *testing.T) {
	sqlDB := testdbfixture.Open(t, "retain.db")

	dir := t.TempDir()
	store := scan.NewSQLStore(sqlDB)
	ctx := context.Background()
	if err := store.Insert(ctx, api.CodeScan{
		ID:            "retained",
		CanonicalPath: dir,
		Categories:    []api.ScanCategory{api.ScanCategorySecurity},
		ScannerID:     "lycaon-sast",
		Status:        api.CodeScanStatusComplete,
		CreatedAt:     time.Now().UTC(),
	}, nil, ""); err != nil {
		t.Fatal(err)
	}

	_, err := sqlDB.ExecContext(ctx, `DELETE FROM project_roots WHERE path = ?`, dir)
	if err != nil {
		t.Fatalf("simulate detach: %v", err)
	}
	got, err := store.Get(ctx, "retained")
	testutil.FailErr(t, "Get after detach", err)
	if got == nil || got.ID != "retained" {
		t.Fatal("scan row must survive root detach")
	}
}

func TestCanonicalPathCollapsesSymlinkVariants(t *testing.T) {
	base := t.TempDir()
	real := filepath.Join(base, "repo")
	if err := os.MkdirAll(real, 0o755); err != nil {
		testutil.FailErr(t, "create directory", err)
	}
	link := filepath.Join(base, "link")
	if err := os.Symlink(real, link); err != nil {
		testutil.FailErr(t, "os.Symlink failed", err)
	}
	a, err := scan.CanonicalPath(real)
	testutil.FailErr(t, "CanonicalPath real", err)
	b, err := scan.CanonicalPath(link)
	testutil.FailErr(t, "CanonicalPath link", err)
	if a != b {
		t.Fatalf("canonical paths differ: %q vs %q", a, b)
	}
}

func TestProjectRootPathsDerived(t *testing.T) {
	p := &project.Project{
		Roots: []project.Root{
			{Path: "/a", IsPrimary: true},
			{Path: "/b"},
		},
	}
	paths := project.RootPaths(p)
	if len(paths) != 2 {
		t.Fatalf("paths = %v", paths)
	}
}
