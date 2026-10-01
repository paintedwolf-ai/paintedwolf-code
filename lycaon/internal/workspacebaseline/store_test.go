package workspacebaseline_test

import (
	"context"
	"database/sql"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/projectroot"
	"github.com/lycaon/lycaon/internal/sourceblob"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/workspacebaseline"
)

func TestBaselineCaptureIsImmutableAndIncludesIgnoredFiles(t *testing.T) {
	root := t.TempDir()
	branch := t.TempDir()
	blobs := sourceblob.New(filepath.Join(root, "source-content"))
	store := workspacebaseline.New(nil, blobs, filepath.Join(root, "manifests"))
	for path, body := range map[string]string{".gitignore": "ignored.txt\n", "ignored.txt": "original\n", "empty.txt": "", "binary.dat": "\x00binary"} {
		testutil.FailErr(t, "write fixture", os.WriteFile(filepath.Join(branch, path), []byte(body), 0o600))
	}
	ref, err := store.Capture(t.Context(), "job", workspacebaseline.Branch(nil, branch))
	testutil.FailErr(t, "capture", err)
	testutil.FailErr(t, "rewrite branch", os.WriteFile(filepath.Join(branch, "ignored.txt"), []byte("changed\n"), 0o600))
	reader, err := workspacebaseline.Open(t.Context(), ref, workspacebaseline.ContentStore(ref))
	testutil.FailErr(t, "open baseline", err)
	defer func() { _ = reader.Close() }()
	body, exists, err := reader.Content(t.Context(), "ignored.txt")
	testutil.FailErr(t, "read original ignored content", err)
	if !exists || body != "original\n" {
		t.Fatalf("baseline = %q exists=%v", body, exists)
	}
	_, exists, err = reader.Content(t.Context(), "empty.txt")
	testutil.FailErr(t, "read empty file", err)
	if !exists {
		t.Fatal("empty file treated as absent")
	}
	_, exists, err = reader.Content(t.Context(), "binary.dat")
	if !exists || err == nil {
		t.Fatalf("binary baseline: exists=%v err=%v", exists, err)
	}
	f, _, err := reader.Lookup(t.Context(), "ignored.txt")
	testutil.FailErr(t, "lookup object", err)
	rel, err := sourceblob.RelPath(f.SHA256)
	testutil.FailErr(t, "object path", err)
	testutil.FailErr(t, "remove merge object", os.Remove(filepath.Join(blobs.Root(), rel)))
	if _, _, err := reader.Content(t.Context(), "ignored.txt"); err == nil {
		t.Fatal("missing content treated as empty")
	}
}

func TestCanceledBaselineLeavesNoPublishedManifest(t *testing.T) {
	root := t.TempDir()
	store := workspacebaseline.New(nil, sourceblob.New(filepath.Join(root, "source-content")), filepath.Join(root, "manifests"))
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	_, err := store.Capture(ctx, "job", workspacebaseline.Branch(nil, t.TempDir()))
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("capture error=%v", err)
	}
	entries, err := os.ReadDir(filepath.Join(root, "manifests"))
	testutil.FailErr(t, "inspect failed capture", err)
	for _, entry := range entries {
		if strings.HasSuffix(entry.Name(), ".db") {
			t.Fatalf("failed capture retained %s", entry.Name())
		}
	}
}

func TestBaselineKeepsRootQualifiedPaths(t *testing.T) {
	root := t.TempDir()
	branch := t.TempDir()
	roots := []projectroot.RootRef{{ID: "p", Label: "app", Path: t.TempDir(), IsPrimary: true}, {ID: "s", Label: "shared", Path: t.TempDir()}}
	for _, r := range roots {
		dir, err := projectroot.BranchDirForID(r.ID)
		testutil.FailErr(t, "root directory", err)
		testutil.FailErr(t, "create branch root", os.MkdirAll(filepath.Join(branch, dir), 0o700))
		testutil.FailErr(t, "write root source", os.WriteFile(filepath.Join(branch, dir, "same.txt"), []byte(r.Label), 0o600))
	}
	store := workspacebaseline.New(nil, sourceblob.New(filepath.Join(root, "source-content")), filepath.Join(root, "manifests"))
	ref, err := store.Capture(t.Context(), "job", workspacebaseline.Branch(roots, branch))
	testutil.FailErr(t, "capture roots", err)
	reader, err := workspacebaseline.Open(t.Context(), ref, workspacebaseline.ContentStore(ref))
	testutil.FailErr(t, "open roots", err)
	defer func() { _ = reader.Close() }()
	for path, want := range map[string]string{"same.txt": "app", "@shared/same.txt": "shared"} {
		content, exists, err := reader.Content(t.Context(), path)
		testutil.FailErr(t, "read root-qualified baseline", err)
		if !exists || content != want {
			t.Fatalf("%s: %q exists=%v", path, content, exists)
		}
	}
	changes, err := reader.Changes(t.Context(), roots, branch)
	testutil.FailErr(t, "compare roots", err)
	if len(changes) != 0 {
		t.Fatalf("unchanged roots: %v", changes)
	}
}

func TestReaderRejectsUnknownFormatAndIncompleteShape(t *testing.T) {
	for name, schema := range map[string]string{
		"unknown format":  `CREATE TABLE manifest(version INTEGER); INSERT INTO manifest VALUES(2);`,
		"missing files":   `CREATE TABLE manifest(version INTEGER); INSERT INTO manifest VALUES(1);`,
		"missing columns": `CREATE TABLE manifest(version INTEGER); INSERT INTO manifest VALUES(1); CREATE TABLE files(path TEXT);`,
	} {
		t.Run(name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "manifest.db")
			database, err := sql.Open("sqlite", path)
			testutil.FailErr(t, "create manifest fixture", err)
			_, err = database.ExecContext(t.Context(), schema)
			testutil.FailErr(t, "write manifest fixture", err)
			testutil.FailErr(t, "close manifest fixture", database.Close())
			if reader, err := workspacebaseline.Open(t.Context(), path, workspacebaseline.ContentStore(path)); err == nil {
				_ = reader.Close()
				t.Fatal("unsupported manifest accepted")
			}
		})
	}
}
