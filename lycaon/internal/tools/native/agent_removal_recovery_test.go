package native

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/google/uuid"
	"github.com/lycaon/lycaon/internal/project"
	"github.com/lycaon/lycaon/internal/sourceledger"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/tools"
	nativefixture "github.com/lycaon/lycaon/internal/tools/native/internal/testfixture"
)

// recoverableToolFixture wires the tool context to the Files history service
// with a system Trash that must never be reached.
func recoverableToolFixture(t *testing.T) (tools.ToolContext, *project.SourceMutationService, *project.Project, string) {
	t.Helper()
	dir := t.TempDir()
	ledger := bindLedgerForWrites(t, dir)
	service := project.NewSourceMutationService(ledger.LedgerDB(), ledger)
	service.SetTrashMover(func(context.Context, string) error {
		t.Fatal("an agent tool reached the system Trash")
		return nil
	})
	tctx := nativefixture.Context(dir)
	tctx.Identity.ProjectID, tctx.Identity.SessionID, tctx.Identity.UserTurn = "p1", "s1", 1
	tctx.Source.SourceLedger, tctx.Source.SourceMutations = ledger, service
	tctx.Source.History = tools.SourceHistory{Files: ledger.History, Comparison: ledger.Comparisons, Git: ledger.Git, Authorship: ledger.Walk}
	tctx.Source.Commands = ledger.Commands
	tctx.Source.GitMutations = ledger.Git
	tctx.Source.Observations = ledger.Inventory
	p := &project.Project{ID: "p1", Roots: []project.Root{{ID: "r1", ProjectID: "p1", Path: dir, IsPrimary: true}}}
	return tctx, service, p, dir
}

func unversionedBody() []byte {
	return bytes.Repeat([]byte("large\n"), sourceledger.MaxRevisionContentBytes/4)
}

func undoLatest(t *testing.T, service *project.SourceMutationService, p *project.Project, wantKind string) {
	t.Helper()
	history, err := service.History(t.Context(), p.ID)
	testutil.FailErr(t, "read history", err)
	if history.Undo == nil || history.Undo.Kind != wantKind {
		t.Fatalf("undo head = %+v, want kind %s", history.Undo, wantKind)
	}
	_, err = service.Undo(t.Context(), uuid.NewString(), p, project.SourceHistoryMutationRequest{ExpectedEntryID: history.Undo.ID})
	testutil.FailErr(t, "undo", err)
}

func assertFileBytes(t *testing.T, path string, want []byte) {
	t.Helper()
	got, err := os.ReadFile(path)
	testutil.FailErr(t, "read "+filepath.Base(path), err)
	if !bytes.Equal(got, want) {
		t.Fatalf("%s holds %d bytes, want %d", filepath.Base(path), len(got), len(want))
	}
}

// The delete tool removes a file history cannot hold, and Files history undoes it.
func TestDeleteToolRemovalIsUndoableWhateverItsSize(t *testing.T) {
	tctx, service, p, dir := recoverableToolFixture(t)
	body := unversionedBody()
	testutil.FailErr(t, "seed", os.WriteFile(filepath.Join(dir, "data.bin"), body, 0o640))
	_, err := (&DeleteTool{Boundary: nativefixture.Boundary(t)}).Run(t.Context(), map[string]any{"paths": []any{"data.bin"}}, tctx)
	testutil.FailErr(t, "delete", err)
	if _, err := os.Lstat(filepath.Join(dir, "data.bin")); !os.IsNotExist(err) {
		t.Fatalf("deleted file remains: %v", err)
	}
	undoLatest(t, service, p, "delete")
	assertFileBytes(t, filepath.Join(dir, "data.bin"), body)
}

// A move onto an existing file sets that file aside before the rename lands.
func TestMoveOntoAnExistingFileKeepsTheReplacedFile(t *testing.T) {
	tctx, service, p, dir := recoverableToolFixture(t)
	replaced := unversionedBody()
	testutil.FailErr(t, "seed destination", os.WriteFile(filepath.Join(dir, "b.bin"), replaced, 0o640))
	testutil.FailErr(t, "seed source", os.WriteFile(filepath.Join(dir, "a.bin"), []byte("new"), 0o640))
	_, err := (&MoveTool{Boundary: nativefixture.Boundary(t)}).Run(t.Context(),
		map[string]any{"moves": []any{map[string]any{"from": "a.bin", "to": "b.bin"}}}, tctx)
	testutil.FailErr(t, "move", err)
	assertFileBytes(t, filepath.Join(dir, "b.bin"), []byte("new"))
	testutil.FailErr(t, "clear the moved file for the restore", os.Remove(filepath.Join(dir, "b.bin")))
	undoLatest(t, service, p, "delete")
	assertFileBytes(t, filepath.Join(dir, "b.bin"), replaced)
}

// A copy over a file too large for history keeps the overwritten file.
func TestCopyOverAnUnversionedFileKeepsTheReplacedFile(t *testing.T) {
	tctx, service, p, dir := recoverableToolFixture(t)
	replaced := unversionedBody()
	testutil.FailErr(t, "seed destination", os.WriteFile(filepath.Join(dir, "b.bin"), replaced, 0o640))
	testutil.FailErr(t, "seed source", os.WriteFile(filepath.Join(dir, "a.bin"), []byte("copy"), 0o640))
	_, err := (&CopyTool{Boundary: nativefixture.Boundary(t)}).Run(t.Context(),
		map[string]any{"copies": []any{map[string]any{"from": "a.bin", "to": "b.bin"}}}, tctx)
	testutil.FailErr(t, "copy", err)
	assertFileBytes(t, filepath.Join(dir, "b.bin"), []byte("copy"))
	testutil.FailErr(t, "clear the copy for the restore", os.Remove(filepath.Join(dir, "b.bin")))
	undoLatest(t, service, p, "delete")
	assertFileBytes(t, filepath.Join(dir, "b.bin"), replaced)
}

// A small overwrite stays one write whose pre-image source history keeps.
func TestCopyOverAVersionedFileStaysOneWrite(t *testing.T) {
	tctx, service, p, dir := recoverableToolFixture(t)
	testutil.FailErr(t, "seed destination", os.WriteFile(filepath.Join(dir, "b.txt"), []byte("old"), 0o640))
	testutil.FailErr(t, "seed source", os.WriteFile(filepath.Join(dir, "a.txt"), []byte("new"), 0o640))
	_, err := (&CopyTool{Boundary: nativefixture.Boundary(t)}).Run(t.Context(),
		map[string]any{"copies": []any{map[string]any{"from": "a.txt", "to": "b.txt"}}}, tctx)
	testutil.FailErr(t, "copy", err)
	history, err := service.History(t.Context(), p.ID)
	testutil.FailErr(t, "read history", err)
	if history.Undo != nil {
		t.Fatalf("a versioned overwrite entered Files history: %+v", history.Undo)
	}
}
