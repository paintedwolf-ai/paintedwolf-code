package project

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"github.com/lycaon/lycaon/internal/testdbfixture"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/lycaon/lycaon/internal/db"
	"github.com/lycaon/lycaon/internal/sourcefeed"
	"github.com/lycaon/lycaon/internal/sourceledger"
	"github.com/lycaon/lycaon/internal/testdbseed"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/textfile"
	"github.com/lycaon/lycaon/pkg/api"
)

func sourceMutationFixture(t *testing.T) (*SourceMutationService, *Project, string, *db.Queries) {
	t.Helper()
	rootPath := t.TempDir()
	sqlDB := testdbfixture.Open(t, "store.db")
	rootID := testdbseed.InsertProjectRoot(t, sqlDB, testdbseed.DefaultProjectID, rootPath)
	ledger := sourceledger.New(sqlDB, filepath.Join(t.TempDir(), "source-content"))
	service := NewSourceMutationService(sqlDB, ledger)
	installTestTrash(t, service)
	p := &Project{ID: testdbseed.DefaultProjectID, Roots: []Root{{ID: rootID, ProjectID: testdbseed.DefaultProjectID, Path: rootPath, IsPrimary: true}}}
	return service, p, rootPath, db.New(sqlDB)
}

func TestSourceMutationWriteExactReplayAndConflict(t *testing.T) {
	service, p, rootPath, _ := sourceMutationFixture(t)
	path := filepath.Join(rootPath, "note.txt")
	testutil.FailErr(t, "seed source", os.WriteFile(path, []byte("before"), 0o640))
	opID := uuid.NewString()
	req := SourceWriteRequest{Path: "note.txt", RootID: p.Roots[0].ID, Content: "after", Encoding: textfile.UTF8,
		BaseSHA256: textfile.SHA256([]byte("before"))}
	first, err := service.Write(t.Context(), opID, p, req)
	testutil.FailErr(t, "first write", err)
	replayed, err := service.Write(t.Context(), opID, p, req)
	testutil.FailErr(t, "replay write", err)
	if replayed.SHA256 != first.SHA256 {
		t.Fatalf("replay sha = %q want %q", replayed.SHA256, first.SHA256)
	}
	req.Content = "different"
	if _, err := service.Write(t.Context(), opID, p, req); !errors.Is(err, ErrSourceMutationConflict) {
		t.Fatalf("conflicting reuse error = %v", err)
	}
	content, err := os.ReadFile(path)
	testutil.FailErr(t, "read source", err)
	if string(content) != "after" {
		t.Fatalf("source = %q", content)
	}
}

func TestSourceMutationRecoveryFinishesAppliedWrite(t *testing.T) {
	service, p, rootPath, _ := sourceMutationFixture(t)
	path := filepath.Join(rootPath, "recover.txt")
	testutil.FailErr(t, "seed source", os.WriteFile(path, []byte("before"), 0o600))
	req := SourceWriteRequest{Path: "recover.txt", RootID: p.Roots[0].ID, Content: "after", Encoding: textfile.UTF8,
		BaseSHA256: textfile.SHA256([]byte("before"))}
	writePlan, err := planProjectSourceWrite(p, req)
	testutil.FailErr(t, "plan write", err)
	response, err := sourceMutationDigest(req)
	testutil.FailErr(t, "digest", err)
	opID := uuid.NewString()
	plan := sourceMutationPlan{Kind: "write", ProjectID: p.ID, WorkspaceID: p.WorkspaceID(), RootID: writePlan.Result.RootID, RootPath: rootPath,
		Path: writePlan.Result.Path, AbsPath: writePlan.Result.AbsPath, BaseSHA256: writePlan.BaseSHA256,
		AfterSHA: writePlan.Result.SHA256, Before: writePlan.Result.Before,
		After: writePlan.Result.After, Changed: true, Response: []byte(`{"Path":"recover.txt"}`)}
	now := time.Now().UTC()
	row := &sourceMutationRow{ID: opID, ProjectID: p.ID, Kind: "write", InputDigest: response,
		Plan: plan, Status: sourceMutationPrepared, CreatedAt: now, UpdatedAt: now}
	testutil.FailErr(t, "insert mutation", service.insert(t.Context(), row))
	testutil.FailErr(t, "apply file before crash", applyProjectSourceWrite(writePlan))
	restarted := NewSourceMutationService(service.db, service.ledger)
	testutil.FailErr(t, "recover mutations", restarted.Recover(t.Context()))
	loaded, found, err := restarted.load(t.Context(), opID)
	testutil.FailErr(t, "load recovered mutation", err)
	if !found || loaded.Status != sourceMutationCommitted {
		t.Fatalf("recovered status = %q found=%v", loaded.Status, found)
	}
}

