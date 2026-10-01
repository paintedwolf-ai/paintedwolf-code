package editordoc

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/google/uuid"
	"github.com/lycaon/lycaon/internal/project"
	"github.com/lycaon/lycaon/internal/sourceledger"
	"github.com/lycaon/lycaon/internal/testdbfixture"
	"github.com/lycaon/lycaon/internal/testdbseed"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/textfile"
)

type externalFixture struct {
	service *Service
	store   *Store
	project *project.Project
	rootID  string
	root    string
	changes *[]Change
}

func newExternalFixture(t *testing.T, files map[string]string) externalFixture {
	t.Helper()
	sqlDB := testdbfixture.Open(t, "store.db")
	root := t.TempDir()
	projectID, rootID := testdbseed.DefaultProjectID, uuid.NewString()
	testdbseed.InsertProjectRootWithID(t, sqlDB, projectID, rootID, root)
	for name, content := range files {
		testutil.FailErr(t, "write "+name, os.WriteFile(filepath.Join(root, name), []byte(content), 0o644))
	}
	p := &project.Project{ID: projectID, Roots: []project.Root{{ID: rootID, ProjectID: projectID, Path: root, IsPrimary: true}}}
	store := NewStore(sqlDB)
	service := New(store, sourceledger.New(sqlDB, ""), fixedRoots{p: p})
	closeServiceAtCleanup(t, service)
	changes := &[]Change{}
	service.SetOnChange(func(_ context.Context, c Change) { *changes = append(*changes, c) })
	return externalFixture{service: service, store: store, project: p, rootID: rootID, root: root, changes: changes}
}

func (f externalFixture) open(t *testing.T, name string) *Document {
	t.Helper()
	document, err := f.service.Open(t.Context(), f.project, name, f.rootID, "", "", nil)
	testutil.FailErr(t, "open "+name, err)
	return document
}

func (f externalFixture) write(t *testing.T, name, content string) {
	t.Helper()
	testutil.FailErr(t, "write outside "+name, os.WriteFile(filepath.Join(f.root, name), []byte(content), 0o644))
}

func TestObserveExternalRebasesCleanDocumentOnDiskChange(t *testing.T) {
	f := newExternalFixture(t, map[string]string{"a.txt": "base\n"})
	document := f.open(t, "a.txt")
	*f.changes = nil
	f.write(t, "a.txt", "outside\n")

	testutil.FailErr(t, "observe", f.service.ObserveExternal(t.Context(), f.project, []PathRef{{RootID: f.rootID, Path: "a.txt"}}))

	stored, err := f.store.Get(t.Context(), document.ID)
	testutil.FailErr(t, "reload document", err)
	if stored.Draft != "outside\n" || stored.BaseContent != "outside\n" || stored.Dirty || stored.Diverged {
		t.Fatalf("clean document did not take the file's bytes: %+v", stored)
	}
	if stored.Revision != document.Revision+1 {
		t.Fatalf("revision = %d, want %d", stored.Revision, document.Revision+1)
	}
	if len(*f.changes) != 1 || !(*f.changes)[0].ContentChanged || (*f.changes)[0].Document.Draft != "outside\n" {
		t.Fatalf("change projection = %+v", *f.changes)
	}
}

func TestWatcherAndExplicitObservationShareOneDocumentTransition(t *testing.T) {
	for _, watcherFirst := range []bool{false, true} {
		t.Run(map[bool]string{false: "request first", true: "watcher first"}[watcherFirst], func(t *testing.T) {
			f := newExternalFixture(t, map[string]string{"a.txt": "base\n"})
			d := f.open(t, "a.txt")
			*f.changes = nil
			f.write(t, "a.txt", "outside\n")
			watch := func() {
				testutil.FailErr(t, "watch observation", f.service.ObserveExternal(t.Context(), f.project, nil))
			}
			if watcherFirst {
				watch()
			}
			observed, err := f.service.ObserveDisk(t.Context(), f.project, d.ID, "window")
			testutil.FailErr(t, "request observation", err)
			if !watcherFirst {
				watch()
			}
			if observed.Draft != "outside\n" || observed.Dirty || observed.Diverged || observed.Revision != d.Revision+1 {
				t.Fatalf("observation = %+v", observed)
			}
			if len(*f.changes) != 1 || !(*f.changes)[0].ContentChanged {
				t.Fatalf("document transitions = %+v", *f.changes)
			}
		})
	}
}

