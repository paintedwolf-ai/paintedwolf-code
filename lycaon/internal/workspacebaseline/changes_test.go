package workspacebaseline_test

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/lycaon/lycaon/internal/projectroot"
	"github.com/lycaon/lycaon/internal/sourceblob"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/workspacebaseline"
)

func sealedBranch(t testing.TB, files map[string][]byte) (branch string, reader *workspacebaseline.Reader) {
	t.Helper()
	root := t.TempDir()
	branch = filepath.Join(t.TempDir(), "branch")
	blobs := sourceblob.New(filepath.Join(root, "source-content"))
	for rel, body := range files {
		abs := filepath.Join(branch, rel)
		testutil.FailErr(t, "mkdir", os.MkdirAll(filepath.Dir(abs), 0o755))
		testutil.FailErr(t, "write", os.WriteFile(abs, body, 0o644))
	}
	testutil.FailErr(t, "mkdir branch", os.MkdirAll(branch, 0o755))
	store := workspacebaseline.New(nil, blobs, filepath.Join(root, "manifests"))
	manifest, err := store.Capture(context.Background(), "job", workspacebaseline.Branch(nil, branch))
	testutil.FailErr(t, "capture", err)
	reader, err = workspacebaseline.Open(context.Background(), manifest, blobs)
	testutil.FailErr(t, "open", err)
	t.Cleanup(func() { _ = reader.Close() })
	return branch, reader
}

func rewrite(t *testing.T, path, body string) {
	t.Helper()
	testutil.FailErr(t, "rewrite", os.WriteFile(path, []byte(body), 0o644))
	later := time.Now().Add(time.Minute)
	testutil.FailErr(t, "chtimes", os.Chtimes(path, later, later))
}

func TestChangeSetNamesEachKindInPathOrder(t *testing.T) {
	branch, reader := sealedBranch(t, map[string][]byte{"edit.txt": []byte("old\n"), "gone.txt": []byte("gone\n"), "keep.txt": []byte("keep\n"), "dir/swap": []byte("file\n")})
	rewrite(t, filepath.Join(branch, "edit.txt"), "new\n")
	testutil.FailErr(t, "remove", os.Remove(filepath.Join(branch, "gone.txt")))
	testutil.FailErr(t, "remove swap", os.Remove(filepath.Join(branch, "dir", "swap")))
	testutil.FailErr(t, "dir in place of file", os.Mkdir(filepath.Join(branch, "dir", "swap"), 0o755))
	writeFile(t, filepath.Join(branch, "made.txt"), []byte("made\n"), 0o644)

	changes, err := reader.ChangeSet(t.Context(), nil, branch)
	testutil.FailErr(t, "change set", err)
	want := []workspacebaseline.Change{
		{Path: "dir/swap", Kind: workspacebaseline.ChangeDeleted},
		{Path: "edit.txt", Kind: workspacebaseline.ChangeModified},
		{Path: "gone.txt", Kind: workspacebaseline.ChangeDeleted},
		{Path: "made.txt", Kind: workspacebaseline.ChangeCreated},
	}
	if fmt.Sprint(changes) != fmt.Sprint(want) {
		t.Fatalf("changes = %+v, want %+v", changes, want)
	}
	paths, err := reader.Changes(t.Context(), nil, branch)
	testutil.FailErr(t, "changes", err)
	if len(paths) != len(want) || paths[1] != "edit.txt" {
		t.Fatalf("paths = %v", paths)
	}
}

func TestChangeSetStopsOnACanceledContext(t *testing.T) {
	branch, reader := sealedBranch(t, map[string][]byte{"a.txt": []byte("a\n")})
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if _, err := reader.ChangeSet(ctx, nil, branch); !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v, want canceled", err)
	}
}

