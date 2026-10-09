package editordoc

import (
	"context"
	"database/sql"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/lycaon/lycaon/internal/project"
	"github.com/lycaon/lycaon/internal/projectsource"
	"github.com/lycaon/lycaon/internal/sourcefeed"
	"github.com/lycaon/lycaon/internal/sourceledger"
	"github.com/lycaon/lycaon/internal/testdbfixture"
	"github.com/lycaon/lycaon/internal/testdbseed"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/textfile"
	"github.com/lycaon/lycaon/pkg/api"
)

type captureRecorder struct {
	*sourceledger.Store
	records []sourceledger.RecordInput
	err     error
}

func (c *captureRecorder) RecordTx(_ context.Context, _ *sql.Tx, in sourceledger.RecordInput) error {
	c.records = append(c.records, in)
	return c.err
}

func (c *captureRecorder) RecordFileTx(_ context.Context, _ *sql.Tx, in sourceledger.RecordInput) (sourceledger.TrackedFile, error) {
	c.records = append(c.records, in)
	return sourceledger.TrackedFile{}, c.err
}

type fixedRoots struct{ p *project.Project }

func (f fixedRoots) Get(context.Context, string) (*project.Project, error) { return f.p, nil }

func TestSourceLifecycleReservationKeepsUnrelatedEditorSavesAvailable(t *testing.T) {
	f := newAgentFixture(t, map[string]string{"a.txt": "before", "b.txt": "before"})
	mutations := projectsource.NewSourceMutationService(f.store.db, f.recorder.Store)
	f.service.SetSourceMutations(mutations)
	documents := make(map[string]*Document)
	for _, name := range []string{"a.txt", "b.txt"} {
		d, err := f.service.Open(t.Context(), f.project, name, f.rootID, "", "window", nil)
		testutil.FailErr(t, "open document", err)
		d, err = f.service.ReplaceSnapshot(t.Context(), d.ID, f.project.ID, SnapshotReplacement{DocumentCommand: DocumentCommand{ClientID: "window", OperationID: uuid.NewString(), ExpectedRevision: d.Revision}, Content: "after", EOL: "lf"})
		testutil.FailErr(t, "edit draft", err)
		documents[name] = d
	}
	release, err := mutations.Paths.ReserveSourcePath(f.root, "a.txt")
	testutil.FailErr(t, "reserve lifecycle source", err)
	defer release()
	a, b := documents["a.txt"], documents["b.txt"]
	id := uuid.NewString()
	_, err = f.service.Save(t.Context(), f.project, a.ID, "window", id, "", 0, a.Revision)
	if !errors.Is(err, projectsource.ErrSourceBusy) {
		t.Fatalf("related save error=%v", err)
	}
	if f.disk(t, "a.txt") != "before" {
		t.Fatal("reserved source changed")
	}
	_, err = f.service.Save(t.Context(), f.project, b.ID, "window", uuid.NewString(), "", 0, b.Revision)
	testutil.FailErr(t, "save unrelated document", err)
	if f.disk(t, "b.txt") != "after" {
		t.Fatal("unrelated save did not publish")
	}
	release()
	_, err = f.service.Save(t.Context(), f.project, a.ID, "window", id, "", 0, a.Revision)
	testutil.FailErr(t, "retry after lifecycle releases path", err)
	if f.disk(t, "a.txt") != "after" {
		t.Fatal("retry did not preserve the draft")
	}
}

