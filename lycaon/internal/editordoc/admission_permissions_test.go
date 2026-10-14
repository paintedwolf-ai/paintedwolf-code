package editordoc

import (
	"context"
	"database/sql"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/google/uuid"
	"github.com/lycaon/lycaon/internal/project"
	"github.com/lycaon/lycaon/internal/sourcefeed"
	"github.com/lycaon/lycaon/internal/sourceledger"
	"github.com/lycaon/lycaon/internal/testdbfixture"
	"github.com/lycaon/lycaon/internal/testdbseed"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/pkg/api"
)

func TestDocumentAdmissionRequiresWritableSource(t *testing.T) {
	for _, existing := range []bool{false, true} {
		name := "new document"
		if existing {
			name = "retained document"
		}
		t.Run(name, func(t *testing.T) {
			f := newAgentFixture(t, map[string]string{"a.txt": "saved"})
			if existing {
				_, err := f.service.Open(t.Context(), f.project, "a.txt", f.rootID, "", "first-window", nil)
				testutil.FailErr(t, "open writable document", err)
			}
			path := filepath.Join(f.root, "a.txt")
			testutil.FailErr(t, "make source read-only", os.Chmod(path, 0o444))
			_, err := f.service.Open(t.Context(), f.project, "a.txt", f.rootID, "", "second-window", nil)
			if !errors.Is(err, ErrReadOnly) {
				t.Fatalf("open read-only source: %v", err)
			}
			testutil.FailErr(t, "make source writable", os.Chmod(path, 0o644))
			_, err = f.service.Open(t.Context(), f.project, "a.txt", f.rootID, "", "second-window", nil)
			testutil.FailErr(t, "open writable source", err)
		})
	}
}

func TestLifecycleDependentsIncludeOpenAndUnsavedDocuments(t *testing.T) {
	sqlDB := testdbfixture.Open(t, "store.db")
	root := t.TempDir()
	projectID, rootID := testdbseed.DefaultProjectID, uuid.NewString()
	testdbseed.InsertProjectRootWithID(t, sqlDB, projectID, rootID, root)
	testutil.FailErr(t, "write", os.WriteFile(filepath.Join(root, "a.txt"), []byte("base\n"), 0o644))
	p := &project.Project{ID: projectID, Roots: []project.Root{{ID: rootID, ProjectID: projectID, Path: root, IsPrimary: true}}}
	sourceHistory7 := sourceledger.New(sqlDB, "")
	service := New(NewStore(sqlDB), sourceHistory7, sourceHistory7.History, fixedRoots{p: p})
	closeServiceAtCleanup(t, service)

	document, err := service.Open(t.Context(), p, "a.txt", rootID, "", "window", nil)
	testutil.FailErr(t, "open document", err)
	dependents, err := service.LifecycleDependents(t.Context(), projectID, rootID)
	testutil.FailErr(t, "list open dependents", err)
	if len(dependents) != 1 || dependents[0].ID != document.ID {
		t.Fatalf("open dependents = %+v", dependents)
	}
	service.ForgetRemoved([]string{document.ID})
	testutil.FailErr(t, "close tab retention", service.ReplaceRetention(t.Context(), projectID, "window", nil, nil, nil))
	dependents, err = service.LifecycleDependents(t.Context(), projectID, rootID)
	testutil.FailErr(t, "list forgotten dependents", err)
	if len(dependents) != 0 {
		t.Fatalf("forgotten clean dependents = %+v", dependents)
	}
	document, err = service.Join(t.Context(), document.ID, projectID, ReplicaJoin{ClientID: "window", Incarnation: uuid.NewString(), Epoch: 1})
	testutil.FailErr(t, "reclaim forgotten document", err)

	err = service.Leave(t.Context(), document.ID, projectID, "window", document.Participants[0].Incarnation)
	testutil.FailErr(t, "release clean document", err)
	dependents, err = service.LifecycleDependents(t.Context(), projectID, rootID)
	testutil.FailErr(t, "list released dependents", err)
	if len(dependents) != 0 {
		t.Fatalf("released clean dependents = %+v", dependents)
	}

	document, err = service.Join(t.Context(), document.ID, projectID, ReplicaJoin{ClientID: "window", Incarnation: uuid.NewString(), Epoch: 1})
	testutil.FailErr(t, "reclaim document", err)
	document, err = service.ReplaceSnapshot(t.Context(), document.ID, projectID, SnapshotReplacement{DocumentCommand: DocumentCommand{ClientID: "window", OperationID: uuid.NewString(), ExpectedRevision: document.Revision}, Content: "draft\n", EOL: "lf", MixedEOL: false})
	testutil.FailErr(t, "update draft", err)
	err = service.Leave(t.Context(), document.ID, projectID, "window", document.Participants[0].Incarnation)
	testutil.FailErr(t, "release dirty document", err)
	dependents, err = service.LifecycleDependents(t.Context(), projectID, rootID)
	testutil.FailErr(t, "list dirty dependents", err)
	if len(dependents) != 1 || dependents[0].ID != document.ID || !dependents[0].Dirty {
		t.Fatalf("dirty dependents = %+v", dependents)
	}
}

