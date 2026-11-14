package session

// Workspace baselines key every root the same way: snapshots, promotion
// manifests and the overlay tree agree across roots and across the legacy
// manifest format.

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"testing"

	"github.com/lycaon/lycaon/internal/projectroot"
	"github.com/lycaon/lycaon/internal/sourceblob"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/tools"
	"github.com/lycaon/lycaon/internal/workspacebaseline"
	"github.com/lycaon/lycaon/pkg/api"
	_ "modernc.org/sqlite"
)

// Workspace snapshots spell every root in baseline grammar (primary bare,
// others "@label/rel") regardless of which root the session has active.
func TestSnapshotWorkspaceFromRootsKeysBaselineGrammar(t *testing.T) {
	primary, secondary := t.TempDir(), t.TempDir()
	testutil.FailErr(t, "seed primary", os.WriteFile(filepath.Join(primary, "x.txt"), []byte("primary\n"), 0o600))
	testutil.FailErr(t, "seed secondary", os.WriteFile(filepath.Join(secondary, "y.txt"), []byte("shared\n"), 0o600))
	roots := []projectroot.RootRef{
		{ID: "p", Label: "app", Path: primary, IsPrimary: true},
		{ID: "s", Label: "shared", Path: secondary},
	}

	snapshot, err := SnapshotWorkspaceFromRoots(roots)
	testutil.FailErr(t, "snapshot workspace", err)
	keys := make([]string, 0, len(snapshot))
	for key := range snapshot {
		keys = append(keys, key)
	}
	slices.Sort(keys)
	if !slices.Equal(keys, []string{"@shared/y.txt", "x.txt"}) {
		t.Fatalf("snapshot keys = %v", keys)
	}

	branch := t.TempDir()
	branchFiles := map[string]string{"p": "x.txt", "s": "y.txt"}
	branchContent := map[string]string{"p": "primary\n", "s": "shared\n"}
	for _, root := range roots {
		dir, err := projectroot.BranchDirForID(root.ID)
		testutil.FailErr(t, "branch dir", err)
		testutil.FailErr(t, "mkdir branch root", os.MkdirAll(filepath.Join(branch, dir), 0o750))
		target := filepath.Join(branch, dir, branchFiles[root.ID])
		testutil.FailErr(t, "seed branch file", os.WriteFile(target, []byte(branchContent[root.ID]), 0o600))
		// Fingerprints are mtime-based; an untouched branch copy keeps the
		// project file's timestamps.
		info, err := os.Stat(filepath.Join(root.Path, branchFiles[root.ID]))
		testutil.FailErr(t, "stat project file", err)
		testutil.FailErr(t, "preserve mtime", os.Chtimes(target, info.ModTime(), info.ModTime()))
	}
	secondaryDir, err := projectroot.BranchDirForID("s")
	testutil.FailErr(t, "secondary dir", err)
	testutil.FailErr(t, "change branch file", os.WriteFile(filepath.Join(branch, secondaryDir, "y.txt"), []byte("changed\n"), 0o600))

	changed := DiffBranchWorkspaceSnapshot(roots, branch, snapshot)
	if !slices.Equal(changed, []string{"@shared/y.txt"}) {
		t.Fatalf("changed = %v", changed)
	}
}

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

func TestOverlayTreeReadsThePathsPromotionConsiders(t *testing.T) {
	data := t.TempDir()
	branch := filepath.Join(data, "branch")
	testutil.FailErr(t, "mkdir", os.MkdirAll(filepath.Join(branch, ".paintedwolf"), 0o755))
	testutil.FailErr(t, "seed", os.WriteFile(filepath.Join(branch, "main.go"), []byte("old\n"), 0o644))
	blobs := sourceblob.New(filepath.Join(data, "source-content"))
	store := workspacebaseline.New(nil, blobs, filepath.Join(data, "manifests"))
	manifest, err := store.Capture(context.Background(), "job", workspacebaseline.Branch(nil, branch))
	testutil.FailErr(t, "capture", err)

	testutil.FailErr(t, "gen", os.WriteFile(filepath.Join(branch, "gen.txt"), []byte("gen\n"), 0o644))
	testutil.FailErr(t, "metadata", os.WriteFile(filepath.Join(branch, ".paintedwolf", "approvals.yaml"), []byte("rules: []\n"), 0o644))
	tree := tools.SourceTree{BaselinePath: manifest, BranchRoot: branch, Roots: []projectroot.RootRef{{ID: "root", Path: "/project", IsPrimary: true}}}

	changes, err := OverlayTree{}.TreeChanges(t.Context(), tree)
	testutil.FailErr(t, "tree changes", err)
	if fmt.Sprint(changes) != fmt.Sprint([]workspacebaseline.Change{{RootID: "root", ScopeRel: "gen.txt", Path: "gen.txt", Kind: workspacebaseline.ChangeCreated}}) {
		t.Fatalf("changes = %+v, want gen.txt alone", changes)
	}
	compared, err := OverlayTree{}.TreePathChange(t.Context(), tree, "gen.txt")
	testutil.FailErr(t, "path change", err)
	if compared.Kind != workspacebaseline.ChangeCreated || compared.After != "gen\n" {
		t.Fatalf("compared = %+v", compared)
	}
	omitted, err := OverlayTree{}.TreePathChange(t.Context(), tree, ".paintedwolf/approvals.yaml")
	testutil.FailErr(t, "omitted path", err)
	if omitted.Kind != "" {
		t.Fatalf("omitted = %+v, want host metadata to read as unchanged", omitted)
	}
}