func TestSaveRetryDoesNotAdvanceDocumentAfterLedgerFailure(t *testing.T) {
	sqlDB := testdbfixture.Open(t, "store.db")
	root := t.TempDir()
	projectID, rootID := testdbseed.DefaultProjectID, uuid.NewString()
	testdbseed.InsertProjectRootWithID(t, sqlDB, projectID, rootID, root)
	testutil.FailErr(t, "write", os.WriteFile(filepath.Join(root, "a.txt"), []byte("base\n"), 0o644))
	p := &project.Project{ID: projectID, Roots: []project.Root{{ID: rootID, ProjectID: projectID, Path: root, IsPrimary: true}}}
	recorder := &captureRecorder{Store: sourceledger.New(sqlDB, ""), err: errors.New("ledger unavailable")}
	service := New(NewStore(sqlDB), recorder, recorder.History, fixedRoots{p: p})
	closeServiceAtCleanup(t, service)
	document, err := service.Open(t.Context(), p, "a.txt", rootID, "", "window", nil)
	testutil.FailErr(t, "open", err)
	document, err = service.ReplaceSnapshot(t.Context(), document.ID, projectID, SnapshotReplacement{DocumentCommand: DocumentCommand{ClientID: "window", OperationID: uuid.NewString(), ExpectedRevision: document.Revision}, Content: "draft\n", EOL: "lf", MixedEOL: false})
	testutil.FailErr(t, "draft", err)
	operationID := uuid.NewString()
	if _, err := service.Save(t.Context(), p, document.ID, "window", operationID, "", 0, document.Revision); err == nil {
		t.Fatal("save reported success while the ledger write failed")
	}
	if _, err := service.Save(t.Context(), p, document.ID, "window", operationID, "", 0, document.Revision); err == nil {
		t.Fatal("retry reported success while the ledger write failed")
	}
	stored, err := NewStore(sqlDB).Get(t.Context(), document.ID)
	testutil.FailErr(t, "reload document", err)
	if stored.Revision != document.Revision {
		t.Fatalf("document revision = %d, want %d — an unrecorded save advanced it", stored.Revision, document.Revision)
	}
	if !stored.Dirty || stored.BaseSHA256 != document.BaseSHA256 {
		t.Fatalf("document base moved without attribution: %+v", stored)
	}
	mutation, err := NewStore(sqlDB).Mutation(t.Context(), operationID)
	testutil.FailErr(t, "reload mutation", err)
	if mutation.Status == "complete" {
		t.Fatal("mutation completed while its ledger write failed")
	}
}

func TestSaveConflictIsTerminalAndDropsJournalPayload(t *testing.T) {
	sqlDB := testdbfixture.Open(t, "store.db")
	root := t.TempDir()
	projectID, rootID := testdbseed.DefaultProjectID, uuid.NewString()
	testdbseed.InsertProjectRootWithID(t, sqlDB, projectID, rootID, root)
	path := filepath.Join(root, "a.txt")
	testutil.FailErr(t, "write base", os.WriteFile(path, []byte("base\n"), 0o644))
	p := &project.Project{ID: projectID, Roots: []project.Root{{ID: rootID, ProjectID: projectID, Path: root, IsPrimary: true}}}
	store := NewStore(sqlDB)
	sourceHistory1 := sourceledger.New(sqlDB, "")
	service := New(store, sourceHistory1, sourceHistory1.History, fixedRoots{p: p})
	closeServiceAtCleanup(t, service)
	document, err := service.Open(t.Context(), p, "a.txt", rootID, "", "window", nil)
	testutil.FailErr(t, "open document", err)
	document, err = service.ReplaceSnapshot(t.Context(), document.ID, projectID, SnapshotReplacement{DocumentCommand: DocumentCommand{ClientID: "window", OperationID: uuid.NewString(), ExpectedRevision: document.Revision}, Content: "draft\n", EOL: "lf", MixedEOL: false})
	testutil.FailErr(t, "update draft", err)
	testutil.FailErr(t, "write external change", os.WriteFile(path, []byte("external\n"), 0o644))
	operationID := uuid.NewString()
	_, err = service.Save(t.Context(), p, document.ID, "window", operationID, "", 0, document.Revision)
	if !errors.Is(err, projectsource.ErrSourceWriteConflict) {
		t.Fatalf("save error = %v", err)
	}
	mutation, err := store.Mutation(t.Context(), operationID)
	testutil.FailErr(t, "read mutation", err)
	if mutation.Status != "conflict" || mutation.Content != "" || mutation.BeforeBytes != nil || mutation.AfterBytes != nil {
		t.Fatalf("terminal mutation = %+v", mutation)
	}
	diverged, err := store.Get(t.Context(), document.ID)
	testutil.FailErr(t, "read document", err)
	if !diverged.Diverged {
		t.Fatal("document did not record divergence")
	}
}

