package api

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/lycaon/lycaon/internal/editordoc"
	"github.com/lycaon/lycaon/internal/project"
	"github.com/lycaon/lycaon/internal/repochange"
	"github.com/lycaon/lycaon/internal/sourcecatalog"
	"github.com/lycaon/lycaon/internal/sourcefeed"
	"github.com/lycaon/lycaon/internal/sourceledger"
	"github.com/lycaon/lycaon/internal/testdbfixture"
	"github.com/lycaon/lycaon/internal/testdbseed"
	"github.com/lycaon/lycaon/internal/testutil"
)

func TestExternalWatchRevalidatesDirectoriesAndHeadOnlyBatches(t *testing.T) {
	for _, headOnly := range []bool{false, true} {
		t.Run(map[bool]string{false: "directory", true: "head only"}[headOnly], func(t *testing.T) {
			database := testdbfixture.Open(t, "editor.db")
			var documents *editordoc.Service
			srv := newTestServer(t, func(d *Dependencies) {
				sourceHistory15 := sourceledger.New(database, "")
				documents = editordoc.New(editordoc.NewStore(database), sourceHistory15, sourceHistory15.History, d.Core.Projects)
				d.Source.EditorDocuments = documents
			})
			root := t.TempDir()
			p, err := project.CreateWithRoot(t.Context(), srv.Sources.Workspace.ProjectRegistry, root)
			testutil.FailErr(t, "create project", err)
			testdbseed.InsertProjectRootWithID(t, database, p.ID, p.Roots[0].ID, root)
			testutil.FailErr(t, "create directory", os.Mkdir(filepath.Join(root, "src"), 0o755))
			file := filepath.Join(root, "src", "notes.txt")
			testutil.FailErr(t, "write initial", os.WriteFile(file, []byte("before"), 0o644))
			document, err := documents.Open(t.Context(), p, "src/notes.txt", p.Roots[0].ID, "", "client", nil)
			testutil.FailErr(t, "open document", err)
			testutil.FailErr(t, "write external", os.WriteFile(file, []byte("after"), 0o644))
			isDir := true
			batch := sourcefeed.ExternalBatch{Changes: []sourcefeed.Change{{RootID: p.Roots[0].ID, Path: "src", IsDir: &isDir}}}
			if headOnly {
				batch = sourcefeed.ExternalBatch{HeadMoved: true}
			}
			srv.Sources.Watch.RevalidateEditorDocuments(t.Context(), p, batch)
			srv.background.Wait(context.Background())
			current, err := documents.CurrentSnapshot(t.Context(), p.ID, document.ID)
			testutil.FailErr(t, "load current document", err)
			if current.Draft != "after" {
				t.Fatalf("external change left draft %q", current.Draft)
			}
		})
	}
}

// Observation is live before any walk: the watcher binds while the catalog
// walk that seeds per-directory coverage is still running.
func TestEnsureSourceWatchBindsBeforeTheCatalogWalk(t *testing.T) {
	walkStarted := make(chan struct{})
	release := make(chan struct{})
	srv := newTestServer(t, func(d *Dependencies) {
		d.Source.WatchNeedsSeed = func(string) bool { return true }
		d.Source.CatalogSnapshot = func(ctx context.Context, _ string, _ []sourcecatalog.Root) (sourcecatalog.Snapshot, error) {
			close(walkStarted)
			select {
			case <-release:
			case <-ctx.Done():
			}
			return sourcecatalog.Snapshot{}, context.Canceled
		}
	})
	dir := t.TempDir()
	p, err := project.CreateWithRoot(t.Context(), srv.Sources.Workspace.ProjectRegistry, dir)
	testutil.FailErr(t, "create project", err)
	t.Cleanup(func() { sourcefeed.StopProjectWatch(context.Background(), p.ID) })

	done := make(chan struct{})
	go func() {
		defer close(done)
		srv.Sources.Watch.EnsureSourceWatch(t.Context(), p.ID)
	}()
	select {
	case <-walkStarted:
	case <-time.After(5 * time.Second):
		t.Fatal("catalog walk never started")
	}
	// The walk is blocked; the root must already be observed.
	coverage := repochange.Coverage(p.Roots[0].Path)
	if !coverage.Watching {
		t.Fatalf("root is not watched while the catalog walk runs: %+v", coverage)
	}
	close(release)
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("ensureSourceWatch did not return after the walk released")
	}
}
