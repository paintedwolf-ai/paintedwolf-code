package checkpoint

import (
	"errors"
	"os"
	"path/filepath"
	"testing"

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