func TestObserveDiskMergesOutsideChangesWithTyping(t *testing.T) {
	sqlDB := testdbfixture.Open(t, "store.db")
	root := t.TempDir()
	projectID, rootID := testdbseed.DefaultProjectID, uuid.NewString()
	testdbseed.InsertProjectRootWithID(t, sqlDB, projectID, rootID, root)
	path := filepath.Join(root, "a.txt")
	testutil.FailErr(t, "write base", os.WriteFile(path, []byte("one\ntwo\n"), 0o644))
	p := &project.Project{ID: projectID, Roots: []project.Root{{ID: rootID, ProjectID: projectID, Path: root, IsPrimary: true}}}
	sourceHistory2 := sourceledger.New(sqlDB, "")
	service := New(NewStore(sqlDB), sourceHistory2, sourceHistory2.History, fixedRoots{p: p})
	closeServiceAtCleanup(t, service)
	document, err := service.Open(t.Context(), p, "a.txt", rootID, "", "window", nil)
	testutil.FailErr(t, "open document", err)
	document, err = service.ReplaceSnapshot(t.Context(), document.ID, projectID, SnapshotReplacement{DocumentCommand: DocumentCommand{ClientID: "window", OperationID: uuid.NewString(), ExpectedRevision: document.Revision}, Content: "one\nmy two\n", EOL: "lf", MixedEOL: false})
	testutil.FailErr(t, "update draft", err)
	testutil.FailErr(t, "write external change", os.WriteFile(path, []byte("ONE\ntwo\n"), 0o644))

	observed, err := service.ObserveDisk(t.Context(), p, document.ID, "window")
	testutil.FailErr(t, "observe disk", err)
	if observed.Diverged || observed.Draft != "ONE\nmy two\n" || observed.BaseContent != "ONE\ntwo\n" || observed.BaseSHA256 == document.BaseSHA256 {
		t.Fatalf("observed document = %+v", observed)
	}
	revision := observed.Revision
	observed, err = service.ObserveDisk(t.Context(), p, document.ID, "window")
	testutil.FailErr(t, "repeat observation", err)
	if observed.Revision != revision {
		t.Fatalf("duplicate observation revision = %d, want %d", observed.Revision, revision)
	}

	testutil.FailErr(t, "restore base", os.WriteFile(path, []byte("one\ntwo\n"), 0o644))
	observed, err = service.ObserveDisk(t.Context(), p, document.ID, "window")
	testutil.FailErr(t, "observe restored base", err)
	if observed.Diverged {
		t.Fatal("restored base remained diverged")
	}
}

func TestObserveAndReloadPreserveExplicitUTF16Decoding(t *testing.T) {
	sqlDB := testdbfixture.Open(t, "store.db")
	root := t.TempDir()
	projectID, rootID := testdbseed.DefaultProjectID, uuid.NewString()
	testdbseed.InsertProjectRootWithID(t, sqlDB, projectID, rootID, root)
	path := filepath.Join(root, "a.txt")
	testutil.FailErr(t, "write base", os.WriteFile(path, testutil.EncodeTextFixture(t, "one\ntwo\n", textfile.UTF16LE), 0o644))
	p := &project.Project{ID: projectID, Roots: []project.Root{{ID: rootID, ProjectID: projectID, Path: root, IsPrimary: true}}}
	sourceHistory3 := sourceledger.New(sqlDB, "")
	service := New(NewStore(sqlDB), sourceHistory3, sourceHistory3.History, fixedRoots{p: p})
	closeServiceAtCleanup(t, service)
	document, err := service.Open(t.Context(), p, "a.txt", rootID, textfile.UTF16LE, "window", nil)
	testutil.FailErr(t, "open document", err)
	document, err = service.ReplaceSnapshot(t.Context(), document.ID, projectID, SnapshotReplacement{DocumentCommand: DocumentCommand{ClientID: "window", OperationID: uuid.NewString(), ExpectedRevision: document.Revision}, Content: "one\nmy two\n", EOL: "lf", MixedEOL: false})
	testutil.FailErr(t, "update draft", err)
	testutil.FailErr(t, "write external change", os.WriteFile(path, testutil.EncodeTextFixture(t, "ONE\ntwo\n", textfile.UTF16LE), 0o644))

	document, err = service.ObserveDisk(t.Context(), p, document.ID, "window")
	testutil.FailErr(t, "observe disk", err)
	if document.Diverged || document.Encoding != textfile.UTF16LE || document.Draft != "ONE\nmy two\n" {
		t.Fatalf("observed document = %+v", document)
	}
	document, err = service.Reload(t.Context(), p, document.ID, DocumentCommand{ClientID: "window", OperationID: uuid.NewString(), ExpectedRevision: document.Revision})
	testutil.FailErr(t, "reload document", err)
	if document.Diverged || document.Dirty || document.Encoding != textfile.UTF16LE || document.Draft != "ONE\ntwo\n" {
		t.Fatalf("reloaded document = %+v", document)
	}
}

