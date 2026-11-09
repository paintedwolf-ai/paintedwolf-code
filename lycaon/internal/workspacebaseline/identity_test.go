package workspacebaseline_test

import (
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"github.com/lycaon/lycaon/internal/projectroot"
	"github.com/lycaon/lycaon/internal/sourceblob"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/workspacebaseline"
	_ "modernc.org/sqlite"
)

func multiRootFixture(t *testing.T) (branch string, roots []projectroot.RootRef) {
	t.Helper()
	branch = t.TempDir()
	roots = []projectroot.RootRef{{ID: "p", Label: "app", Path: t.TempDir(), IsPrimary: true}, {ID: "s", Label: "shared", Path: t.TempDir()}}
	for _, r := range roots {
		dir, err := projectroot.BranchDirForID(r.ID)
		testutil.FailErr(t, "root directory", err)
		testutil.FailErr(t, "create branch root", os.MkdirAll(filepath.Join(branch, dir), 0o700))
		testutil.FailErr(t, "write root source", os.WriteFile(filepath.Join(branch, dir, "same.txt"), []byte(r.Label), 0o600))
	}
	primaryDir, err := projectroot.BranchDirForID("p")
	testutil.FailErr(t, "primary directory", err)
	testutil.FailErr(t, "write primary extra", os.WriteFile(filepath.Join(branch, primaryDir, "only.txt"), []byte("primary\n"), 0o600))
	return branch, roots
}

func branchDir(t *testing.T, branch, id string) string {
	t.Helper()
	dir, err := projectroot.BranchDirForID(id)
	testutil.FailErr(t, "root directory", err)
	return filepath.Join(branch, dir)
}

func TestManifestKeysEntriesByRootIdentity(t *testing.T) {
	root := t.TempDir()
	branch, roots := multiRootFixture(t)
	store := workspacebaseline.New(nil, sourceblob.New(filepath.Join(root, "source-content")), filepath.Join(root, "manifests"))
	ref, err := store.Capture(t.Context(), "job", workspacebaseline.Branch(roots, branch))
	testutil.FailErr(t, "capture", err)
	reader, err := workspacebaseline.Open(t.Context(), ref, workspacebaseline.ContentStore(ref))
	testutil.FailErr(t, "open", err)
	defer func() { _ = reader.Close() }()

	seen := map[string]workspacebaseline.Entry{}
	err = reader.EachIdentity(t.Context(), roots, func(entry workspacebaseline.Entry) error {
		seen[entry.RootID+"/"+entry.ScopeRel] = entry
		return nil
	})
	testutil.FailErr(t, "each identity", err)
	if len(seen) != 3 {
		t.Fatalf("entries = %v", seen)
	}
	for _, root := range roots {
		entry, ok := seen[root.ID+"/same.txt"]
		if !ok {
			t.Fatalf("missing identity for %s: %v", root.ID, seen)
		}
		if entry.Path == "" || entry.File.SHA256 == "" {
			t.Fatalf("entry lost display or body: %+v", entry)
		}
	}
	if _, ok := seen["s/only.txt"]; ok {
		t.Fatal("primary-only file keyed under shared root")
	}
	if _, ok := seen["p/only.txt"]; !ok {
		t.Fatal("primary-only file missing under primary identity")
	}

	f, exists, err := reader.LookupIdentity(t.Context(), roots, "p", "only.txt")
	testutil.FailErr(t, "lookup identity", err)
	if !exists || f.SHA256 == "" {
		t.Fatalf("lookup identity = %+v exists=%v", f, exists)
	}
	if _, exists, err := reader.LookupIdentity(t.Context(), roots, "s", "only.txt"); exists || err != nil {
		t.Fatalf("cross-root identity leaked: exists=%v err=%v", exists, err)
	}
}