func TestObserveExternalMergesDirtyDraft(t *testing.T) {
	f := newExternalFixture(t, map[string]string{"a.txt": "one\ntwo\n"})
	document := f.open(t, "a.txt")
	document, err := f.service.ReplaceSnapshot(t.Context(), document.ID, f.project.ID, SnapshotReplacement{DocumentCommand: DocumentCommand{ClientID: "window", OperationID: uuid.NewString(), ExpectedRevision: document.Revision}, Content: "one\nmy two\n", EOL: "lf", MixedEOL: false})
	testutil.FailErr(t, "update draft", err)
	*f.changes = nil
	f.write(t, "a.txt", "ONE\ntwo\n")

	testutil.FailErr(t, "observe", f.service.ObserveExternal(t.Context(), f.project, nil))

	stored, err := f.store.Get(t.Context(), document.ID)
	testutil.FailErr(t, "reload document", err)
	if stored.Draft != "ONE\nmy two\n" || !stored.Dirty || stored.Diverged {
		t.Fatalf("dirty document did not merge outside edit: %+v", stored)
	}
	if len(*f.changes) != 1 || !(*f.changes)[0].ContentChanged {
		t.Fatalf("change projection = %+v", *f.changes)
	}
}

func TestObservationPreservesDraftWhenDiskEncodingBecomesUnsupported(t *testing.T) {
	for _, dirty := range []bool{false, true} {
		t.Run(map[bool]string{false: "clean", true: "dirty"}[dirty], func(t *testing.T) {
			f := newExternalFixture(t, map[string]string{"a.txt": "base\n"})
			d := f.open(t, "a.txt")
			if dirty {
				var err error
				d, err = f.service.ReplaceSnapshot(t.Context(), d.ID, f.project.ID, SnapshotReplacement{DocumentCommand: DocumentCommand{ClientID: "window", OperationID: uuid.NewString(), ExpectedRevision: d.Revision}, Content: "draft\n", EOL: "lf", MixedEOL: false})
				testutil.FailErr(t, "type draft", err)
			}
			*f.changes = nil
			f.write(t, "a.txt", string([]byte{0x63, 0x61, 0x66, 0xe9}))
			testutil.FailErr(t, "watch unsupported encoding", f.service.ObserveExternal(t.Context(), f.project, nil))
			observed, err := f.service.ObserveDisk(t.Context(), f.project, d.ID, "window")
			testutil.FailErr(t, "observe unsupported encoding", err)
			if observed.Draft != d.Draft || observed.BaseSHA256 != d.BaseSHA256 || observed.Dirty != dirty || !observed.Diverged {
				t.Fatalf("unsupported source changed document contents: %+v", observed)
			}
			if len(*f.changes) != 1 || (*f.changes)[0].ContentChanged {
				t.Fatalf("unsupported source transitions = %+v", *f.changes)
			}
			f.write(t, "a.txt", "base\n")
			observed, err = f.service.ObserveDisk(t.Context(), f.project, d.ID, "window")
			testutil.FailErr(t, "observe restored encoding", err)
			if observed.Diverged || observed.Draft != d.Draft || observed.Dirty != dirty {
				t.Fatalf("restored source did not settle divergence: %+v", observed)
			}
		})
	}
}