func TestRecoverRetargetMovesOnlyTheRenamedDocumentSubtree(t *testing.T) {
	sqlDB := testdbfixture.Open(t, "store.db")
	root := t.TempDir()
	projectID, rootID := testdbseed.DefaultProjectID, uuid.NewString()
	testdbseed.InsertProjectRootWithID(t, sqlDB, projectID, rootID, root)
	for _, path := range []string{"dir/a.txt", "dir/nested/b.txt", "directory/c.txt"} {
		testutil.FailErr(t, "mkdir", os.MkdirAll(filepath.Dir(filepath.Join(root, path)), 0o755))
		testutil.FailErr(t, "write", os.WriteFile(filepath.Join(root, path), []byte(path), 0o644))
	}
	p := &project.Project{ID: projectID, Roots: []project.Root{{ID: rootID, ProjectID: projectID, Path: root, IsPrimary: true}}}
	sourceHistory4 := sourceledger.New(sqlDB, "")
	service := New(NewStore(sqlDB), sourceHistory4, sourceHistory4.History, fixedRoots{p: p})
	closeServiceAtCleanup(t, service)
	ids := map[string]string{}
	for _, path := range []string{"dir/a.txt", "dir/nested/b.txt", "directory/c.txt"} {
		document, err := service.Open(t.Context(), p, path, rootID, "", "window", nil)
		testutil.FailErr(t, "open "+path, err)
		ids[path] = document.ID
	}
	operationID := uuid.NewString()
	intentID, err := service.PrepareRetarget(t.Context(), p, projectsource.SourceRenamePlan{
		OperationID: operationID, RootID: rootID, From: "dir", To: "moved",
	})
	testutil.FailErr(t, "prepare retarget", err)
	mutations := projectsource.NewSourceMutationService(sqlDB, nil)
	_, err = mutations.Rename(t.Context(), operationID, p, projectsource.SourceRenameRequest{RootID: rootID, From: "dir", To: "moved"})
	testutil.FailErr(t, "rename directory", err)
	if intentID == "" {
		t.Fatal("retarget intent id is empty")
	}
	var changed []string
	sourceHistory5 := sourceledger.New(sqlDB, "")
	recovered := New(NewStore(sqlDB), sourceHistory5, sourceHistory5.History, fixedRoots{p: p})
	closeServiceAtCleanup(t, recovered)
	recovered.SetOnChange(func(_ context.Context, change Change) {
		changed = append(changed, change.Document.Path)
	})
	testutil.FailErr(t, "recover retarget", recovered.Recover(t.Context()))
	for from, path := range map[string]string{"dir/a.txt": "moved/a.txt", "dir/nested/b.txt": "moved/nested/b.txt", "directory/c.txt": "directory/c.txt"} {
		document, err := recovered.store.Get(t.Context(), ids[from])
		testutil.FailErr(t, "read retargeted document", err)
		if document.Path != path {
			t.Fatalf("document path = %q, want %q", document.Path, path)
		}
	}
	if len(changed) != 2 {
		t.Fatalf("changed paths = %v", changed)
	}
}

