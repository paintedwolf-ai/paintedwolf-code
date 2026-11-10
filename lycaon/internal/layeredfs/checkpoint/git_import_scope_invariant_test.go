package checkpoint

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/lycaon/lycaon/internal/gitexec"

	"github.com/lycaon/lycaon/internal/testutil"
)

// A shared retained object store is not the set of objects authorized for this
// checkpoint. Publishing one ref must not export unrelated retained content.
func TestCheckpointGitImportDoesNotPublishUnreferencedObjects(t *testing.T) {
	for _, location := range []string{"store", "retained", "checkpoint"} {
		t.Run(location, func(t *testing.T) {
			root := t.TempDir()
			store, err := NewStore(filepath.Join(root, "recovery", "checkpoints"))
			testutil.FailErr(t, "checkpoint store", err)
			objects := filepath.Join(store.RootDir(), "objects")
			switch location {
			case "retained":
				objects = filepath.Join(root, "privategit", "retained-store", "objects")
			case "checkpoint":
				objects = CheckpointGitObjectsDir(store.RootDir(), "selected-checkpoint", "")
			}
			selected, err := WriteLooseGitObject(objects, "blob", []byte("selected checkpoint content\n"))
			testutil.FailErr(t, "selected object", err)
			unrelated, err := WriteLooseGitObject(objects, "blob", []byte("another worker's unpublished content\n"))
			testutil.FailErr(t, "unrelated object", err)
			target := filepath.Join(root, "source")
			testutil.FailErr(t, "target repository", os.MkdirAll(target, 0o755))
			runGitTestCmd(t, target, "init", "-q")
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
			if got := runGitTestCmd(t, target, "cat-file", "blob", selected); got != "selected checkpoint content" {
				t.Fatalf("selected object content differs: %q", got)
			}
			_, code, probeErr := gitexec.Run(t.Context(), target, []string{"cat-file", "-e", unrelated}, gitexec.Opts{Profile: gitexec.ProfileHermetic})
			testutil.FailErr(t, "probe unselected object", probeErr)
			if code == 0 {
				t.Fatal("unreferenced object escaped retained store")
			}
			_, err = os.Stat(filepath.Join(objects, unrelated[:2], unrelated[2:]))
			testutil.FailErr(t, "unrelated retained object preserved", err)
		})
	}
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

// Established destination branch tips (HEAD / refs/heads/*) bound incremental
// verification so unmodified subtrees and deep ancestor commits are not re-walked
// object-by-object, while missing direct frontier boundary objects fail closed.
func TestCheckpointGitImportEstablishedTargetFrontierAndBoundaryFailure(t *testing.T) {
	target := t.TempDir()
	runGitTestCmd(t, target, "init", "-q")
	testutil.FailErr(t, "mkdir unchanged", os.MkdirAll(filepath.Join(target, "unchanged"), 0o755))
	testutil.FailErr(t, "write deep.txt", os.WriteFile(filepath.Join(target, "unchanged", "deep.txt"), []byte("deep unchanged payload\n"), 0o644))
	testutil.FailErr(t, "write edited.txt", os.WriteFile(filepath.Join(target, "edited.txt"), []byte("v0\n"), 0o644))
	runGitTestCmd(t, target, "add", ".")
	runGitTestCmd(t, target, "commit", "-qm", "c0")
	c0 := runGitTestCmd(t, target, "rev-parse", "HEAD")

	testutil.FailErr(t, "write edited v1", os.WriteFile(filepath.Join(target, "edited.txt"), []byte("v1\n"), 0o644))
	runGitTestCmd(t, target, "commit", "-qam", "c1")
	unchangedTreeOID := runGitTestCmd(t, target, "rev-parse", "HEAD:unchanged")
	deepBlobOID := runGitTestCmd(t, target, "rev-parse", "HEAD:unchanged/deep.txt")

	// Author incremental commit c2 in a separate clone/worktree and export only the c1..c2 delta objects into store.
	workerRepo := t.TempDir()
	runGitTestCmd(t, workerRepo, "clone", "-q", target, ".")
	testutil.FailErr(t, "write edited v2", os.WriteFile(filepath.Join(workerRepo, "edited.txt"), []byte("v2\n"), 0o644))
	runGitTestCmd(t, workerRepo, "commit", "-qam", "c2")
	c2 := runGitTestCmd(t, workerRepo, "rev-parse", "HEAD")
	c2Tree := runGitTestCmd(t, workerRepo, "rev-parse", "HEAD^{tree}")
	v2Blob := runGitTestCmd(t, workerRepo, "rev-parse", "HEAD:edited.txt")

	store, err := NewStore(t.TempDir())
	testutil.FailErr(t, "checkpoint store", err)
	cpObjs := CheckpointGitObjectsDir(store.RootDir(), "chk-frontier", "")
	for _, oid := range []string{c2, c2Tree, v2Blob} {
		srcObj := filepath.Join(workerRepo, ".git", "objects", oid[:2], oid[2:])
		dstObj := filepath.Join(cpObjs, oid[:2], oid[2:])
		testutil.FailErr(t, "copy delta object "+oid, copyFileStreamingAtomic(srcObj, dstObj, 0o444))
	}

	// Remove deep ancestor c0 and deepunchanged blob inside target to prove incremental verification
	// stops at established frontier commit c1 and unmodified subtree entry unchangedTreeOID.
	_ = os.Remove(filepath.Join(target, ".git", "objects", c0[:2], c0[2:]))
	_ = os.Remove(filepath.Join(target, ".git", "objects", deepBlobOID[:2], deepBlobOID[2:]))

	// 1. Corrupting or removing the direct frontier boundary subtree (unchangedTreeOID) must fail closed.
	unchangedTreePath := filepath.Join(target, ".git", "objects", unchangedTreeOID[:2], unchangedTreeOID[2:])
	savedTreeBytes, err := os.ReadFile(unchangedTreePath)
	testutil.FailErr(t, "read unchangedTree", err)
	testutil.FailErr(t, "remove unchangedTree", os.Remove(unchangedTreePath))
	if _, err := ImportAndValidateCheckpointGitRefs(
		t.Context(), store.RootDir(), "chk-frontier-missing-boundary", "worker",
		PrivateGitSnapshot{Refs: map[string]string{"refs/heads/work": c2}, ObjectCheckpoints: []string{"chk-frontier"}},
		map[string]string{"primary": target},
	); err == nil {
		t.Fatal("expected missing direct frontier subtree object to fail verification")
	}

	// 2. Restoring the direct frontier subtree allows bounded incremental import to succeed without walking c0 or deepBlobOID.
	testutil.FailErr(t, "restore unchangedTree", os.WriteFile(unchangedTreePath, savedTreeBytes, 0o444))
	imported, err := ImportAndValidateCheckpointGitRefs(
		t.Context(), store.RootDir(), "chk-frontier", "worker",
		PrivateGitSnapshot{Refs: map[string]string{"refs/heads/work": c2}},
		map[string]string{"primary": target},
	)
	testutil.FailErr(t, "bounded incremental import", err)
	if imported["refs/lycaon/workers/worker/heads/work"] != c2 {
		t.Fatalf("unexpected imported ref: %v", imported)
	}
	if got := runGitTestCmd(t, target, "show", c2+":edited.txt"); got != "v2" {
		t.Fatalf("imported commit edited.txt = %q, want v2", got)
	}
}
