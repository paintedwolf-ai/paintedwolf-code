package workspacebaseline_test

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"testing"
	"time"

	"github.com/lycaon/lycaon/internal/projectroot"
	"github.com/lycaon/lycaon/internal/sourceblob"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/workspacebaseline"
)

func writeFile(t *testing.T, path string, body []byte, mode os.FileMode) {
	t.Helper()
	testutil.FailErr(t, "mkdir "+filepath.Dir(path), os.MkdirAll(filepath.Dir(path), 0o755))
	testutil.FailErr(t, "write "+path, os.WriteFile(path, body, mode))
}

// TestOverlayCaptureRebuildsTheBranchExactly seals a worker's changes, drops the
// tree, rebuilds it, and checks the baseline comparison sees the same overlay.
func TestOverlayCaptureRebuildsTheBranchExactly(t *testing.T) {
	root := t.TempDir()
	project := t.TempDir()
	branch := filepath.Join(t.TempDir(), "branch")
	blobs := sourceblob.New(filepath.Join(root, "source-content"))
	store := workspacebaseline.New(nil, blobs, filepath.Join(root, "manifests"))

	large := bytes.Repeat([]byte("L"), workspacebaseline.MaxContentBytes+1)
	seed := map[string][]byte{
		"keep.txt":       []byte("keep\n"),
		"edit.txt":       []byte("before\n"),
		"gone.txt":       []byte("gone\n"),
		"tool.sh":        []byte("#!/bin/sh\n"),
		"big-keep.bin":   large,
		"big-edit.bin":   large,
		"nested/deep.md": []byte("# deep\n"),
	}
	for rel, body := range seed {
		mode := os.FileMode(0o644)
		if rel == "tool.sh" {
			mode = 0o755
		}
		writeFile(t, filepath.Join(project, rel), body, mode)
		writeFile(t, filepath.Join(branch, rel), body, mode)
	}
	testutil.FailErr(t, "symlink", os.Symlink("keep.txt", filepath.Join(branch, "alias")))
	testutil.FailErr(t, "project symlink", os.Symlink("keep.txt", filepath.Join(project, "alias")))
	baseline, err := store.Capture(t.Context(), "job", workspacebaseline.Branch(nil, branch))
	testutil.FailErr(t, "capture baseline", err)

	// The worker's changes: an edit, a large edit, a deletion, a new file, a
	// mode change, and a retargeted symlink.
	later := time.Now().Add(time.Minute)
	writeFile(t, filepath.Join(branch, "edit.txt"), []byte("after\n"), 0o644)
	bigEdited := append(append([]byte(nil), large...), 'X')
	writeFile(t, filepath.Join(branch, "big-edit.bin"), bigEdited, 0o644)
	testutil.FailErr(t, "delete", os.Remove(filepath.Join(branch, "gone.txt")))
	writeFile(t, filepath.Join(branch, "new/added.txt"), []byte("added\n"), 0o600)
	testutil.FailErr(t, "chmod", os.Chmod(filepath.Join(branch, "tool.sh"), 0o644))
	testutil.FailErr(t, "touch tool", os.Chtimes(filepath.Join(branch, "tool.sh"), later, later))
	testutil.FailErr(t, "retarget", os.Remove(filepath.Join(branch, "alias")))
	testutil.FailErr(t, "retarget", os.Symlink("edit.txt", filepath.Join(branch, "alias")))

	captured, err := store.CaptureOverlay(t.Context(), "job", baseline, nil, branch)
	testutil.FailErr(t, "capture overlay", err)
	if captured.Changed != 5 || captured.Deleted != 1 {
		t.Fatalf("overlay = %+v want 5 changed, 1 deleted", captured)
	}
	wantChanged := changedPaths(t, baseline, branch)

	testutil.FailErr(t, "drop tree", os.RemoveAll(branch))
	current := func(path string) string { return filepath.Join(project, filepath.FromSlash(path)) }
	report, err := workspacebaseline.Materialize(t.Context(), baseline, captured.Path, workspacebaseline.ContentStore(baseline), nil, branch, current)
	testutil.FailErr(t, "materialize", err)
	if len(report.Unavailable) != 0 || report.Deleted != 1 {
		t.Fatalf("report = %+v", report)
	}

	assertBody(t, filepath.Join(branch, "keep.txt"), "keep\n")
	assertBody(t, filepath.Join(branch, "edit.txt"), "after\n")
	assertBody(t, filepath.Join(branch, "new/added.txt"), "added\n")
	assertBody(t, filepath.Join(branch, "nested/deep.md"), "# deep\n")
	if got, err := os.ReadFile(filepath.Join(branch, "big-edit.bin")); err != nil || !bytes.Equal(got, bigEdited) {
		t.Fatalf("large edit not restored: err=%v len=%d", err, len(got))
	}
	if got, err := os.ReadFile(filepath.Join(branch, "big-keep.bin")); err != nil || !bytes.Equal(got, large) {
		t.Fatalf("large stand-in not restored: err=%v len=%d", err, len(got))
	}
	if _, err := os.Lstat(filepath.Join(branch, "gone.txt")); !os.IsNotExist(err) {
		t.Fatalf("deleted path came back: %v", err)
	}
	if info, err := os.Stat(filepath.Join(branch, "tool.sh")); err != nil || info.Mode().Perm() != 0o644 {
		t.Fatalf("mode change lost: %v %v", err, info)
	}
	if target, err := os.Readlink(filepath.Join(branch, "alias")); err != nil || target != "edit.txt" {
		t.Fatalf("symlink = %q err=%v", target, err)
	}
	if got := changedPaths(t, baseline, branch); !equalStrings(got, wantChanged) {
		t.Fatalf("rebuilt overlay = %v want %v", got, wantChanged)
	}
}

