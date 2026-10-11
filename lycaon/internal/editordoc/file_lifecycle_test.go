package editordoc

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/lycaon/lycaon/internal/projectsource"
	"github.com/lycaon/lycaon/internal/sourcebranch"
	"github.com/lycaon/lycaon/internal/sourceledger"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/pkg/api"
)

// A document follows its path: deletion leaves it absent with its draft, and
// a recreation rebinds it to the new file identity and merges the new bytes.
func TestDocumentFollowsItsPathThroughDeletionAndRecreation(t *testing.T) {
	f, ledger := newLedgerAgentFixture(t, map[string]string{"a.txt": "old file\n"})
	d := f.open(t, "a.txt")
	typed, err := f.service.ReplaceSnapshot(t.Context(), d.ID, f.project.ID, SnapshotReplacement{
		DocumentCommand: DocumentCommand{ClientID: "window", OperationID: uuid.NewString(), ExpectedRevision: d.Revision},
		Content:         "old file\nunsaved line\n", EOL: "lf"})
	testutil.FailErr(t, "type an unsaved line", err)

	testutil.FailErr(t, "remove on disk", os.Remove(filepath.Join(f.root, "a.txt")))
	testutil.FailErr(t, "record the deletion", ledger.Record(t.Context(), sourceledger.RecordInput{
		RecordLocation: sourceledger.RecordLocation{RootID: f.rootID, Path: "a.txt"}, ProjectID: f.project.ID, OperationID: uuid.NewString(), Op: api.SourceChangeOpDelete,
		Origin: api.SourceChangeOriginExternal, Before: []byte("old file\n")}))
	testutil.FailErr(t, "observe the deletion", f.service.ObserveExternal(t.Context(), f.project, []PathRef{{RootID: f.rootID, Path: "a.txt"}}))
	absent, err := f.store.Get(t.Context(), d.ID)
	testutil.FailErr(t, "reload absent document", err)
	if !absent.Absent || !absent.Dirty || absent.Draft != typed.Draft || absent.FileID != d.FileID {
		t.Fatalf("deletion did not leave an absent document with its draft: %+v", absent)
	}

	// The agent reads the draft the person still holds for the deleted path.
	read, found, err := f.service.OpenText(t.Context(), f.project.ID, sourcebranch.Trunk, f.rootID, "a.txt")
	testutil.FailErr(t, "agent read of the absent document", err)
	if !found || read.ID != d.ID || !read.Absent || read.Draft != typed.Draft {
		t.Fatalf("agent did not read the retained draft: found=%v %+v", found, read)
	}
	if _, found, err := f.service.OpenText(t.Context(), f.project.ID, sourcebranch.Trunk, f.rootID, "b.txt"); err != nil || found {
		t.Fatalf("a path with no document read as present: found=%v err=%v", found, err)
	}

	// The path comes back with other content: the ledger sees a new file, the
	// document rebinds to it and merges the bytes as an outside change.
	f.write(t, "a.txt", "new file\n")
	testutil.FailErr(t, "observe the recreation", f.service.ObserveExternal(t.Context(), f.project, []PathRef{{RootID: f.rootID, Path: "a.txt"}}))
	rebound, err := f.store.Get(t.Context(), d.ID)
	testutil.FailErr(t, "reload rebound document", err)
	current, err := ledger.LookupFile(t.Context(), sourceledger.TrackInput{ProjectID: f.project.ID, RootID: f.rootID, Path: "a.txt"})
	testutil.FailErr(t, "look up the recreated file", err)
	if rebound.Absent || rebound.FileID == d.FileID || rebound.FileID != current.FileID {
		t.Fatalf("recreation did not rebind the document to the new file: document=%+v ledger=%+v", rebound, current)
	}
	if rebound.BaseContent != "new file\n" || !rebound.Dirty || !strings.Contains(rebound.Draft, "unsaved line") || !strings.Contains(rebound.Draft, "new file") {
		t.Fatalf("recreation did not merge the new bytes with the draft: %+v", rebound)
	}
	// One document per path: opening it again resumes the same document.
	reopened := f.open(t, "a.txt")
	if reopened.ID != d.ID || reopened.FileID != current.FileID {
		t.Fatalf("reopen minted another document: %+v", reopened)
	}
}

