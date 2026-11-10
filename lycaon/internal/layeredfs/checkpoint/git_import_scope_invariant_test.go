package checkpoint

import (
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/lycaon/lycaon/internal/gitexec"

	"github.com/lycaon/lycaon/internal/testutil"
)

// A shared retained object store is not the set of objects authorized for this
// checkpoint. Publishing one ref must not export unrelated retained content.
func TestCheckpointGitImportDoesNotPublishUnreferencedObjects(t *testing.T) {
	root := t.TempDir()
	store, err := NewStore(filepath.Join(root, "checkpoints"))
	testutil.FailErr(t, "checkpoint store", err)
	objects := filepath.Join(store.RootDir(), "objects")
	selected, err := WriteLooseGitObject(objects, "blob", []byte("selected checkpoint content\n"))
	testutil.FailErr(t, "selected object", err)
	unrelated, err := WriteLooseGitObject(objects, "blob", []byte("another worker's unpublished content\n"))
	testutil.FailErr(t, "unrelated object", err)
	target := filepath.Join(root, "source")
	testutil.FailErr(t, "target repository", os.MkdirAll(filepath.Join(target, ".git", "objects"), 0o755))
	imported, err := ImportAndValidateCheckpointGitRefs(t.Context(), store.RootDir(), "selected-checkpoint", "selected-worker",
		PrivateGitSnapshot{Refs: map[string]string{"refs/tags/selected": selected}}, map[string]string{"primary": target})
	testutil.FailErr(t, "import selected checkpoint", err)
	if len(imported) == 0 {
		t.Fatal("selected checkpoint published no refs")
	}
	for _, oid := range imported {
		if oid != selected {
			t.Fatalf("published an unselected ref: %s", oid)
		}
	}
	targetObjects := filepath.Join(target, ".git", "objects")
	_, err = os.Stat(filepath.Join(targetObjects, selected[:2], selected[2:]))
	testutil.FailErr(t, "selected object retained in target", err)
	if _, err := os.Stat(filepath.Join(targetObjects, unrelated[:2], unrelated[2:])); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("unreferenced object escaped retained store: %v", err)
	}
	_, err = os.Stat(filepath.Join(objects, unrelated[:2], unrelated[2:]))
	testutil.FailErr(t, "unrelated retained object preserved", err)
}

// A pack can contain both selected history and later unpublished work. Copying
// its container wholesale must not widen the checkpoint's publication scope.
func TestCheckpointGitImportFiltersUnselectedPackedHistory(t *testing.T) {
	source := t.TempDir()
	runGitTestCmd(t, source, "init", "-q")
	testutil.FailErr(t, "ancestor file", os.WriteFile(filepath.Join(source, "file.txt"), []byte("ancestor\n"), 0o644))
	runGitTestCmd(t, source, "add", "file.txt")
	runGitTestCmd(t, source, "commit", "-qm", "ancestor")
	testutil.FailErr(t, "selected file", os.WriteFile(filepath.Join(source, "file.txt"), []byte("selected\n"), 0o644))
	runGitTestCmd(t, source, "add", "file.txt")
	runGitTestCmd(t, source, "commit", "-qm", "selected")
	selected := runGitTestCmd(t, source, "rev-parse", "HEAD")
	testutil.FailErr(t, "later file", os.WriteFile(filepath.Join(source, "file.txt"), []byte("unpublished later content\n"), 0o644))
	runGitTestCmd(t, source, "commit", "-qam", "unpublished")
	unselected := runGitTestCmd(t, source, "rev-parse", "HEAD")
	unselectedBlob := runGitTestCmd(t, source, "rev-parse", "HEAD:file.txt")
	runGitTestCmd(t, source, "repack", "-a", "-d", "-q")
	runGitTestCmd(t, source, "prune-packed", "-q")
	store, err := NewStore(t.TempDir())
	testutil.FailErr(t, "checkpoint store", err)
	packDir := filepath.Join(CheckpointGitObjectsDir(store.RootDir(), "packed", ""), "pack")
	testutil.FailErr(t, "checkpoint pack directory", os.MkdirAll(packDir, 0o755))
	entries, err := os.ReadDir(filepath.Join(source, ".git", "objects", "pack"))
	testutil.FailErr(t, "source pack", err)
	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		data, err := os.ReadFile(filepath.Join(source, ".git", "objects", "pack", entry.Name()))
		testutil.FailErr(t, "read pack asset", err)
		testutil.FailErr(t, "retain pack asset", os.WriteFile(filepath.Join(packDir, entry.Name()), data, 0o444))
	}
	target := t.TempDir()
	runGitTestCmd(t, target, "init", "-q")
	_, err = ImportAndValidateCheckpointGitRefs(t.Context(), store.RootDir(), "packed", "worker",
		PrivateGitSnapshot{Refs: map[string]string{"refs/heads/selected": selected}}, map[string]string{"primary": target})
	testutil.FailErr(t, "publish selected packed history", err)
	if got := runGitTestCmd(t, target, "show", selected+":file.txt"); got != "selected" {
		t.Fatalf("selected commit tree was not imported: %q", got)
	}
	if got := runGitTestCmd(t, target, "show", selected+"^:file.txt"); got != "ancestor" {
		t.Fatalf("selected history ancestor was not imported: %q", got)
	}
	for _, oid := range []string{unselected, unselectedBlob} {
		_, code, err := gitexec.Run(t.Context(), target, []string{"cat-file", "-e", oid}, gitexec.Opts{Profile: gitexec.ProfileHermetic})
		if err != nil {
			t.Fatalf("probe unselected object: %v", err)
		}
		if code == 0 {
			t.Fatalf("unselected packed object escaped retained store: %s", oid)
		}
	}
	if got := runGitTestCmd(t, source, "rev-parse", "HEAD"); got != unselected {
		t.Fatal("publication changed retained source history")
	}
}