func TestReaderReadsLegacyDisplayManifest(t *testing.T) {
	root := t.TempDir()
	branch, roots := multiRootFixture(t)
	store := workspacebaseline.New(nil, sourceblob.New(filepath.Join(root, "source-content")), filepath.Join(root, "manifests"))
	ref, err := store.Capture(t.Context(), "job", workspacebaseline.Branch(roots, branch))
	testutil.FailErr(t, "capture", err)
	current, err := workspacebaseline.Open(t.Context(), ref, workspacebaseline.ContentStore(ref))
	testutil.FailErr(t, "open current", err)
	defer func() { _ = current.Close() }()

	// Rebuild the same manifest in the legacy display-keyed shape.
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

	legacy, err := workspacebaseline.Open(t.Context(), legacyPath, workspacebaseline.ContentStore(legacyPath))
	testutil.FailErr(t, "open legacy", err)
	defer func() { _ = legacy.Close() }()

	for _, root := range roots {
		f, exists, err := legacy.LookupIdentity(t.Context(), roots, root.ID, "same.txt")
		testutil.FailErr(t, "legacy lookup identity", err)
		if !exists || f.SHA256 == "" {
			t.Fatalf("legacy lookup %s: exists=%v", root.ID, exists)
		}
	}
	identities := map[string]bool{}
	err = legacy.EachIdentity(t.Context(), roots, func(entry workspacebaseline.Entry) error {
		identities[entry.RootID+"/"+entry.ScopeRel] = true
		return nil
	})
	testutil.FailErr(t, "legacy each identity", err)
	if len(identities) != 3 {
		t.Fatalf("legacy identities = %v", identities)
	}

	// The same tree change reports identically against either manifest shape.
	testutil.FailErr(t, "rewrite same", os.WriteFile(filepath.Join(branchDir(t, branch, "p"), "same.txt"), []byte("changed\n"), 0o600))
	testutil.FailErr(t, "remove only", os.Remove(filepath.Join(branchDir(t, branch, "p"), "only.txt")))
	testutil.FailErr(t, "write made", os.WriteFile(filepath.Join(branchDir(t, branch, "s"), "made.txt"), []byte("made\n"), 0o600))
	want, err := current.ChangeSet(t.Context(), roots, branch)
	testutil.FailErr(t, "current change set", err)
	got, err := legacy.ChangeSet(t.Context(), roots, branch)
	testutil.FailErr(t, "legacy change set", err)
	if fmt.Sprint(got) != fmt.Sprint(want) {
		t.Fatalf("legacy change set = %+v, want %+v", got, want)
	}
}

func TestSingleRootCaptureCarriesRootIdentity(t *testing.T) {
	root := t.TempDir()
	branch := t.TempDir()
	roots := []projectroot.RootRef{{ID: "solo", Label: "solo", Path: t.TempDir(), IsPrimary: true}}
	testutil.FailErr(t, "write file", os.WriteFile(filepath.Join(branch, "a.txt"), []byte("a\n"), 0o600))
	store := workspacebaseline.New(nil, sourceblob.New(filepath.Join(root, "source-content")), filepath.Join(root, "manifests"))
	ref, err := store.Capture(t.Context(), "job", workspacebaseline.Branch(roots, branch))
	testutil.FailErr(t, "capture", err)
	reader, err := workspacebaseline.Open(t.Context(), ref, workspacebaseline.ContentStore(ref))
	testutil.FailErr(t, "open", err)
	defer func() { _ = reader.Close() }()
	var entries []workspacebaseline.Entry
	err = reader.EachIdentity(t.Context(), roots, func(entry workspacebaseline.Entry) error {
		entries = append(entries, entry)
		return nil
	})
	testutil.FailErr(t, "each identity", err)
	if len(entries) != 1 || entries[0].RootID != "solo" || entries[0].ScopeRel != "a.txt" || entries[0].Path != "a.txt" {
		t.Fatalf("entries = %+v", entries)
	}
}

func TestOverlayCaptureCarriesRootIdentity(t *testing.T) {
	root := t.TempDir()
	branch, roots := multiRootFixture(t)
	store := workspacebaseline.New(nil, sourceblob.New(filepath.Join(root, "source-content")), filepath.Join(root, "manifests"))
	baselineRef, err := store.Capture(t.Context(), "job", workspacebaseline.Branch(roots, branch))
	testutil.FailErr(t, "capture baseline", err)
	testutil.FailErr(t, "rewrite shared", os.WriteFile(filepath.Join(branchDir(t, branch, "s"), "same.txt"), []byte("changed\n"), 0o600))
	testutil.FailErr(t, "remove primary extra", os.Remove(filepath.Join(branchDir(t, branch, "p"), "only.txt")))
	overlay, err := store.CaptureOverlay(t.Context(), "job", baselineRef, roots, branch)
	testutil.FailErr(t, "capture overlay", err)
	reader, err := workspacebaseline.Open(t.Context(), overlay.Path, workspacebaseline.ContentStore(overlay.Path))
	testutil.FailErr(t, "open overlay", err)
	defer func() { _ = reader.Close() }()
	seen := map[string]bool{}
	err = reader.EachIdentity(t.Context(), roots, func(entry workspacebaseline.Entry) error {
		seen[entry.RootID+"/"+entry.ScopeRel] = entry.File.Deleted
		return nil
	})
	testutil.FailErr(t, "each identity", err)
	if len(seen) != 2 {
		t.Fatalf("overlay entries = %v", seen)
	}
	if deleted, ok := seen["s/same.txt"]; !ok || deleted {
		t.Fatalf("shared rewrite deleted=%v present=%v", deleted, ok)
	}
	if deleted, ok := seen["p/only.txt"]; !ok || !deleted {
		t.Fatalf("primary removal deleted=%v present=%v", deleted, ok)
	}
}
