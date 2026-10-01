package execution

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/lycaon/lycaon/internal/backgroundwork"
	scanbase "github.com/lycaon/lycaon/internal/scan"
	scancatalog "github.com/lycaon/lycaon/internal/scan/catalog"
	scanoutput "github.com/lycaon/lycaon/internal/scan/output"
	"github.com/lycaon/lycaon/internal/testdbfixture"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/pkg/api"
)

func TestAbsoluteScanPathsPlaceFilesUnderTheRoot(t *testing.T) {
	root := filepath.Join(t.TempDir(), "live")
	paths := absoluteScanPaths(root, []string{"src/main.go", "pkg/lib.go"})
	want := []string{
		filepath.Join(root, "src", "main.go"),
		filepath.Join(root, "pkg", "lib.go"),
	}
	for index := range want {
		if paths[index] != want[index] {
			t.Fatalf("path %d = %q want %q", index, paths[index], want[index])
		}
	}
}

func TestScanAdmissionKeySeparatesDistinctWork(t *testing.T) {
	base := &api.CodeScan{
		CanonicalPath: "/repo", ScannerID: "scanner-1",
		Categories: []api.ScanCategory{api.ScanCategorySecurity, api.ScanCategorySAST},
	}
	reordered := &api.CodeScan{
		CanonicalPath: "/repo", ScannerID: "scanner-1",
		Categories: []api.ScanCategory{api.ScanCategorySAST, api.ScanCategorySecurity},
	}
	if scanAdmissionKey(base) != scanAdmissionKey(reordered) {
		t.Fatal("category order changed the admission key; two generations of the same work would not supersede")
	}
	otherScanner := &api.CodeScan{
		CanonicalPath: "/repo", ScannerID: "scanner-2",
		Categories: []api.ScanCategory{api.ScanCategorySecurity, api.ScanCategorySAST},
	}
	if scanAdmissionKey(base) == scanAdmissionKey(otherScanner) {
		t.Fatal("a different scanner shares an admission key; unrelated work would supersede")
	}
	otherRoot := &api.CodeScan{
		CanonicalPath: "/other", ScannerID: "scanner-1",
		Categories: []api.ScanCategory{api.ScanCategorySecurity, api.ScanCategorySAST},
	}
	if scanAdmissionKey(base) == scanAdmissionKey(otherRoot) {
		t.Fatal("a different tree shares an admission key")
	}
}

func TestScanFailsWhenTheBoundSnapshotIsNotResident(t *testing.T) {
	database := testdbfixture.Open(t, "store.db")
	sqlStore := scanbase.NewSQLStore(database)
	snapshots := testSnapshots(t, sqlStore)
	root := t.TempDir()
	testutil.FailErr(t, "write source", os.WriteFile(filepath.Join(root, "main.go"), []byte("package main\n"), 0o644))

	coordinator := scanbase.NewCoordinator(sqlStore, nil, snapshots)
	created, err := coordinator.Enqueue(t.Context(), scanbase.EnqueueRequest{
		ProjectDir: root, Categories: []api.ScanCategory{api.ScanCategorySecurity},
		ScannerID: "scanner-1", SourceSnapshotID: publishTestSnapshot(t, snapshots, root),
	})
	testutil.FailErr(t, "enqueue scan", err)
	_, err = database.ExecContext(t.Context(), "DELETE FROM source_snapshots WHERE id = ?", created.SourceSnapshotID)
	testutil.FailErr(t, "remove bound source snapshot", err)

	ran := false
	runner := &Runner{
		Store: sqlStore, Snapshots: snapshots, Broker: backgroundwork.Process(),
		Registry: recordingRegistry{onRun: func(scanbase.ScanRequest) { ran = true }},
	}
	claimed, err := sqlStore.ClaimNext(t.Context())
	testutil.FailErr(t, "claim scan", err)
	runner.execute(t.Context(), claimed)

	final, err := sqlStore.Get(t.Context(), created.ID)
	testutil.FailErr(t, "load scan", err)
	if final.Status != api.CodeScanStatusFailed {
		t.Fatalf("status = %q, want failed", final.Status)
	}
	if final.Attempt != 1 {
		t.Fatalf("attempt = %d want 1 (missing snapshot is terminal)", final.Attempt)
	}
	if ran {
		t.Fatal("a scanner ran without a resolvable source manifest")
	}
}