func TestDocumentFollowsItsRootWhenTheFolderMoves(t *testing.T) {
	sqlDB := testdbfixture.Open(t, "store.db")

	projectID := testdbseed.DefaultProjectID
	rootID := uuid.NewString()
	scratch, folder := t.TempDir(), t.TempDir()
	testdbseed.InsertProjectRootWithID(t, sqlDB, projectID, rootID, scratch)
	testutil.FailErr(t, "seed draft file", os.WriteFile(filepath.Join(scratch, "a.txt"), []byte("first\n"), 0o644))

	draftProject := &project.Project{ID: projectID, Roots: []project.Root{{ID: rootID, ProjectID: projectID, Path: scratch, IsPrimary: true}}}
	savedProject := &project.Project{ID: projectID, Roots: []project.Root{{ID: rootID, ProjectID: projectID, Path: folder, IsPrimary: true}}}
	roots := &movableRoots{p: draftProject}
	recorder := &captureRecorder{Store: sourceledger.New(sqlDB, "")}
	service := New(NewStore(sqlDB), recorder, recorder.History, roots)
	closeServiceAtCleanup(t, service)

	document, err := service.Open(t.Context(), draftProject, "a.txt", rootID, "", "window-1", nil)
	testutil.FailErr(t, "open in the draft", err)
	document, err = service.ReplaceSnapshot(t.Context(), document.ID, projectID, SnapshotReplacement{DocumentCommand: DocumentCommand{ClientID: "window-1", OperationID: uuid.NewString(), ExpectedRevision: document.Revision}, Content: "unsaved edit\n", EOL: "lf", MixedEOL: false})
	testutil.FailErr(t, "type an unsaved edit", err)
	if !document.Dirty {
		t.Fatal("document is not dirty after an edit")
	}

	// Promotion preserves the root ID.
	testutil.FailErr(t, "install promoted bytes", os.WriteFile(filepath.Join(folder, "a.txt"), []byte("first\n"), 0o644))
	_, err = sqlDB.ExecContext(t.Context(), `UPDATE project_roots SET path = ? WHERE id = ?`, folder, rootID)
	testutil.FailErr(t, "move the root", err)
	roots.p = savedProject

	reopened, err := service.Open(t.Context(), savedProject, "a.txt", rootID, "", "window-1", nil)
	testutil.FailErr(t, "reopen after the move", err)
	if reopened.ID != document.ID {
		t.Fatalf("reopen forked the document: %s then %s", document.ID, reopened.ID)
	}
	if !reopened.Dirty || reopened.Draft != "unsaved edit\n" {
		t.Fatalf("unsaved draft lost across the move: dirty=%v draft=%q", reopened.Dirty, reopened.Draft)
	}

	saved, err := service.Save(t.Context(), savedProject, reopened.ID, "window-1", uuid.NewString(), "", 0, reopened.Revision)
	testutil.FailErr(t, "save into the folder", err)
	if saved.Dirty {
		t.Fatal("document still dirty after save")
	}
	landed, err := os.ReadFile(filepath.Join(folder, "a.txt"))
	testutil.FailErr(t, "read the saved folder", err)
	if string(landed) != "unsaved edit\n" {
		t.Fatalf("save landed %q in the folder", landed)
	}
	if scratchBytes, readErr := os.ReadFile(filepath.Join(scratch, "a.txt")); readErr != nil || string(scratchBytes) != "first\n" {
		t.Fatalf("save wrote back to the old folder: %q err=%v", scratchBytes, readErr)
	}
}

type movableRoots struct{ p *project.Project }

func (m *movableRoots) Get(context.Context, string) (*project.Project, error) { return m.p, nil }

func TestDiscardIsOneHostRevision(t *testing.T) {
	sqlDB := testdbfixture.Open(t, "store.db")
	root := t.TempDir()
	projectID, rootID := testdbseed.DefaultProjectID, uuid.NewString()
	testdbseed.InsertProjectRootWithID(t, sqlDB, projectID, rootID, root)
	testutil.FailErr(t, "write", os.WriteFile(filepath.Join(root, "a.txt"), []byte("base\n"), 0o644))
	p := &project.Project{ID: projectID, Roots: []project.Root{{ID: rootID, ProjectID: projectID, Path: root, IsPrimary: true}}}
	sourceHistory6 := sourceledger.New(sqlDB, "")
	service := New(NewStore(sqlDB), sourceHistory6, sourceHistory6.History, fixedRoots{p: p})
	closeServiceAtCleanup(t, service)
	doc, err := service.Open(t.Context(), p, "a.txt", rootID, "", "window", nil)
	testutil.FailErr(t, "open", err)
	doc, err = service.ReplaceSnapshot(t.Context(), doc.ID, projectID, SnapshotReplacement{DocumentCommand: DocumentCommand{ClientID: "window", OperationID: uuid.NewString(), ExpectedRevision: doc.Revision}, Content: "draft\n", EOL: "crlf", MixedEOL: false})
	testutil.FailErr(t, "draft", err)
	discarded, err := service.Discard(t.Context(), doc.ID, projectID, DocumentCommand{ClientID: "window", OperationID: uuid.NewString(), ExpectedRevision: doc.Revision})
	testutil.FailErr(t, "discard", err)
	if discarded.Dirty || discarded.Draft != "base\n" || discarded.EOL != discarded.BaseEOL {
		t.Fatalf("discarded = %+v", discarded)
	}
}