func TestMaterializeReportsMissingStandIns(t *testing.T) {
	root := t.TempDir()
	branch := filepath.Join(t.TempDir(), "branch")
	store := workspacebaseline.New(nil, sourceblob.New(filepath.Join(root, "source-content")), filepath.Join(root, "manifests"))
	writeFile(t, filepath.Join(branch, "big.bin"), bytes.Repeat([]byte("B"), workspacebaseline.MaxContentBytes+1), 0o644)
	writeFile(t, filepath.Join(branch, "small.txt"), []byte("small\n"), 0o644)
	baseline, err := store.Capture(t.Context(), "job", workspacebaseline.Branch(nil, branch))
	testutil.FailErr(t, "capture baseline", err)
	testutil.FailErr(t, "drop tree", os.RemoveAll(branch))

	report, err := workspacebaseline.Materialize(t.Context(), baseline, "", workspacebaseline.ContentStore(baseline), nil, branch, func(string) string { return "" })
	testutil.FailErr(t, "materialize", err)
	if len(report.Unavailable) != 1 || report.Unavailable[0] != "big.bin" {
		t.Fatalf("unavailable = %v", report.Unavailable)
	}
	assertBody(t, filepath.Join(branch, "small.txt"), "small\n")
}

func TestOverlayCaptureKeepsRootQualifiedPaths(t *testing.T) {
	root := t.TempDir()
	branch := t.TempDir()
	roots := []projectroot.RootRef{{ID: "p", Label: "app", Path: t.TempDir(), IsPrimary: true}, {ID: "s", Label: "shared", Path: t.TempDir()}}
	for _, r := range roots {
		dir, err := projectroot.BranchDirForID(r.ID)
		testutil.FailErr(t, "root directory", err)
		writeFile(t, filepath.Join(branch, dir, "file.txt"), []byte("base\n"), 0o644)
	}
	store := workspacebaseline.New(nil, sourceblob.New(filepath.Join(root, "source-content")), filepath.Join(root, "manifests"))
	baseline, err := store.Capture(t.Context(), "job", workspacebaseline.Branch(roots, branch))
	testutil.FailErr(t, "capture baseline", err)
	sharedDir, err := projectroot.BranchDirForID("s")
	testutil.FailErr(t, "shared dir", err)
	writeFile(t, filepath.Join(branch, sharedDir, "file.txt"), []byte("changed\n"), 0o644)

	captured, err := store.CaptureOverlay(t.Context(), "job", baseline, roots, branch)
	testutil.FailErr(t, "capture overlay", err)
	reader, err := workspacebaseline.Open(t.Context(), captured.Path, workspacebaseline.ContentStore(captured.Path))
	testutil.FailErr(t, "open overlay", err)
	defer func() { _ = reader.Close() }()
	if _, ok, err := reader.Lookup(t.Context(), "@shared/file.txt"); err != nil || !ok {
		t.Fatalf("qualified overlay row missing: ok=%v err=%v", ok, err)
	}
	testutil.FailErr(t, "drop tree", os.RemoveAll(branch))
	_, err = workspacebaseline.Materialize(t.Context(), baseline, captured.Path, workspacebaseline.ContentStore(baseline), roots, branch, func(string) string { return "" })
	testutil.FailErr(t, "materialize", err)
	assertBody(t, filepath.Join(branch, sharedDir, "file.txt"), "changed\n")
}

func changedPaths(t *testing.T, baselinePath, branch string) []string {
	t.Helper()
	reader, err := workspacebaseline.Open(t.Context(), baselinePath, workspacebaseline.ContentStore(baselinePath))
	testutil.FailErr(t, "open baseline", err)
	defer func() { _ = reader.Close() }()
	changed, err := reader.Changes(t.Context(), nil, branch)
	testutil.FailErr(t, "changes", err)
	sort.Strings(changed)
	return changed
}