func TestSavingAnAbsentDocumentRecreatesTheFile(t *testing.T) {
	f, ledger := newLedgerAgentFixture(t, map[string]string{"a.txt": "old file\n"})
	d := f.open(t, "a.txt")
	typed, err := f.service.ReplaceSnapshot(t.Context(), d.ID, f.project.ID, SnapshotReplacement{
		DocumentCommand: DocumentCommand{ClientID: "window", OperationID: uuid.NewString(), ExpectedRevision: d.Revision},
		Content:         "kept draft\n", EOL: "lf"})
	testutil.FailErr(t, "type", err)
	testutil.FailErr(t, "remove on disk", os.Remove(filepath.Join(f.root, "a.txt")))
	testutil.FailErr(t, "record the deletion", ledger.Record(t.Context(), sourceledger.RecordInput{
		RecordLocation: sourceledger.RecordLocation{RootID: f.rootID, Path: "a.txt"}, ProjectID: f.project.ID, OperationID: uuid.NewString(), Op: api.SourceChangeOpDelete,
		Origin: api.SourceChangeOriginExternal, Before: []byte("old file\n")}))
	absent, err := f.service.ObserveDisk(t.Context(), f.project, d.ID, "window")
	testutil.FailErr(t, "observe", err)
	if !absent.Absent {
		t.Fatalf("fixture did not go absent: %+v", absent)
	}

	pinned, err := f.service.PinSave(t.Context(), f.project.ID, d.ID, "window", uuid.NewString())
	testutil.FailErr(t, "reserve the save", err)
	saved, err := f.service.Save(t.Context(), f.project, d.ID, "window", uuid.NewString(), "", 0, pinned.Revision)
	testutil.FailErr(t, "save the absent document", err)
	if saved.Absent || saved.Dirty || saved.Diverged || f.disk(t, "a.txt") != typed.Draft {
		t.Fatalf("save did not recreate the file from the draft: %+v disk=%q", saved, f.disk(t, "a.txt"))
	}
	current, err := ledger.LookupFile(t.Context(), sourceledger.TrackInput{ProjectID: f.project.ID, RootID: f.rootID, Path: "a.txt"})
	testutil.FailErr(t, "look up the recreated file", err)
	if current.FileID == "" || current.FileID == d.FileID || saved.FileID != current.FileID {
		t.Fatalf("the recreated file kept the deleted identity: document=%s ledger=%+v original=%s", saved.FileID, current, d.FileID)
	}
}

func TestSavingAnAbsentDocumentYieldsToAFileThatReappeared(t *testing.T) {
	f := newAgentFixture(t, map[string]string{"a.txt": "old file\n"})
	d := f.open(t, "a.txt")
	_, err := f.service.ReplaceSnapshot(t.Context(), d.ID, f.project.ID, SnapshotReplacement{
		DocumentCommand: DocumentCommand{ClientID: "window", OperationID: uuid.NewString(), ExpectedRevision: d.Revision},
		Content:         "old file\nkept draft\n", EOL: "lf"})
	testutil.FailErr(t, "type", err)
	testutil.FailErr(t, "remove on disk", os.Remove(filepath.Join(f.root, "a.txt")))
	_, err = f.service.ObserveDisk(t.Context(), f.project, d.ID, "window")
	testutil.FailErr(t, "observe", err)
	pinned, err := f.service.PinSave(t.Context(), f.project.ID, d.ID, "window", uuid.NewString())
	testutil.FailErr(t, "reserve the save", err)

	// Someone put a file back before the save published.
	f.write(t, "a.txt", "theirs\n")
	_, err = f.service.Save(t.Context(), f.project, d.ID, "window", uuid.NewString(), "", 0, pinned.Revision)
	if !errors.Is(err, projectsource.ErrSourceWriteConflict) {
		t.Fatalf("save replaced a file it did not expect: %v", err)
	}
	if f.disk(t, "a.txt") != "theirs\n" {
		t.Fatalf("their file was overwritten: %q", f.disk(t, "a.txt"))
	}
	merged, err := f.service.ObserveDisk(t.Context(), f.project, d.ID, "window")
	testutil.FailErr(t, "observe the reappearance", err)
	if merged.Absent || merged.Diverged || merged.BaseContent != "theirs\n" || !merged.Dirty || merged.Draft != "theirs\nkept draft\n" {
		t.Fatalf("the reappeared file did not merge with the draft: %+v", merged)
	}
}