func TestRecoverCompletesSaveAcrossFilesystemCommitBoundary(t *testing.T) {
	for _, fileAlreadyApplied := range []bool{false, true} {
		name := "before_file_commit"
		if fileAlreadyApplied {
			name = "after_file_commit"
		}
		t.Run(name, func(t *testing.T) {
			sqlDB := testdbfixture.Open(t, "store.db")
			root := t.TempDir()
			projectID, rootID := testdbseed.DefaultProjectID, uuid.NewString()
			testdbseed.InsertProjectRootWithID(t, sqlDB, projectID, rootID, root)
			path := filepath.Join(root, "a.txt")
			before, after := []byte("base\n"), []byte("draft\n")
			testutil.FailErr(t, "write base", os.WriteFile(path, before, 0o644))

			p := &project.Project{ID: projectID, Roots: []project.Root{{ID: rootID, ProjectID: projectID, Path: root, IsPrimary: true}}}
			recorder := &captureRecorder{Store: sourceledger.New(sqlDB, "")}
			store := NewStore(sqlDB)
			service := New(store, recorder, recorder.History, fixedRoots{p: p})
			closeServiceAtCleanup(t, service)
			document, err := service.Open(t.Context(), p, "a.txt", rootID, "", "window", nil)
			testutil.FailErr(t, "open", err)
			document, err = service.ReplaceSnapshot(t.Context(), document.ID, projectID, SnapshotReplacement{DocumentCommand: DocumentCommand{ClientID: "window", OperationID: uuid.NewString(), ExpectedRevision: document.Revision}, Content: string(after), EOL: "lf", MixedEOL: false})
			testutil.FailErr(t, "draft", err)
			if fileAlreadyApplied {
				testutil.FailErr(t, "apply file", os.WriteFile(path, after, 0o644))
			}
			now := time.Now().UTC()
			person, err := store.people.HostOwner(t.Context())
			testutil.FailErr(t, "read saving person", err)
			mutation := &Mutation{
				ID: uuid.NewString(), DocumentID: document.ID, ProjectID: projectID,
				FileID: document.FileID, RootID: rootID, Path: "a.txt",
				ExpectedSHA256: document.BaseSHA256, AfterSHA256: textfile.SHA256(after),
				Encoding: document.Encoding, Content: string(after),
				DraftRevision: document.Revision, EOL: document.EOL,
				BeforeBytes: before, AfterBytes: after,
				Status: "prepared", CreatedAt: now, UpdatedAt: now, Origin: api.SourceChangeOriginUser, PersonID: person.ID,
			}
			testutil.FailErr(t, "insert mutation", store.InsertMutation(t.Context(), mutation))

			testutil.FailErr(t, "recover", service.Recover(t.Context()))
			recovered, err := store.Get(t.Context(), document.ID)
			testutil.FailErr(t, "read document", err)
			completed, err := store.Mutation(t.Context(), mutation.ID)
			testutil.FailErr(t, "read mutation", err)
			raw, err := os.ReadFile(path)
			testutil.FailErr(t, "read file", err)
			if string(raw) != string(after) || recovered.Dirty || recovered.BaseSHA256 != mutation.AfterSHA256 {
				t.Fatalf("incomplete recovery: file=%q document=%+v", raw, recovered)
			}
			if completed.Status != "complete" || len(recorder.records) != 1 {
				t.Fatalf("mutation=%+v records=%d", completed, len(recorder.records))
			}
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

func closeServiceAtCleanup(t *testing.T, service *Service) {
	t.Helper()
	t.Cleanup(func() { testutil.FailErr(t, "close document service", service.Close(context.Background())) })
}
