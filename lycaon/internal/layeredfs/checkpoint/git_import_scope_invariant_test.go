package checkpoint

import (
	"bytes"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/gitexec"

	"github.com/lycaon/lycaon/internal/testutil"
)

// A shared retained object store is not the set of objects authorized for this
// checkpoint. Publishing one ref must not export unrelated retained content.
func TestCheckpointGitImportDoesNotPublishUnreferencedObjects(t *testing.T) {
	for _, location := range []string{"store", "retained", "adapter-retained", "checkpoint"} {
		t.Run(location, func(t *testing.T) {
			root := t.TempDir()
			store, err := NewStore(filepath.Join(root, "recovery", "checkpoints"))
			testutil.FailErr(t, "checkpoint store", err)
			objects := filepath.Join(store.RootDir(), "objects")
			switch location {
			case "retained":
				objects = filepath.Join(root, "privategit", "retained-store", "objects")
			case "adapter-retained":
				objects = filepath.Join(root, "worker-branches", ".adapter", "privategit", "retained-store", "objects")
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
	for _, packLoc := range []string{"checkpoint-pack", "retained-pack"} {
		t.Run(packLoc, func(t *testing.T) {
			root := t.TempDir()
			store, err := NewStore(filepath.Join(root, "recovery", "checkpoints"))
			testutil.FailErr(t, "checkpoint store", err)
			packDir := filepath.Join(CheckpointGitObjectsDir(store.RootDir(), "packed", ""), "pack")
			if packLoc == "retained-pack" {
				packDir = filepath.Join(root, "privategit", "retained-store", "objects", "pack")
			}
			testutil.FailErr(t, "candidate pack directory", os.MkdirAll(packDir, 0o755))
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
			target := filepath.Join(root, "target")
			testutil.FailErr(t, "mkdir target", os.MkdirAll(target, 0o755))
			runGitTestCmd(t, target, "init", "-q")
			// Seed an existing packed commit in destination to exercise targetHasPacks.
			testutil.FailErr(t, "seed target file", os.WriteFile(filepath.Join(target, "base.txt"), []byte("target-packed-base\n"), 0o644))
			runGitTestCmd(t, target, "add", "base.txt")
			runGitTestCmd(t, target, "commit", "-qm", "target-base")
			runGitTestCmd(t, target, "repack", "-a", "-d", "-q")
			runGitTestCmd(t, target, "prune-packed", "-q")

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
		})
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

	// Remove deep ancestor c0 and deep unchanged blob inside target to prove incremental verification
	// stops at established frontier commit c1 and unmodified subtree entry unchangedTreeOID.
	c1 := runGitTestCmd(t, target, "rev-parse", "HEAD")
	c1Tree := runGitTestCmd(t, target, "rev-parse", "HEAD^{tree}")
	deepBlobPath := filepath.Join(target, ".git", "objects", deepBlobOID[:2], deepBlobOID[2:])
	savedDeepBlobBytes, err := os.ReadFile(deepBlobPath)
	testutil.FailErr(t, "read deepBlobOID", err)
	c0Path := filepath.Join(target, ".git", "objects", c0[:2], c0[2:])
	savedC0Bytes, err := os.ReadFile(c0Path)
	testutil.FailErr(t, "read c0", err)
	testutil.FailErr(t, "remove c0", os.Remove(c0Path))
	testutil.FailErr(t, "remove deepBlobOID", os.Remove(deepBlobPath))
	for _, absentOID := range []string{c0, deepBlobOID} {
		_, code, probeErr := gitexec.Run(t.Context(), target, []string{"cat-file", "-e", absentOID}, gitexec.Opts{Profile: gitexec.ProfileHermetic})
		testutil.FailErr(t, "probe removed object "+absentOID, probeErr)
		if code == 0 {
			t.Fatalf("expected object %s to be absent in target before incremental import", absentOID)
		}
	}
	assertTargetRefsPreserved := func(step string) {
		t.Helper()
		if got := runGitTestCmd(t, target, "rev-parse", "HEAD"); got != c1 {
			t.Fatalf("%s: target HEAD changed to %s, want %s", step, got, c1)
		}
		if out := runGitTestCmd(t, target, "for-each-ref", "--format=%(refname)", "refs/lycaon/"); out != "" {
			t.Fatalf("%s: unexpected refs/lycaon refs published on failed import: %q", step, out)
		}
	}

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
	assertTargetRefsPreserved("missing-boundary-subtree")

	// 2. Supplying the missing boundary subtree in candidates while its child blob (deepBlobOID) is still
	// unavailable must recursively traverse the candidate-repaired subtree, fail closed without publishing
	// the unverified boundary subtree into target, and fail again on an immediate identical retry.
	candTreePath := filepath.Join(cpObjs, unchangedTreeOID[:2], unchangedTreeOID[2:])
	testutil.FailErr(t, "stage unchangedTree in candidates", os.MkdirAll(filepath.Dir(candTreePath), 0o755))
	testutil.FailErr(t, "write unchangedTree in candidates", os.WriteFile(candTreePath, savedTreeBytes, 0o444))
	for attempt := 1; attempt <= 2; attempt++ {
		if _, err := ImportAndValidateCheckpointGitRefs(
			t.Context(), store.RootDir(), "chk-frontier-incomplete-repair", "worker",
			PrivateGitSnapshot{Refs: map[string]string{"refs/heads/work": c2}, ObjectCheckpoints: []string{"chk-frontier"}},
			map[string]string{"primary": target},
		); err == nil {
			t.Fatalf("attempt %d: expected candidate-repaired boundary subtree with missing child blob to fail verification", attempt)
		}
		if _, statErr := os.Stat(unchangedTreePath); !errors.Is(statErr, os.ErrNotExist) {
			t.Fatalf("attempt %d: failed import prematurely published unverified boundary subtree to target: %v", attempt, statErr)
		}
		assertTargetRefsPreserved("incomplete-candidate-subtree-repair")
	}
	testutil.FailErr(t, "remove candTreePath", os.Remove(candTreePath))

	// 2b. Interrupted publication AFTER boundary subtree is visible in target and BEFORE child blob is durable:
	// both loose and mixed pack+loose retries must not trust the partially published boundary subtree.
	candBlobPath := filepath.Join(cpObjs, deepBlobOID[:2], deepBlobOID[2:])
	testutil.FailErr(t, "mkdir candBlobPath", os.MkdirAll(filepath.Dir(candBlobPath), 0o755))
	testutil.FailErr(t, "write candTreePath for 2b", os.WriteFile(candTreePath, savedTreeBytes, 0o444))
	testutil.FailErr(t, "write candBlobPath for 2b", os.WriteFile(candBlobPath, savedDeepBlobBytes, 0o444))
	origWrap := wrapObjectCopyReaderFn
	t.Cleanup(func() { wrapObjectCopyReaderFn = origWrap })
	wrapObjectCopyReaderFn = func(srcPath string, r io.Reader) io.Reader {
		if srcPath == candBlobPath {
			// Ensure boundary subtree is visible in target before child blob publication fails.
			_ = os.MkdirAll(filepath.Dir(unchangedTreePath), 0o755)
			_ = os.WriteFile(unchangedTreePath, savedTreeBytes, 0o444)
			return bytes.NewReader([]byte("corrupt-zlib"))
		}
		return r
	}
	if _, err := ImportAndValidateCheckpointGitRefs(
		t.Context(), store.RootDir(), "chk-frontier-interrupted-loose", "worker",
		PrivateGitSnapshot{Refs: map[string]string{"refs/heads/work": c2}, ObjectCheckpoints: []string{"chk-frontier"}},
		map[string]string{"primary": target},
	); err == nil {
		t.Fatal("expected interrupted loose publication on child blob to fail")
	}
	if _, statErr := os.Stat(unchangedTreePath); statErr != nil {
		t.Fatalf("expected boundary subtree to be visible in target after partial publication: %v", statErr)
	}
	assertTargetRefsPreserved("interrupted-loose-publication")
	wrapObjectCopyReaderFn = origWrap
	testutil.FailErr(t, "remove candBlobPath before retry", os.Remove(candBlobPath))
	if _, err := ImportAndValidateCheckpointGitRefs(
		t.Context(), store.RootDir(), "chk-frontier-interrupted-loose", "worker",
		PrivateGitSnapshot{Refs: map[string]string{"refs/heads/work": c2}, ObjectCheckpoints: []string{"chk-frontier"}},
		map[string]string{"primary": target},
	); err == nil {
		t.Fatal("expected immediate retry after interrupted loose publication to still fail while child blob is missing")
	}
	assertTargetRefsPreserved("interrupted-loose-publication-retry")

	// 2c. Mixed pack+loose interrupted publication: boundary subtree in candidate pack reaches target/objects/pack
	// via publishSelectedPack before loose child blob fails in publishStagedLoose.
	testutil.FailErr(t, "remove unchangedTreePath for 2c", os.Remove(unchangedTreePath))
	testutil.FailErr(t, "remove candTreePath for 2c", os.Remove(candTreePath))
	candPackDir := filepath.Join(store.RootDir(), "objects", "pack")
	testutil.FailErr(t, "mkdir candPackDir", os.MkdirAll(candPackDir, 0o755))
	_, packCode, packErr := gitexec.Run(
		t.Context(), workerRepo,
		[]string{"pack-objects", "--quiet", filepath.Join(candPackDir, "pack")},
		gitexec.Opts{Profile: gitexec.ProfileHermetic, Input: []byte(unchangedTreeOID + "\n")},
	)
	if packErr != nil || packCode != 0 {
		t.Fatalf("pack unchangedTreeOID into candidate pack (exit %d): %v", packCode, packErr)
	}
	testutil.FailErr(t, "restore candBlobPath for 2c", os.WriteFile(candBlobPath, savedDeepBlobBytes, 0o444))
	wrapObjectCopyReaderFn = func(srcPath string, r io.Reader) io.Reader {
		if srcPath == candBlobPath {
			return bytes.NewReader([]byte("corrupt-zlib"))
		}
		return r
	}
	if _, err := ImportAndValidateCheckpointGitRefs(
		t.Context(), store.RootDir(), "chk-frontier-interrupted-mixed", "worker",
		PrivateGitSnapshot{Refs: map[string]string{"refs/heads/work": c2}, ObjectCheckpoints: []string{"chk-frontier"}},
		map[string]string{"primary": target},
	); err == nil {
		t.Fatal("expected interrupted mixed pack+loose publication to fail on loose child blob")
	}
	if gotType := runGitTestCmd(t, target, "cat-file", "-t", unchangedTreeOID); gotType != "tree" {
		t.Fatalf("expected packed boundary subtree to be visible in target after publishSelectedPack, got %q", gotType)
	}
	assertTargetRefsPreserved("interrupted-mixed-publication")
	wrapObjectCopyReaderFn = origWrap
	testutil.FailErr(t, "remove candBlobPath before mixed retry", os.Remove(candBlobPath))
	if _, err := ImportAndValidateCheckpointGitRefs(
		t.Context(), store.RootDir(), "chk-frontier-interrupted-mixed", "worker",
		PrivateGitSnapshot{Refs: map[string]string{"refs/heads/work": c2}, ObjectCheckpoints: []string{"chk-frontier"}},
		map[string]string{"primary": target},
	); err == nil {
		t.Fatal("expected immediate retry after interrupted mixed pack+loose publication to fail while child blob is missing")
	}
	assertTargetRefsPreserved("interrupted-mixed-publication-retry")
	testutil.FailErr(t, "clean candPackDir", os.RemoveAll(candPackDir))
	testutil.FailErr(t, "clean target pack dir", os.RemoveAll(filepath.Join(target, ".git", "objects", "pack")))
	testutil.FailErr(t, "clean pending publish dir before step 3", os.RemoveAll(filepath.Join(target, ".git", "lycaon-pending-publish")))

	// 3. Restoring unchangedTree in target while removing the frontier commit c1's root tree (c1Tree)
	// must prevent c1 from being admitted to the established frontier and fail closed.
	testutil.FailErr(t, "restore unchangedTree for root-tree check", os.WriteFile(unchangedTreePath, savedTreeBytes, 0o444))
	c1TreePath := filepath.Join(target, ".git", "objects", c1Tree[:2], c1Tree[2:])
	savedC1TreeBytes, err := os.ReadFile(c1TreePath)
	testutil.FailErr(t, "read c1Tree", err)
	testutil.FailErr(t, "remove c1Tree", os.Remove(c1TreePath))
	if _, err := ImportAndValidateCheckpointGitRefs(
		t.Context(), store.RootDir(), "chk-frontier-missing-root-tree", "worker",
		PrivateGitSnapshot{Refs: map[string]string{"refs/heads/work": c2}, ObjectCheckpoints: []string{"chk-frontier"}},
		map[string]string{"primary": target},
	); err == nil {
		t.Fatal("expected frontier commit with missing root tree to not be admitted and fail verification")
	}
	assertTargetRefsPreserved("missing-frontier-commit-root-tree")
	testutil.FailErr(t, "restore c1Tree", os.WriteFile(c1TreePath, savedC1TreeBytes, 0o444))

	// 4. With c1Tree and unchangedTree established in target, bounded incremental import succeeds without walking c0 or deepBlobOID.
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

	// 5. When unchangedTree is absent from target (or marked pending from an interrupted publication) and repaired
	// from intact candidates alongside its child deepBlobOID, retry recovers and passes git fsck --full once c0 is restored.
	pendingDir := filepath.Join(target, ".git", "lycaon-pending-publish")
	testutil.FailErr(t, "mkdir pending publish dir", os.MkdirAll(pendingDir, 0o750))
	testutil.FailErr(t, "write pending publish record for recovery check", os.WriteFile(
		filepath.Join(pendingDir, "pending-manual.json"),
		[]byte(`{"format_version":1,"oids":["`+unchangedTreeOID+`"]}`),
		0o600,
	))
	testutil.FailErr(t, "write candTreePath", os.WriteFile(candTreePath, savedTreeBytes, 0o444))
	testutil.FailErr(t, "write candBlobPath", os.WriteFile(candBlobPath, savedDeepBlobBytes, 0o444))
	_, err = ImportAndValidateCheckpointGitRefs(
		t.Context(), store.RootDir(), "chk-frontier-repaired", "worker",
		PrivateGitSnapshot{Refs: map[string]string{"refs/heads/work": c2}, ObjectCheckpoints: []string{"chk-frontier"}},
		map[string]string{"primary": target},
	)
	testutil.FailErr(t, "complete candidate boundary subtree repair", err)
	if _, statErr := os.Stat(pendingDir); !errors.Is(statErr, os.ErrNotExist) {
		t.Fatalf("expected lycaon-pending-publish to be cleared after complete repair, got %v", statErr)
	}
	testutil.FailErr(t, "restore c0 for full fsck", os.WriteFile(c0Path, savedC0Bytes, 0o444))
	runGitTestCmd(t, target, "fsck", "--full")
}

func TestGitImportStreamingVerificationRejectsMidStreamCorruption(t *testing.T) {
	root := t.TempDir()
	store, err := NewStore(filepath.Join(root, "recovery", "checkpoints"))
	testutil.FailErr(t, "checkpoint store", err)
	cpObjs := CheckpointGitObjectsDir(store.RootDir(), "chk-stream", "")
	payload := bytes.Repeat([]byte("streaming-verification-line\n"), 4096)
	blobOID, err := WriteLooseGitObject(cpObjs, "blob", payload)
	testutil.FailErr(t, "write loose blob", err)

	target := filepath.Join(root, "target")
	testutil.FailErr(t, "mkdir target", os.MkdirAll(target, 0o755))
	runGitTestCmd(t, target, "init", "-q")

	origWrap := wrapObjectCopyReaderFn
	t.Cleanup(func() { wrapObjectCopyReaderFn = origWrap })
	wrapObjectCopyReaderFn = func(_ string, r io.Reader) io.Reader {
		raw, rErr := io.ReadAll(r)
		if rErr != nil || len(raw) == 0 {
			return bytes.NewReader(raw)
		}
		raw[len(raw)/2] ^= 0xff
		return bytes.NewReader(raw)
	}

	if _, err := ImportAndValidateCheckpointGitRefs(
		t.Context(), store.RootDir(), "chk-stream", "worker",
		PrivateGitSnapshot{Refs: map[string]string{"refs/tags/blob": blobOID}},
		map[string]string{"primary": target},
	); err == nil {
		t.Fatal("expected mid-stream loose object corruption to fail verification")
	}
	dstLoose := filepath.Join(target, ".git", "objects", blobOID[:2], blobOID[2:])
	if _, statErr := os.Stat(dstLoose); !errors.Is(statErr, os.ErrNotExist) {
		t.Fatalf("expected corrupted loose object not to be published at destination, got %v", statErr)
	}

	// Also verify mid-stream pack asset corruption is rejected before destination publication.
	packSource := t.TempDir()
	runGitTestCmd(t, packSource, "init", "-q")
	testutil.FailErr(t, "write pack file", os.WriteFile(filepath.Join(packSource, "packed.txt"), payload, 0o644))
	runGitTestCmd(t, packSource, "add", "packed.txt")
	runGitTestCmd(t, packSource, "commit", "-qm", "packed")
	packedCommit := runGitTestCmd(t, packSource, "rev-parse", "HEAD")
	runGitTestCmd(t, packSource, "repack", "-a", "-d", "-q")
	runGitTestCmd(t, packSource, "prune-packed", "-q")

	cpPackDir := filepath.Join(CheckpointGitObjectsDir(store.RootDir(), "chk-pack-stream", ""), "pack")
	testutil.FailErr(t, "mkdir cpPackDir", os.MkdirAll(cpPackDir, 0o755))
	entries, err := os.ReadDir(filepath.Join(packSource, ".git", "objects", "pack"))
	testutil.FailErr(t, "read packSource packs", err)
	for _, d := range entries {
		if !d.IsDir() {
			raw, rErr := os.ReadFile(filepath.Join(packSource, ".git", "objects", "pack", d.Name()))
			testutil.FailErr(t, "read pack asset", rErr)
			testutil.FailErr(t, "write pack asset", os.WriteFile(filepath.Join(cpPackDir, d.Name()), raw, 0o444))
		}
	}
	wrapObjectCopyReaderFn = func(srcPath string, r io.Reader) io.Reader {
		if !strings.HasSuffix(srcPath, ".pack") {
			return r
		}
		raw, _ := io.ReadAll(r)
		if len(raw) > 16 {
			raw[len(raw)/2] ^= 0xff
		}
		return bytes.NewReader(raw)
	}
	if _, err := ImportAndValidateCheckpointGitRefs(
		t.Context(), store.RootDir(), "chk-pack-stream", "worker",
		PrivateGitSnapshot{Refs: map[string]string{"refs/heads/packed": packedCommit}},
		map[string]string{"primary": target},
	); err == nil {
		t.Fatal("expected mid-stream pack corruption to fail digest verification")
	}

	wrapObjectCopyReaderFn = origWrap
	_, err = ImportAndValidateCheckpointGitRefs(
		t.Context(), store.RootDir(), "chk-pack-stream", "worker",
		PrivateGitSnapshot{Refs: map[string]string{"refs/heads/packed": packedCommit}},
		map[string]string{"primary": target},
	)
	testutil.FailErr(t, "import valid pack stream", err)
	runGitTestCmd(t, target, "fsck", "--full")
}

func TestCheckpointGitImportConcurrentPendingPublishAcrossStores(t *testing.T) {
	root := t.TempDir()
	target := filepath.Join(root, "target")
	testutil.FailErr(t, "mkdir target", os.MkdirAll(filepath.Join(target, "unchanged"), 0o755))
	runGitTestCmd(t, target, "init", "-q")
	testutil.FailErr(t, "write deep", os.WriteFile(filepath.Join(target, "unchanged", "deep.txt"), []byte("deep-payload\n"), 0o644))
	testutil.FailErr(t, "write edited", os.WriteFile(filepath.Join(target, "edited.txt"), []byte("v1\n"), 0o644))
	runGitTestCmd(t, target, "add", ".")
	runGitTestCmd(t, target, "commit", "-qm", "c1")
	c1 := runGitTestCmd(t, target, "rev-parse", "HEAD")
	unchangedTreeOID := runGitTestCmd(t, target, "rev-parse", "HEAD:unchanged")
	deepBlobOID := runGitTestCmd(t, target, "rev-parse", "HEAD:unchanged/deep.txt")

	workerB := filepath.Join(root, "worker-b")
	runGitTestCmd(t, root, "clone", "-q", target, workerB)
	testutil.FailErr(t, "edit in workerB", os.WriteFile(filepath.Join(workerB, "edited.txt"), []byte("v2-b\n"), 0o644))
	runGitTestCmd(t, workerB, "add", "edited.txt")
	runGitTestCmd(t, workerB, "commit", "-qm", "cB")
	cB := runGitTestCmd(t, workerB, "rev-parse", "HEAD")
	cBTree := runGitTestCmd(t, workerB, "rev-parse", "HEAD^{tree}")
	editedBlobB := runGitTestCmd(t, workerB, "rev-parse", "HEAD:edited.txt")

	workerA := filepath.Join(root, "worker-a")
	runGitTestCmd(t, root, "clone", "-q", target, workerA)
	testutil.FailErr(t, "write a.txt", os.WriteFile(filepath.Join(workerA, "a.txt"), []byte("worker-a\n"), 0o644))
	runGitTestCmd(t, workerA, "add", "a.txt")
	runGitTestCmd(t, workerA, "commit", "-qm", "cA")
	cA := runGitTestCmd(t, workerA, "rev-parse", "HEAD")
	cATree := runGitTestCmd(t, workerA, "rev-parse", "HEAD^{tree}")
	blobA := runGitTestCmd(t, workerA, "rev-parse", "HEAD:a.txt")

	storeA, err := NewStore(filepath.Join(root, "store-a", "recovery", "checkpoints"))
	testutil.FailErr(t, "storeA", err)
	storeB, err := NewStore(filepath.Join(root, "store-b", "recovery", "checkpoints"))
	testutil.FailErr(t, "storeB", err)
	for _, oid := range []string{cA, cATree, blobA} {
		src := filepath.Join(workerA, ".git", "objects", oid[:2], oid[2:])
		dst := filepath.Join(CheckpointGitObjectsDir(storeA.RootDir(), "chk-a", ""), oid[:2], oid[2:])
		raw, rErr := os.ReadFile(src)
		testutil.FailErr(t, "read A "+oid, rErr)
		testutil.FailErr(t, "mkdir A "+oid, os.MkdirAll(filepath.Dir(dst), 0o755))
		testutil.FailErr(t, "write A "+oid, os.WriteFile(dst, raw, 0o444))
	}
	for _, oid := range []string{cB, cBTree, editedBlobB, unchangedTreeOID, deepBlobOID} {
		src := filepath.Join(workerB, ".git", "objects", oid[:2], oid[2:])
		dst := filepath.Join(CheckpointGitObjectsDir(storeB.RootDir(), "chk-b", ""), oid[:2], oid[2:])
		raw, rErr := os.ReadFile(src)
		testutil.FailErr(t, "read B "+oid, rErr)
		testutil.FailErr(t, "mkdir B "+oid, os.MkdirAll(filepath.Dir(dst), 0o755))
		testutil.FailErr(t, "write B "+oid, os.WriteFile(dst, raw, 0o444))
	}
	// Remove unchangedTreeOID and deepBlobOID from target so Store B must publish both, and fail Store B mid-publication.
	unchangedTreeTarget := filepath.Join(target, ".git", "objects", unchangedTreeOID[:2], unchangedTreeOID[2:])
	deepBlobTarget := filepath.Join(target, ".git", "objects", deepBlobOID[:2], deepBlobOID[2:])
	savedTreeBytes, err := os.ReadFile(unchangedTreeTarget)
	testutil.FailErr(t, "read unchangedTreeTarget", err)
	testutil.FailErr(t, "remove unchangedTreeTarget", os.Remove(unchangedTreeTarget))
	testutil.FailErr(t, "remove deepBlobTarget", os.Remove(deepBlobTarget))
	candBlobB := filepath.Join(CheckpointGitObjectsDir(storeB.RootDir(), "chk-b", ""), deepBlobOID[:2], deepBlobOID[2:])
	savedBlobB, err := os.ReadFile(candBlobB)
	testutil.FailErr(t, "read candBlobB", err)

	origWrap := wrapObjectCopyReaderFn
	t.Cleanup(func() { wrapObjectCopyReaderFn = origWrap })
	var concurrentAErr error
	wrapObjectCopyReaderFn = func(srcPath string, r io.Reader) io.Reader {
		if srcPath == candBlobB {
			_ = os.MkdirAll(filepath.Dir(unchangedTreeTarget), 0o755)
			_ = os.WriteFile(unchangedTreeTarget, savedTreeBytes, 0o444)
			_, concurrentAErr = ImportAndValidateCheckpointGitRefs(
				t.Context(), storeA.RootDir(), "chk-a", "worker-a",
				PrivateGitSnapshot{Refs: map[string]string{"refs/heads/work-a": cA}},
				map[string]string{"primary": target},
			)
			return bytes.NewReader([]byte("corrupt-zlib"))
		}
		return r
	}
	if _, err := ImportAndValidateCheckpointGitRefs(
		t.Context(), storeB.RootDir(), "chk-b", "worker-b",
		PrivateGitSnapshot{Refs: map[string]string{"refs/heads/work-b": cB}},
		map[string]string{"primary": target},
	); err == nil {
		t.Fatal("expected Store B import to fail on child blob")
	}
	wrapObjectCopyReaderFn = origWrap
	testutil.FailErr(t, "concurrent Store A import", concurrentAErr)
	if got := runGitTestCmd(t, target, "rev-parse", "refs/lycaon/workers/worker-a/heads/work-a"); got != cA {
		t.Fatalf("Store A ref = %q, want %q", got, cA)
	}
	if got := runGitTestCmd(t, target, "rev-parse", "HEAD"); got != c1 {
		t.Fatalf("target HEAD = %q, want %q", got, c1)
	}
	// A subsequent unrelated import from Store A after Store B returned must also preserve Store B's pending record.
	_, err = ImportAndValidateCheckpointGitRefs(
		t.Context(), storeA.RootDir(), "chk-a-second", "worker-a",
		PrivateGitSnapshot{Refs: map[string]string{"refs/heads/work-a2": cA}, ObjectCheckpoints: []string{"chk-a"}},
		map[string]string{"primary": target},
	)
	testutil.FailErr(t, "subsequent Store A import", err)
	// Simulate process restart with a fresh Store B instance and missing child blob: Store B's pending record must still force failure.
	testutil.FailErr(t, "remove candBlobB", os.Remove(candBlobB))
	restartedStoreB, err := NewStore(storeB.RootDir())
	testutil.FailErr(t, "reopen storeB", err)
	if _, err := ImportAndValidateCheckpointGitRefs(
		t.Context(), restartedStoreB.RootDir(), "chk-b-retry", "worker-b",
		PrivateGitSnapshot{Refs: map[string]string{"refs/heads/work-b": cB}, ObjectCheckpoints: []string{"chk-b"}},
		map[string]string{"primary": target},
	); err == nil {
		t.Fatal("expected retry across simulated restart to reject partially published unchangedTree after concurrent Store A completion")
	}
	testutil.FailErr(t, "restore candBlobB", os.WriteFile(candBlobB, savedBlobB, 0o444))
	_, err = ImportAndValidateCheckpointGitRefs(
		t.Context(), restartedStoreB.RootDir(), "chk-b-repaired", "worker-b",
		PrivateGitSnapshot{Refs: map[string]string{"refs/heads/work-b": cB}, ObjectCheckpoints: []string{"chk-b"}},
		map[string]string{"primary": target},
	)
	testutil.FailErr(t, "repair Store B after restart", err)
	if _, statErr := os.Stat(filepath.Join(target, ".git", "lycaon-pending-publish")); !errors.Is(statErr, os.ErrNotExist) {
		t.Fatalf("expected lycaon-pending-publish cleaned up after Store B repair, got %v", statErr)
	}
	runGitTestCmd(t, target, "fsck", "--full")
}