func assertBody(t *testing.T, path, want string) {
	t.Helper()
	got, err := os.ReadFile(path)
	testutil.FailErr(t, "read "+path, err)
	if string(got) != want {
		t.Fatalf("%s = %q want %q", path, got, want)
	}
}

func equalStrings(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func TestOverlayCaptureIncludesGitignoredFiles(t *testing.T) {
	root := t.TempDir()
	branch := filepath.Join(t.TempDir(), "branch")
	blobs := sourceblob.New(filepath.Join(root, "source-content"))
	store := workspacebaseline.New(nil, blobs, filepath.Join(root, "manifests"))

	// Baseline has a gitignore ignoring build/ and vendor/.
	writeFile(t, filepath.Join(branch, ".gitignore"), []byte("build/\nvendor/\n"), 0o644)
	writeFile(t, filepath.Join(branch, "keep.txt"), []byte("keep\n"), 0o644)

	baseline, err := store.Capture(t.Context(), "job", workspacebaseline.Branch(nil, branch))
	testutil.FailErr(t, "capture baseline", err)

	// Worker creates files under build/ and modifies keep.txt.
	writeFile(t, filepath.Join(branch, "build/bundle.js"), []byte("console.log(1);\n"), 0o644)
	writeFile(t, filepath.Join(branch, "keep.txt"), []byte("keep changed\n"), 0o644)

	captured, err := store.CaptureOverlay(t.Context(), "job", baseline, nil, branch)
	testutil.FailErr(t, "capture overlay", err)

	// Both build/bundle.js and keep.txt must be included; overlay capture does not exclude gitignore.
	if captured.Changed != 2 || captured.Deleted != 0 {
		t.Fatalf("overlay = %+v want 2 changed, 0 deleted", captured)
	}
	changed := changedPaths(t, baseline, branch)
	want := []string{"build/bundle.js", "keep.txt"}
	if !equalStrings(changed, want) {
		t.Fatalf("changed = %v want %v", changed, want)
	}
}

func TestOverlayCaptureParallelCorrectness(t *testing.T) {
	root := t.TempDir()
	branch := filepath.Join(t.TempDir(), "branch")
	blobs := sourceblob.New(filepath.Join(root, "source-content"))
	store := workspacebaseline.New(nil, blobs, filepath.Join(root, "manifests"))

	// Baseline has 50 files.
	for i := 0; i < 50; i++ {
		writeFile(t, filepath.Join(branch, fmt.Sprintf("pkg/file-%d.txt", i)), []byte(fmt.Sprintf("initial %d\n", i)), 0o644)
	}

	baseline, err := store.Capture(t.Context(), "job", workspacebaseline.Branch(nil, branch))
	testutil.FailErr(t, "capture baseline", err)

	// In branch, modify 25 files, delete 10 files, and add 25 new files.
	for i := 0; i < 25; i++ {
		writeFile(t, filepath.Join(branch, fmt.Sprintf("pkg/file-%d.txt", i)), []byte(fmt.Sprintf("modified %d\n", i)), 0o644)
	}
	for i := 25; i < 35; i++ {
		_ = os.Remove(filepath.Join(branch, fmt.Sprintf("pkg/file-%d.txt", i)))
	}
	for i := 0; i < 25; i++ {
		writeFile(t, filepath.Join(branch, fmt.Sprintf("pkg/new-%d.txt", i)), []byte(fmt.Sprintf("new %d\n", i)), 0o644)
	}

	captured, err := store.CaptureOverlay(t.Context(), "job", baseline, nil, branch)
	testutil.FailErr(t, "capture overlay", err)

	if captured.Changed != 50 || captured.Deleted != 10 {
		t.Fatalf("overlay = %+v want 50 changed, 10 deleted", captured)
	}

	overlayReader, err := workspacebaseline.Open(t.Context(), captured.Path, blobs)
	testutil.FailErr(t, "open overlay", err)
	defer func() { _ = overlayReader.Close() }()

	// Verify modified files have new content.
	content, exists, err := overlayReader.Content(t.Context(), "pkg/file-0.txt")
	testutil.FailErr(t, "read content", err)
	if !exists || content != "modified 0\n" {
		t.Fatalf("pkg/file-0.txt = %q exists=%v", content, exists)
	}

	// Verify deleted file is recorded as tombstone.
	deletedFile, exists, err := overlayReader.Lookup(t.Context(), "pkg/file-25.txt")
	testutil.FailErr(t, "lookup deleted", err)
	if !exists || !deletedFile.Deleted {
		t.Fatalf("deleted file = %+v exists=%v want deleted", deletedFile, exists)
	}

	// Verify new file is recorded.
	newContent, exists, err := overlayReader.Content(t.Context(), "pkg/new-0.txt")
	testutil.FailErr(t, "read new content", err)
	if !exists || newContent != "new 0\n" {
		t.Fatalf("pkg/new-0.txt = %q exists=%v", newContent, exists)
	}
}