// failingEditorFeed rejects staged events.
type failingEditorFeed struct{}

func (failingEditorFeed) SourceChanged(context.Context, api.SourceChangesEvent) error {
	return errors.New("source feed unavailable")
}

func (failingEditorFeed) SourceChangedTx(context.Context, *sql.Tx, api.SourceChangesEvent) error {
	return errors.New("source feed unavailable")
}

func (failingEditorFeed) Deliver() {}

func TestSaveFeedFailureRollsBackDocumentAndMutation(t *testing.T) {
	sqlDB := testdbfixture.Open(t, "store.db")
	root := t.TempDir()
	projectID, rootID := testdbseed.DefaultProjectID, uuid.NewString()
	testdbseed.InsertProjectRootWithID(t, sqlDB, projectID, rootID, root)
	testutil.FailErr(t, "write", os.WriteFile(filepath.Join(root, "a.txt"), []byte("base\n"), 0o644))
	p := &project.Project{ID: projectID, Roots: []project.Root{{ID: rootID, ProjectID: projectID, Path: root, IsPrimary: true}}}
	recorder := &captureRecorder{Store: sourceledger.New(sqlDB, "")}
	service := New(NewStore(sqlDB), recorder, recorder.History, fixedRoots{p: p})
	closeServiceAtCleanup(t, service)
	document, err := service.Open(t.Context(), p, "a.txt", rootID, "", "window", nil)
	testutil.FailErr(t, "open", err)
	document, err = service.ReplaceSnapshot(t.Context(), document.ID, projectID, SnapshotReplacement{DocumentCommand: DocumentCommand{ClientID: "window", OperationID: uuid.NewString(), ExpectedRevision: document.Revision}, Content: "draft\n", EOL: "lf", MixedEOL: false})
	testutil.FailErr(t, "draft", err)
	t.Cleanup(sourcefeed.Bind(failingEditorFeed{}))

	operationID := uuid.NewString()
	if _, err := service.Save(t.Context(), p, document.ID, "window", operationID, "", 0, document.Revision); err == nil {
		t.Fatal("save reported success while its event could not be staged")
	}
	stored, err := NewStore(sqlDB).Get(t.Context(), document.ID)
	testutil.FailErr(t, "reload document", err)
	if stored.Revision != document.Revision || !stored.Dirty {
		t.Fatalf("document advanced past an unannounced save: %+v", stored)
	}
	mutation, err := NewStore(sqlDB).Mutation(t.Context(), operationID)
	testutil.FailErr(t, "reload mutation", err)
	if mutation.Status == "complete" {
		t.Fatal("mutation completed while its event could not be staged")
	}
}

func TestNewRequiresLedger(t *testing.T) {
	defer func() {
		if recover() == nil {
			t.Fatal("missing ledger accepted")
		}
	}()
	New(NewStore(testdbfixture.Open(t, "store.db")), nil, nil, fixedRoots{})
}