func TestComparePathReadsBothSides(t *testing.T) {
	large := bytes.Repeat([]byte("L"), workspacebaseline.MaxContentBytes+1)
	branch, reader := sealedBranch(t, map[string][]byte{
		"edit.txt": []byte("old\n"), "gone.txt": []byte("gone\n"), "keep.txt": []byte("keep\n"),
		"big.bin": large, "bin.dat": {0, 1, 2, 0}, "shrunk.txt": []byte("was small\n"),
	})
	rewrite(t, filepath.Join(branch, "edit.txt"), "new\n")
	testutil.FailErr(t, "remove", os.Remove(filepath.Join(branch, "gone.txt")))
	writeFile(t, filepath.Join(branch, "made.txt"), []byte("made\n"), 0o644)
	rewrite(t, filepath.Join(branch, "big.bin"), "small now\n")
	rewrite(t, filepath.Join(branch, "bin.dat"), "text now\n")
	writeFile(t, filepath.Join(branch, "shrunk.txt"), large, 0o644)
	testutil.FailErr(t, "dir", os.Mkdir(filepath.Join(branch, "emptydir"), 0o755))

	for _, c := range []struct {
		path                string
		kind                workspacebaseline.ChangeKind
		before, after       string
		hasBefore, hasAfter bool
		absent              bool
	}{
		{"edit.txt", workspacebaseline.ChangeModified, "old\n", "new\n", true, true, false},
		{"gone.txt", workspacebaseline.ChangeDeleted, "gone\n", "", true, true, false},
		{"made.txt", workspacebaseline.ChangeCreated, "", "made\n", true, true, false},
		{"keep.txt", "", "", "", false, false, false},
		{"never.txt", "", "", "", false, false, true},
		{"emptydir", "", "", "", false, false, false},
		{"big.bin", workspacebaseline.ChangeModified, "", "small now\n", false, true, false},
		{"bin.dat", workspacebaseline.ChangeModified, "", "text now\n", false, true, false},
		{"shrunk.txt", workspacebaseline.ChangeModified, "was small\n", "", true, false, false},
	} {
		t.Run(c.path, func(t *testing.T) {
			got, err := reader.ComparePath(t.Context(), nil, branch, c.path)
			testutil.FailErr(t, "compare", err)
			if got.Kind != c.kind || got.Before != c.before || got.After != c.after || got.HasBefore != c.hasBefore || got.HasAfter != c.hasAfter || got.Absent != c.absent {
				t.Fatalf("got %+v, want %+v", got, c)
			}
			if got.Abs != filepath.Join(branch, c.path) {
				t.Fatalf("abs = %q", got.Abs)
			}
		})
	}
}

func TestMoveUnchangedComparesBaselineContentWithTheLiveFile(t *testing.T) {
	large := bytes.Repeat([]byte("L"), workspacebaseline.MaxContentBytes+1)
	branch, reader := sealedBranch(t, map[string][]byte{"a/same.go": []byte("same\n"), "a/edit.go": []byte("edit\n"), "a/big.bin": large})
	testutil.FailErr(t, "move directory", os.Rename(filepath.Join(branch, "a"), filepath.Join(branch, "b")))
	rewrite(t, filepath.Join(branch, "b", "edit.go"), "abcd\n")
	writeFile(t, filepath.Join(branch, "b", "new.go"), []byte("new\n"), 0o644)

	for _, c := range []struct {
		from, to string
		want     bool
	}{
		{"a/same.go", "b/same.go", true},
		{"a/edit.go", "b/edit.go", false},
		{"a/big.bin", "b/big.bin", false},
		{"a/never.go", "b/new.go", false},
		{"a/same.go", "b/gone.go", false},
		{"a/same.go", "b/new.go", false},
	} {
		got, err := reader.MoveUnchanged(t.Context(), nil, branch, workspacebaseline.PathMove{From: c.from, To: c.to})
		testutil.FailErr(t, "move unchanged", err)
		if got != c.want {
			t.Fatalf("%s to %s = %v, want %v", c.from, c.to, got, c.want)
		}
	}
	if _, err := reader.MoveUnchanged(t.Context(), nil, branch, workspacebaseline.PathMove{From: "a/same.go", To: "../out"}); err == nil {
		t.Fatal("a path that climbs out of the branch was compared")
	}
}

func TestComparePathRefusesWhatLeavesTheBranch(t *testing.T) {
	branch, reader := sealedBranch(t, map[string][]byte{"keep.txt": []byte("keep\n")})
	outside := t.TempDir()
	writeFile(t, filepath.Join(outside, "secret.txt"), []byte("secret\n"), 0o644)
	testutil.FailErr(t, "dir link", os.Symlink(outside, filepath.Join(branch, "linked")))
	testutil.FailErr(t, "file link", os.Symlink(filepath.Join(outside, "secret.txt"), filepath.Join(branch, "alias.txt")))

	for _, path := range []string{"linked/secret.txt", "alias.txt"} {
		got, err := reader.ComparePath(t.Context(), nil, branch, path)
		testutil.FailErr(t, "compare "+path, err)
		if got.Kind != workspacebaseline.ChangeCreated || got.HasAfter || got.After != "" {
			t.Fatalf("%s = %+v, want a change with no text from outside the branch", path, got)
		}
	}
	if _, err := reader.ComparePath(t.Context(), nil, branch, "../outside"); err == nil {
		t.Fatal("a path that climbs out of the branch was compared")
	}
}