// The engine reads live files selected by the snapshot.
func TestScanNamesFilesThatMovedWhileItRan(t *testing.T) {
	database := testdbfixture.Open(t, "store.db")
	sqlStore := scanbase.NewSQLStore(database)
	snapshots := testSnapshots(t, sqlStore)
	root := t.TempDir()
	main := filepath.Join(root, "main.go")
	testutil.FailErr(t, "write source", os.WriteFile(main, []byte("package main\n"), 0o644))
	testutil.FailErr(t, "write other", os.WriteFile(filepath.Join(root, "other.go"), []byte("package other\n"), 0o644))
	canonical, err := scanbase.CanonicalPath(root)
	testutil.FailErr(t, "canonical root", err)

	coordinator := scanbase.NewCoordinator(sqlStore, nil, snapshots)
	created, err := coordinator.Enqueue(t.Context(), scanbase.EnqueueRequest{
		ProjectDir: root, Categories: []api.ScanCategory{api.ScanCategorySecurity}, ScannerID: "scanner-1",
	})
	testutil.FailErr(t, "enqueue scan", err)

	var scanned []string
	runner := &Runner{
		Store: sqlStore, Snapshots: snapshots, Broker: backgroundwork.Process(),
		Registry: recordingRegistry{onRun: func(req scanbase.ScanRequest) {
			scanned = req.Paths
			if req.ProjectDir != canonical {
				t.Errorf("engine root = %q, want the live tree %q", req.ProjectDir, canonical)
			}
			if err := os.WriteFile(main, []byte("package main // rewritten under the scan\n"), 0o644); err != nil {
				t.Errorf("rewrite under the scan: %v", err)
			}
		}},
	}
	claimed, err := sqlStore.ClaimNext(t.Context())
	testutil.FailErr(t, "claim scan", err)
	runner.execute(t.Context(), claimed)

	if len(scanned) != 2 {
		t.Fatalf("engine received %v, want the generation's two files", scanned)
	}
	final, err := sqlStore.Get(t.Context(), created.ID)
	testutil.FailErr(t, "load scan", err)
	if final.Status != api.CodeScanStatusComplete {
		t.Fatalf("status = %q error = %q, want complete", final.Status, final.Error)
	}
	if final.CoverageStatus != api.ScanCoveragePartial {
		t.Fatalf("coverage = %q, want partial: a scanned file moved", final.CoverageStatus)
	}
	moved := 0
	for _, warning := range final.Warnings {
		if warning.Kind == api.ScanWarningSourceMoved && warning.File == "main.go" {
			moved++
		}
	}
	if moved != 1 {
		t.Fatalf("warnings = %+v, want one source_moved for main.go", final.Warnings)
	}
}

func TestScanAcceptsIdenticalContentWithNewMetadata(t *testing.T) {
	database := testdbfixture.Open(t, "store.db")
	sqlStore := scanbase.NewSQLStore(database)
	snapshots := testSnapshots(t, sqlStore)
	root := t.TempDir()
	path := filepath.Join(root, "main.go")
	testutil.FailErr(t, "write source", os.WriteFile(path, []byte("package main\n"), 0o644))
	coordinator := scanbase.NewCoordinator(sqlStore, nil, snapshots)
	created, err := coordinator.Enqueue(t.Context(), scanbase.EnqueueRequest{
		ProjectDir: root, Categories: []api.ScanCategory{api.ScanCategorySecurity}, ScannerID: "scanner-1",
	})
	testutil.FailErr(t, "enqueue scan", err)
	info, err := os.Stat(path)
	testutil.FailErr(t, "stat source", err)
	modified := info.ModTime().Add(time.Hour)
	testutil.FailErr(t, "touch identical content", os.Chtimes(path, modified, modified))
	runner := &Runner{
		Store: sqlStore, Snapshots: snapshots, Broker: backgroundwork.Process(),
		Registry: recordingRegistry{},
	}
	claimed, err := sqlStore.ClaimNext(t.Context())
	testutil.FailErr(t, "claim scan", err)
	runner.execute(t.Context(), claimed)
	final, err := sqlStore.Get(t.Context(), created.ID)
	testutil.FailErr(t, "load scan", err)
	if final.Status != api.CodeScanStatusComplete || final.CoverageStatus != api.ScanCoverageComplete {
		t.Fatalf("identical rewrite lost coverage: status=%s coverage=%s warnings=%+v", final.Status, final.CoverageStatus, final.Warnings)
	}
}

// recordingRegistry runs no scanner and reports what it was asked to scan.
type recordingRegistry struct{ onRun func(scanbase.ScanRequest) }

func (r recordingRegistry) Register(scanbase.CodeScanner) error { return nil }

func (r recordingRegistry) Get(id string) (scanbase.CodeScanner, error) {
	return recordingScanner{id: id, onRun: r.onRun}, nil
}

func (r recordingRegistry) List(...api.ScanCategory) []scanbase.ScannerMeta {
	entry := scancatalog.ScannerEntry{
		ID: "scanner-1", Driver: "mock", Engine: "mock", ScopeKind: string(scancatalog.ScopeCustom),
		Categories: []string{string(api.ScanCategorySecurity)},
	}
	return []scanbase.ScannerMeta{{ID: entry.ID, Categories: entry.CategoriesAPI(), Contract: entry.Contract()}}
}

func (r recordingRegistry) RunBest(_ context.Context, _ []api.ScanCategory, req scanbase.ScanRequest) (*scanoutput.Result, error) {
	if r.onRun != nil {
		r.onRun(req)
	}
	return &scanoutput.Result{}, nil
}

type recordingScanner struct {
	id    string
	onRun func(scanbase.ScanRequest)
}

func (s recordingScanner) ID() string { return s.id }

func (s recordingScanner) Categories() []api.ScanCategory {
	return []api.ScanCategory{api.ScanCategorySecurity}
}

func (s recordingScanner) Run(_ context.Context, req scanbase.ScanRequest) (*scanoutput.Result, error) {
	if s.onRun != nil {
		s.onRun(req)
	}
	return &scanoutput.Result{}, nil
}