func TestSourceMutationBatchWriteExactReplay(t *testing.T) {
	service, p, rootPath, _ := sourceMutationFixture(t)
	for _, name := range []string{"a.txt", "b.txt"} {
		testutil.FailErr(t, "seed "+name, os.WriteFile(filepath.Join(rootPath, name), []byte("before"), 0o640))
	}
	opID := uuid.NewString()
	prepareCalls := 0
	req := SourceBatchWriteRequest{Input: struct{ Replacement string }{"after"}, SessionID: "session-1", Prepare: func() (SourceBatchWritePlan, error) {
		prepareCalls++
		return SourceBatchWritePlan{Writes: []SourceWriteRequest{
			{Path: "a.txt", RootID: p.Roots[0].ID, Content: "after-a", Encoding: textfile.UTF8, BaseSHA256: textfile.SHA256([]byte("before"))},
			{Path: "b.txt", RootID: p.Roots[0].ID, Content: "after-b", Encoding: textfile.UTF8, BaseSHA256: textfile.SHA256([]byte("before"))},
		}, Response: json.RawMessage(`{"batch":"complete"}`)}, nil
	}}
	first, err := service.BatchWrite(t.Context(), opID, p, req)
	testutil.FailErr(t, "batch write", err)
	replayed, err := service.BatchWrite(t.Context(), opID, p, req)
	testutil.FailErr(t, "replay batch write", err)
	if string(first) != string(replayed) || prepareCalls != 1 {
		t.Fatalf("replay response=%s first=%s prepare_calls=%d", replayed, first, prepareCalls)
	}
	for name, want := range map[string]string{"a.txt": "after-a", "b.txt": "after-b"} {
		content, err := os.ReadFile(filepath.Join(rootPath, name))
		testutil.FailErr(t, "read "+name, err)
		if string(content) != want {
			t.Fatalf("%s = %q want %q", name, content, want)
		}
	}
	walk, err := service.ledger.QueryWalk(t.Context(), p.ID, sourceledger.Baseline{Kind: sourceledger.BaselineSession, SessionID: "session-1"}, 20, 0, sourceledger.CommitLens{})
	testutil.FailErr(t, "query batch ledger", err)
	batchMembers := 0
	for _, file := range walk.Files {
		for _, effect := range file.Effects {
			if effect.BatchID == opID {
				batchMembers++
			}
		}
	}
	if batchMembers != 2 {
		t.Fatalf("batch members = %d want 2", batchMembers)
	}
}