func TestComparePathFollowsAQualifiedRoot(t *testing.T) {
	root := t.TempDir()
	blobs := sourceblob.New(filepath.Join(root, "source-content"))
	roots := []projectroot.RootRef{
		{ID: "11111111-1111-4111-8111-111111111111", Path: filepath.Join(root, "main"), IsPrimary: true},
		{ID: "22222222-2222-4222-8222-222222222222", Path: filepath.Join(root, "docs"), Label: "docs"},
	}
	branch := filepath.Join(root, "branch")
	dir, err := projectroot.BranchDirForID(roots[1].ID)
	testutil.FailErr(t, "branch dir", err)
	primaryDir, err := projectroot.BranchDirForID(roots[0].ID)
	testutil.FailErr(t, "primary branch dir", err)
	testutil.FailErr(t, "mkdir primary", os.MkdirAll(filepath.Join(branch, primaryDir), 0o755))
	writeFile(t, filepath.Join(branch, dir, "guide.md"), []byte("old\n"), 0o644)
	store := workspacebaseline.New(nil, blobs, filepath.Join(root, "manifests"))
	manifest, err := store.Capture(t.Context(), "job", workspacebaseline.Branch(roots, branch))
	testutil.FailErr(t, "capture", err)
	reader, err := workspacebaseline.Open(t.Context(), manifest, blobs)
	testutil.FailErr(t, "open", err)
	defer func() { _ = reader.Close() }()
	rewrite(t, filepath.Join(branch, dir, "guide.md"), "new\n")

	got, err := reader.ComparePath(t.Context(), roots, branch, "@docs/guide.md")
	testutil.FailErr(t, "compare", err)
	if got.Kind != workspacebaseline.ChangeModified || got.Before != "old\n" || got.After != "new\n" {
		t.Fatalf("got %+v", got)
	}
	changes, err := reader.ChangeSet(t.Context(), roots, branch)
	testutil.FailErr(t, "change set", err)
	if len(changes) != 1 || changes[0].Path != "@docs/guide.md" {
		t.Fatalf("changes = %+v", changes)
	}
}

// BenchmarkChangeSet measures one tree comparison on a leg of each size, opening
// the manifest as each overview call does. A small edit list is typical; the
// comparison stats every file regardless.
func BenchmarkChangeSet(b *testing.B) {
	for _, size := range []int{2_000, 10_000} {
		b.Run(fmt.Sprintf("files=%d", size), func(b *testing.B) {
			root := b.TempDir()
			branch := filepath.Join(root, "branch")
			blobs := sourceblob.New(filepath.Join(root, "source-content"))
			for i := range size {
				abs := filepath.Join(branch, fmt.Sprintf("d%02d", i%50), fmt.Sprintf("s%02d", i%37), fmt.Sprintf("f%05d.go", i))
				if err := os.MkdirAll(filepath.Dir(abs), 0o755); err != nil {
					b.Fatal(err)
				}
				if err := os.WriteFile(abs, fmt.Appendf(nil, "package p // %d\n", i), 0o644); err != nil {
					b.Fatal(err)
				}
			}
			store := workspacebaseline.New(nil, blobs, filepath.Join(root, "manifests"))
			manifest, err := store.Capture(b.Context(), "job", workspacebaseline.Branch(nil, branch))
			if err != nil {
				b.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(branch, "made.txt"), []byte("made\n"), 0o644); err != nil {
				b.Fatal(err)
			}
			b.ReportAllocs()
			b.ResetTimer()
			for range b.N {
				reader, err := workspacebaseline.Open(b.Context(), manifest, blobs)
				if err != nil {
					b.Fatal(err)
				}
				changes, err := reader.ChangeSet(b.Context(), nil, branch)
				if err != nil {
					b.Fatal(err)
				}
				if len(changes) != 1 {
					b.Fatalf("changes = %d", len(changes))
				}
				_ = reader.Close()
			}
		})
	}
}