func TestOverlayTreeNamesMovesWithUnchangedContent(t *testing.T) {
	data := t.TempDir()
	branch := filepath.Join(data, "branch")
	testutil.FailErr(t, "mkdir", os.MkdirAll(filepath.Join(branch, "a"), 0o755))
	testutil.FailErr(t, "seed", os.WriteFile(filepath.Join(branch, "a", "x.go"), []byte("x\n"), 0o644))
	testutil.FailErr(t, "seed", os.WriteFile(filepath.Join(branch, "a", "y.go"), []byte("y\n"), 0o644))
	blobs := sourceblob.New(filepath.Join(data, "source-content"))
	store := workspacebaseline.New(nil, blobs, filepath.Join(data, "manifests"))
	manifest, err := store.Capture(context.Background(), "job", workspacebaseline.Branch(nil, branch))
	testutil.FailErr(t, "capture", err)
	testutil.FailErr(t, "move", os.Rename(filepath.Join(branch, "a"), filepath.Join(branch, "b")))
	testutil.FailErr(t, "edit", os.WriteFile(filepath.Join(branch, "b", "y.go"), []byte("z\n"), 0o644))
	same := workspacebaseline.PathMove{From: "a/x.go", To: "b/x.go"}
	edited := workspacebaseline.PathMove{From: "a/y.go", To: "b/y.go"}
	tree := tools.SourceTree{BaselinePath: manifest, BranchRoot: branch, Roots: []projectroot.RootRef{{ID: "root", Path: "/project", IsPrimary: true}}}
	unchanged, err := OverlayTree{}.TreeMovesUnchanged(t.Context(), tree, []workspacebaseline.PathMove{same, edited})
	testutil.FailErr(t, "moves", err)
	if len(unchanged) != 1 || !unchanged[same] {
		t.Fatalf("unchanged = %+v, want only the file that kept its bytes", unchanged)
	}
	if _, err := (OverlayTree{}).TreeMovesUnchanged(t.Context(), tools.SourceTree{BranchRoot: branch}, []workspacebaseline.PathMove{same}); err == nil {
		t.Fatal("moves with no baseline succeeded")
	}
}

func TestOverlayTreeReportsAMissingBranch(t *testing.T) {
	data := t.TempDir()
	branch := filepath.Join(data, "branch")
	testutil.FailErr(t, "mkdir", os.MkdirAll(branch, 0o755))
	store := workspacebaseline.New(nil, sourceblob.New(filepath.Join(data, "source-content")), filepath.Join(data, "manifests"))
	manifest, err := store.Capture(t.Context(), "job", workspacebaseline.Branch(nil, branch))
	testutil.FailErr(t, "capture", err)
	tree := tools.SourceTree{BaselinePath: manifest, BranchRoot: filepath.Join(data, "reclaimed")}
	if _, err := (OverlayTree{}).TreeChanges(t.Context(), tree); err == nil {
		t.Fatal("comparing a reclaimed branch succeeded")
	}
}

func TestOverlayTreeReportsAnUnusableBaseline(t *testing.T) {
	tree := tools.SourceTree{BranchRoot: t.TempDir()}
	if _, err := (OverlayTree{}).TreeChanges(t.Context(), tree); err == nil {
		t.Fatal("tree changes with no baseline succeeded")
	}
	if _, err := (OverlayTree{}).TreePathChange(t.Context(), tree, "main.go"); err == nil {
		t.Fatal("path change with no baseline succeeded")
	}
}
