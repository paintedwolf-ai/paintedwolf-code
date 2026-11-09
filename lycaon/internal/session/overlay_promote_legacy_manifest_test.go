package session

import (
	"database/sql"
	"os"
	"path/filepath"
	"slices"
	"testing"

	"github.com/lycaon/lycaon/internal/projectroot"
	"github.com/lycaon/lycaon/internal/sourceblob"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/workspacebaseline"
	"github.com/lycaon/lycaon/pkg/api"
	_ "modernc.org/sqlite"
)

// An in-flight task whose baseline manifest was written in the legacy
// display-keyed format promotes the same paths as the current format.
func TestInspectOverlayChangesReadsLegacyManifests(t *testing.T) {
	primary, secondary, branch := t.TempDir(), t.TempDir(), t.TempDir()
	roots := []projectroot.RootRef{
		{ID: "p", Label: "app", Path: primary, IsPrimary: true},
		{ID: "s", Label: "shared", Path: secondary},
	}
	branchDirs := map[string]string{}
	for _, root := range roots {
		dir, err := projectroot.BranchDirForID(root.ID)
		testutil.FailErr(t, "branch dir", err)
		branchDirs[root.ID] = dir
		testutil.FailErr(t, "mkdir branch root", os.MkdirAll(filepath.Join(branch, dir), 0o750))
	}
	testutil.FailErr(t, "seed primary", os.WriteFile(filepath.Join(branch, branchDirs["p"], "keep.txt"), []byte("keep\n"), 0o600))
	testutil.FailErr(t, "seed secondary", os.WriteFile(filepath.Join(branch, branchDirs["s"], "same.txt"), []byte("same\n"), 0o600))

	root := t.TempDir()
	store := workspacebaseline.New(nil, sourceblob.New(filepath.Join(root, "source-content")), filepath.Join(root, "manifests"))
	ref, err := store.Capture(t.Context(), "job", workspacebaseline.Branch(roots, branch))
	testutil.FailErr(t, "capture baseline", err)
	current, err := workspacebaseline.Open(t.Context(), ref, workspacebaseline.ContentStore(ref))
	testutil.FailErr(t, "open baseline", err)

	legacyPath := filepath.Join(root, "legacy.db")
	database, err := sql.Open("sqlite", legacyPath)
	testutil.FailErr(t, "open legacy fixture", err)
	_, err = database.ExecContext(t.Context(), `CREATE TABLE manifest(version INTEGER PRIMARY KEY CHECK(version=1)) STRICT;
 CREATE TABLE files(path TEXT PRIMARY KEY, size INTEGER NOT NULL, mtime_ns INTEGER NOT NULL, sha256 TEXT NOT NULL, opaque INTEGER NOT NULL, mode INTEGER NOT NULL, link_target TEXT NOT NULL, deleted INTEGER NOT NULL) STRICT, WITHOUT ROWID;
 INSERT INTO manifest VALUES(1);`)
	testutil.FailErr(t, "create legacy fixture", err)
	err = current.EachIdentity(t.Context(), roots, func(entry workspacebaseline.Entry) error {
		_, err := database.ExecContext(t.Context(), "INSERT INTO files(path,size,mtime_ns,sha256,opaque,mode,link_target,deleted) VALUES (?,?,?,?,?,?,?,?)",
			entry.Path, entry.File.Size, entry.File.MtimeNano, entry.File.SHA256, entry.File.Opaque, entry.File.Mode, entry.File.LinkTarget, entry.File.Deleted)
		return err
	})
	testutil.FailErr(t, "fill legacy fixture", err)
	testutil.FailErr(t, "close legacy fixture", database.Close())
	testutil.FailErr(t, "close baseline", current.Close())

	// The worker edits the primary file, rewrites the shared file, and adds a
	// new shared file.
	testutil.FailErr(t, "edit primary", os.WriteFile(filepath.Join(branch, branchDirs["p"], "keep.txt"), []byte("changed\n"), 0o600))
	testutil.FailErr(t, "rewrite shared", os.WriteFile(filepath.Join(branch, branchDirs["s"], "same.txt"), []byte("changed\n"), 0o600))
	testutil.FailErr(t, "add shared", os.WriteFile(filepath.Join(branch, branchDirs["s"], "made.txt"), []byte("made\n"), 0o600))

	currentTask := &api.WorkerTask{WorkspaceRoot: branch, WorkspaceBaselinePath: ref}
	legacyTask := &api.WorkerTask{WorkspaceRoot: branch, WorkspaceBaselinePath: legacyPath}
	want, err := InspectOverlayChanges(t.Context(), currentTask, roots)
	testutil.FailErr(t, "inspect current baseline", err)
	got, err := InspectOverlayChanges(t.Context(), legacyTask, roots)
	testutil.FailErr(t, "inspect legacy baseline", err)
	if !slices.Equal(got, want) {
		t.Fatalf("legacy promotion paths = %v, want %v", got, want)
	}
	if !slices.Equal(got, []string{"@shared/made.txt", "@shared/same.txt", "keep.txt"}) {
		t.Fatalf("promotion paths = %v", got)
	}
}