func TestObserveExternalSelectionLeavesOtherDocumentsAlone(t *testing.T) {
	f := newExternalFixture(t, map[string]string{"a.txt": "a\n", "b.txt": "b\n"})
	a := f.open(t, "a.txt")
	b := f.open(t, "b.txt")
	f.write(t, "a.txt", "a2\n")
	f.write(t, "b.txt", "b2\n")

	testutil.FailErr(t, "observe a", f.service.ObserveExternal(t.Context(), f.project, []PathRef{{RootID: f.rootID, Path: "a.txt"}}))

	storedA, err := f.store.Get(t.Context(), a.ID)
	testutil.FailErr(t, "reload a", err)
	storedB, err := f.store.Get(t.Context(), b.ID)
	testutil.FailErr(t, "reload b", err)
	if storedA.Draft != "a2\n" || storedB.Draft != "b\n" || storedB.Revision != b.Revision {
		t.Fatalf("selection leaked: a=%q b=%q", storedA.Draft, storedB.Draft)
	}

	testutil.FailErr(t, "observe every document", f.service.ObserveExternal(t.Context(), f.project, nil))
	storedB, err = f.store.Get(t.Context(), b.ID)
	testutil.FailErr(t, "reload b again", err)
	if storedB.Draft != "b2\n" {
		t.Fatalf("resync did not cover b: %q", storedB.Draft)
	}
}

func TestObserveExternalIsQuietWhenDiskMatches(t *testing.T) {
	f := newExternalFixture(t, map[string]string{"a.txt": "base\n"})
	document := f.open(t, "a.txt")
	*f.changes = nil

	testutil.FailErr(t, "observe", f.service.ObserveExternal(t.Context(), f.project, nil))

	stored, err := f.store.Get(t.Context(), document.ID)
	testutil.FailErr(t, "reload document", err)
	if stored.Revision != document.Revision || len(*f.changes) != 0 {
		t.Fatalf("unchanged file moved the document: revision=%d changes=%d", stored.Revision, len(*f.changes))
	}
}

func TestObserveExternalDirectorySelectionCoversOnlyDescendants(t *testing.T) {
	f := newExternalFixture(t, nil)
	for _, dir := range []string{"src/deep", "src-other"} {
		testutil.FailErr(t, "create directory", os.MkdirAll(filepath.Join(f.root, dir), 0o755))
	}
	paths := []string{"src/clean.txt", "src/deep/dirty.txt", "src-other/keep.txt"}
	for _, name := range paths {
		f.write(t, name, "one\ntwo\n")
	}
	clean, dirty, sibling := f.open(t, paths[0]), f.open(t, paths[1]), f.open(t, paths[2])
	_, err := f.service.ReplaceSnapshot(t.Context(), dirty.ID, f.project.ID, SnapshotReplacement{DocumentCommand: DocumentCommand{ClientID: "window", OperationID: uuid.NewString(), ExpectedRevision: dirty.Revision}, Content: "one\nmy two\n", EOL: "lf", MixedEOL: false})
	testutil.FailErr(t, "hold dirty draft", err)
	for _, name := range paths {
		f.write(t, name, "ONE\ntwo\n")
	}
	testutil.FailErr(t, "observe directory", f.service.ObserveExternal(t.Context(), f.project, []PathRef{{RootID: f.rootID, Path: "src"}}))
	for _, want := range []struct {
		id, draft string
		diverged  bool
	}{{clean.ID, "ONE\ntwo\n", false}, {dirty.ID, "ONE\nmy two\n", false}, {sibling.ID, "one\ntwo\n", false}} {
		stored, err := f.store.Get(t.Context(), want.id)
		testutil.FailErr(t, "reload descendant", err)
		if stored.Draft != want.draft || stored.Diverged != want.diverged {
			t.Fatalf("directory observation: draft=%q diverged=%v, want %+v", stored.Draft, stored.Diverged, want)
		}
	}
}

