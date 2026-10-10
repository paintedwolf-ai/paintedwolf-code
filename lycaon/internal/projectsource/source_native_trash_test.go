package projectsource

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/google/uuid"
	"github.com/lycaon/lycaon/internal/sourcebranch"
	"github.com/lycaon/lycaon/internal/sourceledger"
	"github.com/lycaon/lycaon/internal/testutil"
)

func TestNativeTrashRestoresReceiptAcrossRestartAndRedo(t *testing.T) {
	service, p, root, _ := sourceMutationFixture(t)
	path := filepath.Join(root, "tree")
	testutil.FailErr(t, "create tree", os.Mkdir(path, 0700))
	file, err := os.Create(filepath.Join(path, "large"))
	testutil.FailErr(t, "create sparse file", err)
	testutil.FailErr(t, "size sparse file", file.Truncate(3<<30))
	testutil.FailErr(t, "close sparse file", file.Close())
	ledger := service.settlement.recorder.(*sourceledger.Store)
	tracked, err := ledger.TrackFile(t.Context(), sourceledger.TrackInput{ProjectID: p.ID, RootID: p.Roots[0].ID, Path: "tree/large", Size: 3 << 30})
	testutil.FailErr(t, "track sparse child identity", err)
	id := uuid.NewString()
	var work lifecycleWork
	ctx := WithSourceProgress(t.Context(), work.observe)
	testutil.FailErr(t, "trash tree", service.Delete(ctx, id, p, SourceDeleteRequest{RootID: p.Roots[0].ID, Path: "tree", Recursive: true}))
	row, found, err := service.Journal.load(t.Context(), id)
	testutil.FailErr(t, "read receipt", err)
	if !found || row.Plan.NativeTrash == nil || row.Plan.NativeTrash.Receipt.Identity == "" || row.Plan.RecoveryCount != 0 || row.Plan.TreeSHA != "" || work.verificationBytes() != 0 {
		t.Fatalf("native operation did not retain a content-free receipt: %+v", row)
	}
	var count int
	testutil.FailErr(t, "count recovery entries", service.Journal.db.QueryRowContext(t.Context(), `SELECT count(*) FROM source_recovery_entries WHERE recovery_id=?`, id).Scan(&count))
	if count != 0 {
		t.Fatalf("native trash captured %d entries", count)
	}
	restarted := NewSourceMutationService(service.Journal.db, service.settlement.recorder.(*sourceledger.Store))
	installTestTrash(t, restarted)
	undoHistoryHead(t, restarted, p, id)
	restored, err := ledger.History.ResolveHead(t.Context(), p.ID, sourcebranch.Trunk, p.Roots[0].ID, "tree/large")
	testutil.FailErr(t, "resolve restored child identity", err)
	if restored.FileID != tracked.FileID || restored.VersionID != tracked.VersionID {
		t.Fatalf("native restore rewrote child history: %+v", restored)
	}
	testutil.FailErr(t, "edit restored tree", os.WriteFile(filepath.Join(path, "later"), []byte("preserved"), 0600))
	redoHistoryHead(t, restarted, p, id)
	undoHistoryHead(t, restarted, p, id)
	assertSourceHistoryFile(t, root, "tree/later", "preserved")
}

func TestNativeTrashRefusesMissingOrReplacedReceipt(t *testing.T) {
	for _, replace := range []bool{false, true} {
		t.Run(map[bool]string{false: "emptied", true: "replaced"}[replace], func(t *testing.T) {
			service, p, root, _ := sourceMutationFixture(t)
			older := uuid.NewString()
			_, err := service.Create(t.Context(), older, p, SourceEntryCreateRequest{RootID: p.Roots[0].ID, Path: "older", Kind: SourceEntryFile})
			testutil.FailErr(t, "create older history", err)
			testutil.FailErr(t, "seed source", os.WriteFile(filepath.Join(root, "file"), []byte("selected"), 0600))
			id := uuid.NewString()
			testutil.FailErr(t, "trash", service.Delete(t.Context(), id, p, SourceDeleteRequest{RootID: p.Roots[0].ID, Path: "file"}))
			row, _, err := service.Journal.load(t.Context(), id)
			testutil.FailErr(t, "read receipt", err)
			path := row.Plan.NativeTrash.Receipt.Path
			// Keep the original inode alive so replacement cannot reuse its identity.
			testutil.FailErr(t, "remove from recorded location", os.Rename(path, path+".removed"))
			if replace {
				testutil.FailErr(t, "replace trash path", os.WriteFile(path, []byte("someone else"), 0600))
			}
			_, err = service.Undo(t.Context(), uuid.NewString(), p, SourceHistoryMutationRequest{ExpectedEntryID: id})
			if !errors.Is(err, ErrSourceTrashUnavailable) {
				t.Fatalf("missing recovery error = %v", err)
			}
			if _, err := os.Lstat(filepath.Join(root, "file")); !os.IsNotExist(err) {
				t.Fatalf("unavailable item restored: %v", err)
			}
			var availability string
			testutil.FailErr(t, "retained unavailable entry", service.Journal.db.QueryRowContext(t.Context(), `SELECT availability FROM source_history_entries WHERE id=?`, id).Scan(&availability))
			if availability != "unavailable" {
				t.Fatalf("availability = %q", availability)
			}
			restarted := NewSourceMutationService(service.Journal.db, service.settlement.recorder.(*sourceledger.Store))
			installTestTrash(t, restarted)
			undoHistoryHead(t, restarted, p, older)
			if replace {
				assertSourceHistoryFile(t, filepath.Dir(path), filepath.Base(path), "someone else")
			}
		})
	}
}

