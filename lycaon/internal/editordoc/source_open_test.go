package editordoc

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/google/uuid"
	"github.com/lycaon/lycaon/internal/project"
	"github.com/lycaon/lycaon/internal/sourceledger"
	"github.com/lycaon/lycaon/internal/testdbfixture"
	"github.com/lycaon/lycaon/internal/testdbseed"
	"github.com/lycaon/lycaon/internal/testutil"
)

func TestOpenObservedKeepsSavedSourceAndLiveDraftDistinct(t *testing.T) {
	db := testdbfixture.Open(t, "store.db")
	root := t.TempDir()
	projectID, rootID := testdbseed.DefaultProjectID, uuid.NewString()
	testdbseed.InsertProjectRootWithID(t, db, projectID, rootID, root)
	testutil.FailErr(t, "write source", os.WriteFile(filepath.Join(root, "a.txt"), []byte("saved\n"), 0o644))
	p := &project.Project{ID: projectID, Roots: []project.Root{{ID: rootID, ProjectID: projectID, Path: root, IsPrimary: true}}}
	service := New(NewStore(db), sourceledger.New(db, ""), fixedRoots{p: p})
	closeServiceAtCleanup(t, service)
	observation, err := project.ObserveProjectSource(p, project.SourceReadRequest{Path: "a.txt", RootID: rootID})
	testutil.FailErr(t, "observe source", err)
	opened, err := service.OpenObserved(t.Context(), p, observation, "", "window", nil)
	testutil.FailErr(t, "open source", err)
	if opened.Document == nil || opened.Source.FileID != opened.Document.FileID || opened.Source.VersionID == "" {
		t.Fatalf("missing shared source identity: %+v", opened)
	}
	if len(opened.Document.Participants) != 0 {
		t.Fatal("source preparation claimed editor presence before synchronization")
	}
	document := opened.Document
	document, err = service.ReplaceSnapshot(t.Context(), document.ID, projectID, SnapshotReplacement{DocumentCommand: DocumentCommand{ClientID: "window", OperationID: uuid.NewString(), ExpectedRevision: document.Revision}, Content: "draft\n", EOL: "lf"})
	testutil.FailErr(t, "replace draft", err)
	reopened, err := service.OpenObserved(t.Context(), p, observation, "", "another-window", nil)
	testutil.FailErr(t, "reopen source", err)
	if reopened.Source.Content != "saved\n" || reopened.Document.Draft != "draft\n" || reopened.Document.ID != document.ID {
		t.Fatalf("source=%+v document=%+v", reopened.Source, reopened.Document)
	}
	// A retained replica that already holds the state receives only what it lacks; a stale epoch gets the full snapshot.
	current := &Retained{DocumentID: document.ID, Epoch: reopened.Document.Epoch, Vector: reopened.Document.StateVector}
	differential, err := service.OpenObserved(t.Context(), p, observation, "", "window", current)
	testutil.FailErr(t, "reopen with a retained replica", err)
	if len(differential.Document.CRDTUpdate) >= len(reopened.Document.CRDTUpdate) {
		t.Fatalf("retained identity did not narrow the projection: %d bytes vs %d", len(differential.Document.CRDTUpdate), len(reopened.Document.CRDTUpdate))
	}
	stale, err := service.OpenObserved(t.Context(), p, observation, "", "window", &Retained{DocumentID: document.ID, Epoch: reopened.Document.Epoch + 1, Vector: reopened.Document.StateVector})
	testutil.FailErr(t, "reopen with a stale replica", err)
	if len(stale.Document.CRDTUpdate) != len(reopened.Document.CRDTUpdate) {
		t.Fatalf("stale replica identity must receive the full snapshot: %d bytes vs %d", len(stale.Document.CRDTUpdate), len(reopened.Document.CRDTUpdate))
	}
	// The initial observation cannot override a later save during document admission.
	testutil.FailErr(t, "change disk", os.WriteFile(filepath.Join(root, "a.txt"), []byte("external\n"), 0o644))
	latest, err := service.OpenObserved(t.Context(), p, observation, "", "window", nil)
	testutil.FailErr(t, "open after disk change", err)
	if latest.Source.Content != "external\n" {
		t.Fatalf("reused obsolete observation: %+v", latest.Source)
	}
	testutil.FailErr(t, "make source read-only", os.Chmod(filepath.Join(root, "a.txt"), 0o444))
	readonly, err := service.OpenObserved(t.Context(), p, observation, "", "read-only-window", nil)
	testutil.FailErr(t, "open after permission change", err)
	if readonly.Document != nil || readonly.Source.Writable || readonly.Source.FileID == "" {
		t.Fatalf("stale writable admission: %+v", readonly)
	}
	testutil.FailErr(t, "make source writable", os.Chmod(filepath.Join(root, "a.txt"), 0o644))
	testutil.FailErr(t, "replace source with binary", os.WriteFile(filepath.Join(root, "a.txt"), []byte{0, 1, 2, 3}, 0o644))
	binary, err := service.OpenObserved(t.Context(), p, observation, "", "binary-window", nil)
	testutil.FailErr(t, "open after binary replacement", err)
	if binary.Document != nil || !binary.Source.Binary || binary.Source.FileID == "" || binary.Source.VersionID == readonly.Source.VersionID {
		t.Fatalf("stale text admission: %+v", binary)
	}
}

func TestOpenObservedBinaryDoesNotCreateDocument(t *testing.T) {
	db := testdbfixture.Open(t, "store.db")
	root := t.TempDir()
	projectID, rootID := testdbseed.DefaultProjectID, uuid.NewString()
	testdbseed.InsertProjectRootWithID(t, db, projectID, rootID, root)
	testutil.FailErr(t, "write binary", os.WriteFile(filepath.Join(root, "data.bin"), []byte{0, 1, 2, 3}, 0o644))
	p := &project.Project{ID: projectID, Roots: []project.Root{{ID: rootID, ProjectID: projectID, Path: root, IsPrimary: true}}}
	service := New(NewStore(db), sourceledger.New(db, ""), fixedRoots{p: p})
	closeServiceAtCleanup(t, service)
	observation, err := project.ObserveProjectSource(p, project.SourceReadRequest{Path: "data.bin", RootID: rootID})
	testutil.FailErr(t, "observe binary", err)
	opened, err := service.OpenObserved(t.Context(), p, observation, "", "window", nil)
	testutil.FailErr(t, "open binary", err)
	if opened.Document != nil || !opened.Source.Binary {
		t.Fatalf("binary admission: %+v", opened)
	}
}
