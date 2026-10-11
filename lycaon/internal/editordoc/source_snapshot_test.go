package editordoc

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/google/uuid"
	"github.com/lycaon/lycaon/internal/project"
	"github.com/lycaon/lycaon/internal/projectsource"
	"github.com/lycaon/lycaon/internal/sourceledger"
	"github.com/lycaon/lycaon/internal/testdbfixture"
	"github.com/lycaon/lycaon/internal/testdbseed"
	"github.com/lycaon/lycaon/internal/testutil"
)

func TestSourceSnapshotUsesCurrentDiskOrPinnedUnsavedRevision(t *testing.T) {
	db := testdbfixture.Open(t, "store.db")
	root, projectID, rootID := t.TempDir(), testdbseed.DefaultProjectID, uuid.NewString()
	testdbseed.InsertProjectRootWithID(t, db, projectID, rootID, root)
	p := &project.Project{ID: projectID, Roots: []project.Root{{ID: rootID, ProjectID: projectID, Path: root, IsPrimary: true}}}
	sourceHistory8 := sourceledger.New(db, "")
	service := New(NewStore(db), sourceHistory8, sourceHistory8.History, fixedRoots{p: p})
	closeServiceAtCleanup(t, service)
	path := filepath.Join(root, "source.txt")
	req := projectsource.SourceReadRequest{RootID: rootID, Path: "source.txt"}
	testutil.FailErr(t, "write initial source", os.WriteFile(path, []byte("saved\n"), 0600))
	snapshot, err := service.ResolveSourceSnapshot(t.Context(), p, req, ObserveCurrent)
	testutil.FailErr(t, "observe without admission", err)
	if snapshot.Document != nil || snapshot.Source.Content != "saved\n" {
		t.Fatalf("initial snapshot: %+v", snapshot)
	}
	var count int
	testutil.FailErr(t, "count documents", db.QueryRowContext(t.Context(), "SELECT COUNT(*) FROM editor_documents").Scan(&count))
	if count != 0 {
		t.Fatal("read-only observation admitted a document")
	}
	admitted, err := service.ResolveSourceSnapshot(t.Context(), p, req, AdmitEditable)
	testutil.FailErr(t, "admit editable source", err)
	testutil.FailErr(t, "change disk", os.WriteFile(path, []byte("external\n"), 0600))
	snapshot, err = service.ResolveSourceSnapshot(t.Context(), p, req, ObserveCurrent)
	testutil.FailErr(t, "observe after disk change", err)
	if snapshot.Document != nil || snapshot.Source.Content != "external\n" {
		t.Fatal("clean editor hid current disk")
	}
	document, err := service.ReplaceSnapshot(t.Context(), admitted.Document.ID, projectID, SnapshotReplacement{
		DocumentCommand: DocumentCommand{ClientID: "window", OperationID: uuid.NewString(), ExpectedRevision: admitted.Document.Revision},
		Content:         "unsaved\n", EOL: "lf",
	})
	testutil.FailErr(t, "replace unsaved draft", err)
	snapshot, err = service.ResolveSourceSnapshot(t.Context(), p, req, ObserveCurrent)
	testutil.FailErr(t, "observe unsaved draft", err)
	if snapshot.Document == nil || snapshot.Document.Draft != "unsaved\n" || snapshot.Document.Revision != document.Revision {
		t.Fatal("reader missed accepted draft")
	}
	pinned := snapshot.Document
	_, err = service.ReplaceSnapshot(t.Context(), document.ID, projectID, SnapshotReplacement{
		DocumentCommand: DocumentCommand{ClientID: "window", OperationID: uuid.NewString(), ExpectedRevision: document.Revision},
		Content:         "later\n", EOL: "lf",
	})
	testutil.FailErr(t, "advance draft", err)
	if pinned.Draft != "unsaved\n" {
		t.Fatal("later edit changed pinned reader text")
	}
}