func TestSourceMutationBatchWriteRollsBackOnConflict(t *testing.T) {
	_, p, rootPath, _ := sourceMutationFixture(t)
	for _, name := range []string{"a.txt", "b.txt"} {
		testutil.FailErr(t, "seed "+name, os.WriteFile(filepath.Join(rootPath, name), []byte("before"), 0o640))
	}
	writes := make([]sourceMutationPlan, 0, 2)
	for _, item := range []struct{ path, content string }{{"a.txt", "after-a"}, {"b.txt", "after-b"}} {
		planned, err := planProjectSourceWrite(p, SourceWriteRequest{Path: item.path, RootID: p.Roots[0].ID,
			Content: item.content, Encoding: textfile.UTF8, BaseSHA256: textfile.SHA256([]byte("before"))})
		testutil.FailErr(t, "plan "+item.path, err)
		writes = append(writes, sourceMutationPlan{Kind: "write", ProjectID: p.ID, RootID: planned.Result.RootID,
			RootPath: rootPath, Path: planned.Result.Path, AbsPath: planned.Result.AbsPath, BaseSHA256: planned.BaseSHA256,
			AfterSHA: planned.Result.SHA256, Before: planned.Result.Before,
			After: planned.Result.After, Changed: true})
	}
	testutil.FailErr(t, "race b.txt", os.WriteFile(filepath.Join(rootPath, "b.txt"), []byte("raced"), 0o640))
	err := applySourceBatchWrite(t.Context(), &sourceMutationPlan{Kind: "batch_write", Writes: writes})
	if !errors.Is(err, ErrSourceWriteConflict) {
		t.Fatalf("batch error = %v", err)
	}
	content, readErr := os.ReadFile(filepath.Join(rootPath, "a.txt"))
	testutil.FailErr(t, "read rolled back a.txt", readErr)
	if string(content) != "before" {
		t.Fatalf("a.txt after rollback = %q", content)
	}
}

func TestSourceLifecycleMutationsReplayFinalState(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	service := NewSourceMutationService(nil, nil)
	installTestTrash(t, service)
	rootPath := t.TempDir()
	p := &Project{ID: "project", Roots: []Root{{ID: "root", ProjectID: "project", Path: rootPath, IsPrimary: true}}}

	createID := uuid.NewString()
	createReq := SourceEntryCreateRequest{Path: "dir/a.txt", Kind: SourceEntryFile, RootID: "root"}
	_, err := service.Create(t.Context(), createID, p, createReq)
	testutil.FailErr(t, "create source", err)
	_, err = service.Create(t.Context(), createID, p, createReq)
	testutil.FailErr(t, "replay create", err)
	testutil.FailErr(t, "write created source", os.WriteFile(filepath.Join(rootPath, "dir/a.txt"), []byte("value"), 0o644))

	copyID := uuid.NewString()
	copyReq := SourceCopyRequest{RootID: "root", From: "dir/a.txt", To: "dir/b.txt"}
	_, err = service.Copy(t.Context(), copyID, p, copyReq)
	testutil.FailErr(t, "copy source", err)
	_, err = service.Copy(t.Context(), copyID, p, copyReq)
	testutil.FailErr(t, "replay copy", err)

	renameID := uuid.NewString()
	renameReq := SourceRenameRequest{RootID: "root", From: "dir/b.txt", To: "dir/c.txt"}
	_, err = service.Rename(t.Context(), renameID, p, renameReq)
	testutil.FailErr(t, "rename source", err)
	_, err = service.Rename(t.Context(), renameID, p, renameReq)
	testutil.FailErr(t, "replay rename", err)

	deleteID := uuid.NewString()
	deleteReq := SourceDeleteRequest{RootID: "root", Path: "dir/c.txt"}
	testutil.FailErr(t, "delete source", service.Delete(t.Context(), deleteID, p, deleteReq))
	testutil.FailErr(t, "replay delete", service.Delete(t.Context(), deleteID, p, deleteReq))
	if _, err := os.Lstat(filepath.Join(rootPath, "dir/c.txt")); !os.IsNotExist(err) {
		t.Fatalf("deleted source still exists: %v", err)
	}
}

// failingFeed rejects event staging after attribution is written.
type failingFeed struct{}

func (failingFeed) SourceChanged(context.Context, api.SourceChangesEvent) error {
	return errors.New("source feed unavailable")
}

func (failingFeed) SourceChangedTx(context.Context, *sql.Tx, api.SourceChangesEvent) error {
	return errors.New("source feed unavailable")
}

func (failingFeed) Deliver() {}

func TestSourceMutationPathsIncludesBothRenameParents(t *testing.T) {
	got := sourceMutationPaths(sourceMutationPlan{FromPath: "old/a.go", ToPath: "new/a.go"})
	if len(got) != 2 || got[0] != "old/a.go" || got[1] != "new/a.go" {
		t.Fatalf("rename mutation paths = %v", got)
	}
}

