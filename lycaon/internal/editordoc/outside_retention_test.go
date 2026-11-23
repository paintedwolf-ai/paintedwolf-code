//go:build darwin || linux

package editordoc

import (
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/google/uuid"
	"github.com/lycaon/lycaon/internal/db"
	"github.com/lycaon/lycaon/internal/project"
	"github.com/lycaon/lycaon/internal/projectroot"
	"github.com/lycaon/lycaon/internal/projectsource"
	"github.com/lycaon/lycaon/internal/sourcebranch"
	"github.com/lycaon/lycaon/internal/testutil"
)

func TestClosingTheLastOutsideTabReleasesItsUnreferencedFile(t *testing.T) {
	f := newExternalFixture(t, nil)
	ctx := t.Context()
	folder, err := filepath.EvalSymlinks(t.TempDir())
	testutil.FailErr(t, "resolve fixture folder", err)
	name := filepath.Join(folder, "outside.txt")
	testutil.FailErr(t, "write outside file", os.WriteFile(name, []byte("live text"), 0o600))
	rel, ok := projectroot.OutsideRel(name)
	if !ok {
		t.Fatal("fixture lacks an outside address")
	}
	rootID, err := db.New(f.store.db).GetProjectOutsideRootID(ctx, f.project.ID)
	testutil.FailErr(t, "resolve outside namespace", err)
	f.project.Outside = project.Root{ID: rootID, Path: projectroot.OutsidePath, Kind: project.RootKindOutside}
	p := project.WithOutside(f.project)
	one, two := "window:one", "window:two"
	first, err := f.service.Open(ctx, p, rel, rootID, "", one, nil)
	testutil.FailErr(t, "open first tab", err)
	second, err := f.service.Open(ctx, p, rel, rootID, "", two, nil)
	testutil.FailErr(t, "open second tab", err)
	if first.ID != second.ID {
		t.Fatalf("tabs hold different documents: %s and %s", first.ID, second.ID)
	}

	testutil.FailErr(t, "leave first tab", f.service.Leave(ctx, first.ID, p.ID, one, ""))
	testutil.FailErr(t, "release first tab", f.service.ReplaceRetention(ctx, p.ID, one, nil, nil, nil))
	if _, err := f.store.Get(ctx, first.ID); err != nil {
		t.Fatalf("second tab lost the document: %v", err)
	}
	testutil.FailErr(t, "leave second tab", f.service.Leave(ctx, second.ID, p.ID, two, ""))
	testutil.FailErr(t, "release second tab", f.service.ReplaceRetention(ctx, p.ID, two, nil, nil, nil))
	if _, err := f.store.Get(ctx, second.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("closed, unreferenced document = %v", err)
	}
	if _, err := f.service.history.ResolveHead(ctx, p.ID, sourcebranch.Trunk, rootID, rel); err == nil {
		t.Fatal("closing the last tab left an unreferenced outside file")
	}
	data, err := os.ReadFile(name)
	testutil.FailErr(t, "read original disk file", err)
	if string(data) != "live text" {
		t.Fatalf("closing a tab changed the disk file: %q", data)
	}
}

func TestOutsideSaveRejectsAReplacementParent(t *testing.T) {
	f := newExternalFixture(t, nil)
	ctx := t.Context()
	parent, err := filepath.EvalSymlinks(t.TempDir())
	testutil.FailErr(t, "resolve outside fixture", err)
	dir := filepath.Join(parent, "observed")
	testutil.FailErr(t, "make outside parent", os.Mkdir(dir, 0o700))
	name := filepath.Join(dir, "outside.txt")
	testutil.FailErr(t, "write original source", os.WriteFile(name, []byte("original"), 0o600))
	rel, ok := projectroot.OutsideRel(name)
	if !ok {
		t.Fatal("fixture lacks an outside address")
	}
	rootID, err := db.New(f.store.db).GetProjectOutsideRootID(ctx, f.project.ID)
	testutil.FailErr(t, "resolve namespace", err)
	f.project.Outside = project.Root{ID: rootID, Path: projectroot.OutsidePath, Kind: project.RootKindOutside}
	const client = "window"
	d, err := f.service.Open(ctx, f.project, rel, rootID, "", client, nil)
	testutil.FailErr(t, "open outside document", err)
	edit := func(d *Document, content string) *Document {
		next, err := f.service.ReplaceSnapshot(ctx, d.ID, f.project.ID, SnapshotReplacement{
			DocumentCommand: DocumentCommand{ClientID: client, OperationID: uuid.NewString(), ExpectedRevision: d.Revision},
			Content:         content, EOL: "lf",
		})
		testutil.FailErr(t, "accept draft", err)
		return next
	}
	d = edit(d, "saved")
	d, err = f.service.Save(ctx, f.project, d.ID, client, uuid.NewString(), "", 0, d.Revision)
	testutil.FailErr(t, "save in the observed directory", err)
	d = edit(d, "unsaved")
	testutil.FailErr(t, "move observed directory", os.Rename(dir, dir+"-old"))
	testutil.FailErr(t, "make replacement directory", os.Mkdir(dir, 0o700))
	testutil.FailErr(t, "put the same bytes in the replacement", os.WriteFile(name, []byte("saved"), 0o600))
	_, err = f.service.Save(ctx, f.project, d.ID, client, uuid.NewString(), "", 0, d.Revision)
	if !errors.Is(err, projectsource.ErrSourceWriteConflict) {
		t.Fatalf("replacement directory received a save: %v", err)
	}
	read, err := os.ReadFile(name)
	testutil.FailErr(t, "read replacement", err)
	if string(read) != "saved" {
		t.Fatalf("replacement bytes changed to %q", read)
	}
	remaining, err := f.store.Get(ctx, d.ID)
	testutil.FailErr(t, "read retained draft", err)
	if !remaining.Dirty || remaining.Draft != "unsaved" {
		t.Fatalf("refused save lost the draft: %+v", remaining)
	}
}