func TestObserveExternalMarksARemovedFileAbsentAndKeepsTheDraft(t *testing.T) {
	f := newExternalFixture(t, map[string]string{"a.txt": "base\n"})
	document := f.open(t, "a.txt")
	testutil.FailErr(t, "remove", os.Remove(filepath.Join(f.root, "a.txt")))

	testutil.FailErr(t, "observe", f.service.ObserveExternal(t.Context(), f.project, nil))

	stored, err := f.store.Get(t.Context(), document.ID)
	testutil.FailErr(t, "reload document", err)
	if stored.Draft != "base\n" || !stored.Absent || stored.Diverged || stored.Revision != document.Revision+1 {
		t.Fatalf("a removed file did not become an absent document: %+v", stored)
	}
	// Observing the same absence again is not a transition.
	testutil.FailErr(t, "observe again", f.service.ObserveExternal(t.Context(), f.project, nil))
	again, err := f.store.Get(t.Context(), document.ID)
	testutil.FailErr(t, "reload document again", err)
	if again.Revision != stored.Revision {
		t.Fatalf("repeated absence moved the document: %d -> %d", stored.Revision, again.Revision)
	}
}

func TestOpenTextReturnsTheOpenDocument(t *testing.T) {
	f := newExternalFixture(t, map[string]string{"a.txt": "base\n"})
	document := f.open(t, "a.txt")
	_, err := f.service.ReplaceSnapshot(t.Context(), document.ID, f.project.ID, SnapshotReplacement{DocumentCommand: DocumentCommand{ClientID: "window", OperationID: uuid.NewString(), ExpectedRevision: document.Revision}, Content: "typed\n", EOL: "lf", MixedEOL: false})
	testutil.FailErr(t, "update draft", err)

	open, ok, err := f.service.OpenText(t.Context(), f.project.ID, f.project.SourceBranch, f.rootID, "a.txt")
	testutil.FailErr(t, "open text for open path", err)
	if !ok || open.Draft != "typed\n" || !open.Dirty {
		t.Fatalf("document = %+v ok=%v", open, ok)
	}
	_, ok, err = f.service.OpenText(t.Context(), f.project.ID, f.project.SourceBranch, f.rootID, "missing.txt")
	testutil.FailErr(t, "open text for closed path", err)
	if ok {
		t.Fatal("a path with no document reported one")
	}
}

func TestObserveExternalFollowsEncodingChangesWithoutLosingText(t *testing.T) {
	for _, initial := range []string{textfile.UTF16LE, textfile.UTF16BE} {
		t.Run(initial, func(t *testing.T) {
			f := newExternalFixture(t, map[string]string{"a.txt": ""})
			testutil.FailErr(t, "write wide fixture", os.WriteFile(filepath.Join(f.root, "a.txt"), testutil.EncodeTextFixture(t, "initial\n", initial), 0o644))
			document, err := f.service.Open(t.Context(), f.project, "a.txt", f.rootID, initial, "window", nil)
			testutil.FailErr(t, "open wide fixture", err)
			for _, encoding := range []string{initial, textfile.UTF8, textfile.UTF16LEBOM, textfile.UTF16BEBOM} {
				content := "changed to " + encoding + "\n"
				testutil.FailErr(t, "write external encoding", os.WriteFile(filepath.Join(f.root, "a.txt"), testutil.EncodeTextFixture(t, content, encoding), 0o644))
				testutil.FailErr(t, "observe encoding", f.service.ObserveExternal(t.Context(), f.project, nil))
				stored, getErr := f.store.Get(t.Context(), document.ID)
				testutil.FailErr(t, "read changed document", getErr)
				if stored.Encoding != encoding || stored.Draft != content || stored.Dirty || stored.Diverged {
					t.Fatalf("encoding transition: got encoding=%s draft=%q dirty=%v diverged=%v", stored.Encoding, stored.Draft, stored.Dirty, stored.Diverged)
				}
			}
		})
	}
}