func TestNativeCopyHistoryPreservesEditsWithoutRetainedBackup(t *testing.T) {
	service, p, root, _ := sourceMutationFixture(t)
	testutil.FailErr(t, "seed copy", os.WriteFile(filepath.Join(root, "source"), []byte("original"), 0600))
	id := uuid.NewString()
	_, err := service.Copy(t.Context(), id, p, SourceCopyRequest{RootID: p.Roots[0].ID, From: "source", To: "copy"})
	testutil.FailErr(t, "copy", err)
	var count int
	testutil.FailErr(t, "count recovery rows", service.Journal.db.QueryRowContext(t.Context(), `SELECT count(*) FROM source_recovery_entries WHERE recovery_id=?`, id).Scan(&count))
	if count != 0 {
		t.Fatalf("copy captured %d redundant backup entries", count)
	}
	testutil.FailErr(t, "edit output", os.WriteFile(filepath.Join(root, "copy"), []byte("later edit"), 0600))
	undoHistoryHead(t, service, p, id)
	redoHistoryHead(t, service, p, id)
	assertSourceHistoryFile(t, root, "copy", "later edit")
	assertSourceHistoryFile(t, root, "source", "original")
}

func TestRevisionBufferBoundsGrowingInput(t *testing.T) {
	var buffer bytes.Buffer
	writer := sourceRevisionBuffer{buffer: &buffer}
	chunk := bytes.Repeat([]byte("x"), sourceledger.MaxRevisionContentBytes/2+1)
	for range 4 {
		n, err := writer.Write(chunk)
		if err != nil || n != len(chunk) {
			t.Fatalf("write = %d, %v", n, err)
		}
	}
	if buffer.Len() != sourceledger.MaxRevisionContentBytes {
		t.Fatalf("retained %d bytes", buffer.Len())
	}
}

func TestNativeTrashRestoresLogicalFileIdentity(t *testing.T) {
	service, p, _, _ := sourceMutationFixture(t)
	_, err := service.Create(t.Context(), uuid.NewString(), p, SourceEntryCreateRequest{RootID: p.Roots[0].ID, Path: "file", Kind: SourceEntryFile})
	testutil.FailErr(t, "create tracked file", err)
	ledger := service.settlement.recorder.(*sourceledger.Store)
	original, err := ledger.History.ResolveHead(t.Context(), p.ID, sourcebranch.Trunk, p.Roots[0].ID, "file")
	testutil.FailErr(t, "resolve file before Trash", err)
	id := uuid.NewString()
	testutil.FailErr(t, "trash tracked file", service.Delete(t.Context(), id, p, SourceDeleteRequest{RootID: p.Roots[0].ID, Path: "file"}))
	undoHistoryHead(t, service, p, id)
	restored, err := ledger.History.ResolveHead(t.Context(), p.ID, sourcebranch.Trunk, p.Roots[0].ID, "file")
	testutil.FailErr(t, "resolve restored file", err)
	if restored.FileID != original.FileID {
		t.Fatalf("native recovery forked file identity: %s to %s", original.FileID, restored.FileID)
	}
}

func TestNativeTrashUnavailableRedoDoesNotBlockLaterHistory(t *testing.T) {
	service, p, _, _ := sourceMutationFixture(t)
	first, second := uuid.NewString(), uuid.NewString()
	for _, item := range []struct{ id, path string }{{first, "first"}, {second, "second"}} {
		_, err := service.Create(t.Context(), item.id, p, SourceEntryCreateRequest{RootID: p.Roots[0].ID, Path: item.path, Kind: SourceEntryFile})
		testutil.FailErr(t, "create history", err)
	}
	undoHistoryHead(t, service, p, second)
	undoHistoryHead(t, service, p, first)
	entry, err := service.History.historyEntry(t.Context(), p.ID, "undone", "ASC")
	testutil.FailErr(t, "read redo receipt", err)
	testutil.FailErr(t, "empty Trash item", os.Remove(entry.RedoPlan.NativeTrash.Receipt.Path))
	_, err = service.Redo(t.Context(), uuid.NewString(), p, SourceHistoryMutationRequest{ExpectedEntryID: first})
	if !errors.Is(err, ErrSourceTrashUnavailable) {
		t.Fatalf("unavailable redo = %v", err)
	}
	restarted := NewSourceMutationService(service.Journal.db, service.settlement.recorder.(*sourceledger.Store))
	installTestTrash(t, restarted)
	redoHistoryHead(t, restarted, p, second)
}