func TestSourceMutationLedgerFailureLeavesOperationRecoverable(t *testing.T) {
	service, p, rootPath, _ := sourceMutationFixture(t)
	path := filepath.Join(rootPath, "note.txt")
	testutil.FailErr(t, "seed source", os.WriteFile(path, []byte("before"), 0o640))
	_, err := service.db.ExecContext(t.Context(), `DROP TABLE source_operations`)
	testutil.FailErr(t, "drop source operations", err)

	opID := uuid.NewString()
	if _, err := service.Write(t.Context(), opID, p, SourceWriteRequest{
		Path: "note.txt", RootID: p.Roots[0].ID, Content: "after", Encoding: textfile.UTF8,
		BaseSHA256: textfile.SHA256([]byte("before")),
	}); err == nil {
		t.Fatal("write succeeded with no ledger to record it")
	}
	row, found, err := service.load(t.Context(), opID)
	testutil.FailErr(t, "load mutation", err)
	if !found {
		t.Fatal("no source_mutations row survived the failed commit")
	}
	if row.Status != sourceMutationFileApplied {
		t.Fatalf("status = %q want %q — the status update outlived the ledger failure",
			row.Status, sourceMutationFileApplied)
	}
}

func TestFileAppliedReceiptReprovesAndReappliesLostFilesystemEffect(t *testing.T) {
	service, p, rootPath, _ := sourceMutationFixture(t)
	path := filepath.Join(rootPath, "note.txt")
	before, after := []byte("before"), []byte("after")
	testutil.FailErr(t, "seed source", os.WriteFile(path, before, 0o640))
	now := time.Now().UTC()
	row := &sourceMutationRow{
		ID: uuid.NewString(), ProjectID: p.ID, Kind: "write", InputDigest: "digest",
		Status: sourceMutationFileApplied, CreatedAt: now, UpdatedAt: now,
		Plan: sourceMutationPlan{
			Kind: "write", ProjectID: p.ID, WorkspaceID: p.WorkspaceID(), RootID: p.Roots[0].ID, RootPath: rootPath,
			Path: "note.txt", AbsPath: path, BaseSHA256: textfile.SHA256(before),
			AfterSHA: textfile.SHA256(after), Before: before, After: after,
			Changed: true, Response: json.RawMessage(`{}`),
		},
	}
	testutil.FailErr(t, "insert file-applied receipt", service.insert(t.Context(), row))
	_, err := service.resume(t.Context(), row)
	testutil.FailErr(t, "resume file-applied receipt", err)
	got, err := os.ReadFile(path)
	testutil.FailErr(t, "read reapplied source", err)
	if string(got) != string(after) || row.Status != sourceMutationCommitted {
		t.Fatalf("recovered bytes=%q status=%s, want after/committed", got, row.Status)
	}
}

func TestSourceMutationEventFailureRollsBackAttribution(t *testing.T) {
	service, p, rootPath, _ := sourceMutationFixture(t)
	testutil.FailErr(t, "seed source", os.WriteFile(filepath.Join(rootPath, "note.txt"), []byte("before"), 0o640))
	t.Cleanup(sourcefeed.Bind(failingFeed{}))

	opID := uuid.NewString()
	if _, err := service.Write(t.Context(), opID, p, SourceWriteRequest{
		Path: "note.txt", RootID: p.Roots[0].ID, Content: "after", Encoding: textfile.UTF8,
		BaseSHA256: textfile.SHA256([]byte("before")),
	}); err == nil {
		t.Fatal("write succeeded with a feed that cannot stage its event")
	}
	var effects int
	testutil.FailErr(t, "count source effects", service.db.QueryRowContext(t.Context(),
		`SELECT COUNT(*) FROM source_effects WHERE project_id = ?`, p.ID).Scan(&effects))
	if effects != 0 {
		t.Fatalf("source effects = %d want 0", effects)
	}
	row, found, err := service.load(t.Context(), opID)
	testutil.FailErr(t, "load mutation", err)
	if !found || row.Status == sourceMutationCommitted {
		t.Fatalf("status = %q found=%v want an uncommitted, recoverable operation", row.Status, found)
	}
}
