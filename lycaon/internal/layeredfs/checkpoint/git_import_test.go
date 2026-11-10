package checkpoint

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/lycaon/lycaon/internal/gitexec"
	gittestsetup "github.com/lycaon/lycaon/internal/testsetup/git"
	"github.com/lycaon/lycaon/internal/testutil"
)

func TestMain(m *testing.M) {
	gittestsetup.Enable()
	testutil.VerifyNoLeaks(m, nil)
}

func runGitTestCmd(t *testing.T, dir string, args ...string) string {
	t.Helper()
	out, code, err := gitexec.Run(t.Context(), dir, args, gitexec.Opts{
		Profile: gitexec.ProfileHermetic,
		ExtraConfig: []string{
			"user.name=Test",
			"user.email=test@example.com",
		},
	})
	if err != nil || code != 0 {
		t.Fatalf("git %v in %s failed (exit %d): %v\n%s", args, dir, code, err, string(out))
	}
	return strings.TrimSpace(string(out))
}

func TestCheckpointGitImportPackedRepositoryAndSubmoduleGitlink(t *testing.T) {
	t.Parallel()
	srcRepo := t.TempDir()
	runGitTestCmd(t, srcRepo, "init", "-q")
	testutil.FailErr(t, "write file.txt", os.WriteFile(filepath.Join(srcRepo, "file.txt"), []byte("packed-content\n"), 0o644))
	runGitTestCmd(t, srcRepo, "add", "file.txt")

	// Register a 160000 submodule gitlink pointing to an external submodule commit OID not in srcRepo.
	submodOID := "deadbeefdeadbeefdeadbeefdeadbeefdeadbeef"
	runGitTestCmd(t, srcRepo, "update-index", "--add", "--cacheinfo", "160000,"+submodOID+",submod")
	treeOID := runGitTestCmd(t, srcRepo, "write-tree")
	commitOID := runGitTestCmd(t, srcRepo, "commit-tree", treeOID, "-m", "commit with submodule")
	runGitTestCmd(t, srcRepo, "update-ref", "refs/heads/feat", commitOID)
	runGitTestCmd(t, srcRepo, "tag", "-a", "-m", "annotated release", "v1.0", commitOID)
	tagOID := runGitTestCmd(t, srcRepo, "rev-parse", "refs/tags/v1.0")

	// Pack all objects and prune loose objects so reachable validation must read from packfiles.
	runGitTestCmd(t, srcRepo, "repack", "-a", "-d", "-q")
	runGitTestCmd(t, srcRepo, "prune-packed", "-q")

	storeDir := t.TempDir()
	store, err := NewStore(storeDir)
	testutil.FailErr(t, "NewStore", err)

	cpPackDir := filepath.Join(store.RootDir(), "checkpoints", "chk-pack-1", "git-objects", "pack")
	testutil.FailErr(t, "mkdir cpPackDir", os.MkdirAll(cpPackDir, 0o755))
	packEntries, err := os.ReadDir(filepath.Join(srcRepo, ".git", "objects", "pack"))
	testutil.FailErr(t, "ReadDir pack", err)
	for _, pe := range packEntries {
		if !pe.IsDir() {
			srcP := filepath.Join(srcRepo, ".git", "objects", "pack", pe.Name())
			testutil.FailErr(t, "copy pack asset", copyFileStreamingAtomic(srcP, filepath.Join(cpPackDir, pe.Name()), 0o444))
		}
	}

	dstRepo := t.TempDir()
	runGitTestCmd(t, dstRepo, "init", "-q")

	imported, err := ImportAndValidateCheckpointGitRefs(
		t.Context(),
		store.RootDir(),
		"chk-pack-1",
		"w-pack",
		PrivateGitSnapshot{Refs: map[string]string{
			"refs/heads/feat": commitOID,
			"refs/tags/v1.0":  tagOID,
		}},
		map[string]string{"primary": dstRepo},
	)
	testutil.FailErr(t, "ImportAndValidateCheckpointGitRefs packed+submodule", err)

	if imported["refs/lycaon/workers/w-pack/heads/feat"] != commitOID || imported["refs/lycaon/checkpoints/chk-pack-1/tags/v1.0"] != tagOID {
		t.Fatalf("unexpected imported refs: %v", imported)
	}
	gotRef := runGitTestCmd(t, dstRepo, "rev-parse", "refs/lycaon/workers/w-pack/heads/feat")
	if gotRef != commitOID {
		t.Fatalf("rev-parse feat = %s, want %s", gotRef, commitOID)
	}
}

func TestCheckpointGitImportMalformedNestedOIDsAndInvalidRefNames(t *testing.T) {
	t.Parallel()
	dstRepo := t.TempDir()
	runGitTestCmd(t, dstRepo, "init", "-q")
	storeDir := t.TempDir()
	objsDir := filepath.Join(storeDir, "objects")

	// Validly hashed commit with malformed short tree OID "x" must return error and never panic.
	badCommitOID, err := WriteLooseGitObject(objsDir, "commit", []byte("tree x\nauthor A <a@b.c> 1 +0000\ncommitter A <a@b.c> 1 +0000\n\nmsg\n"))
	testutil.FailErr(t, "write bad commit", err)
	_, err = ImportAndValidateCheckpointGitRefs(
		t.Context(), storeDir, "chk-bad-1", "w-bad",
		PrivateGitSnapshot{Refs: map[string]string{"refs/heads/bad": badCommitOID}},
		map[string]string{"primary": dstRepo},
	)
	if err == nil || !strings.Contains(err.Error(), "invalid tree target oid") {
		t.Fatalf("expected invalid tree target oid error, got %v", err)
	}

	// Validly hashed tag with malformed short object OID "y" must return error and never panic.
	badTagOID, err := WriteLooseGitObject(objsDir, "tag", []byte("object y\ntype commit\ntag v1\ntagger A <a@b.c> 1 +0000\n\nmsg\n"))
	testutil.FailErr(t, "write bad tag", err)
	_, err = ImportAndValidateCheckpointGitRefs(
		t.Context(), storeDir, "chk-bad-2", "w-bad",
		PrivateGitSnapshot{Refs: map[string]string{"refs/tags/bad": badTagOID}},
		map[string]string{"primary": dstRepo},
	)
	if err == nil || !strings.Contains(err.Error(), "invalid object target oid") {
		t.Fatalf("expected invalid object target oid error, got %v", err)
	}

	for _, badRef := range []string{"refs/heads/a..b", "refs/heads/a@{0}", "refs/heads/a.lock", "refs/heads/.hidden", "refs/heads/a~1"} {
		if _, vErr := ValidateGitRefName(badRef); vErr == nil {
			t.Fatalf("expected ValidateGitRefName(%q) to fail", badRef)
		}
	}
}

func TestCheckpointGitImportPackedRefsConcurrentCASAndAuthorizedGitDirs(t *testing.T) {
	t.Parallel()
	baseDir := t.TempDir()
	commonGit := filepath.Join(baseDir, "common-git")
	worktreeGit := filepath.Join(baseDir, "worktree-git")
	primary := filepath.Join(baseDir, "worktree")
	for _, d := range []string{filepath.Join(commonGit, "objects"), worktreeGit, primary} {
		testutil.FailErr(t, "mkdir", os.MkdirAll(d, 0o755))
	}
	testutil.FailErr(t, "write .git", os.WriteFile(filepath.Join(primary, ".git"), []byte("gitdir: ../worktree-git\n"), 0o644))
	testutil.FailErr(t, "write commondir", os.WriteFile(filepath.Join(worktreeGit, "commondir"), []byte("../common-git\n"), 0o644))

	storeParent := filepath.Join(baseDir, "store-parent")
	storeDir := filepath.Join(storeParent, "store")
	_, err := NewStore(storeDir)
	testutil.FailErr(t, "NewStore", err)

	objsDir := filepath.Join(storeDir, "objects")
	oidA, err := WriteLooseGitObject(objsDir, "blob", []byte("blob-A\n"))
	testutil.FailErr(t, "write oidA", err)
	oidB, err := WriteLooseGitObject(objsDir, "blob", []byte("blob-B\n"))
	testutil.FailErr(t, "write oidB", err)

	// 1. Unauthorized external gitdir/commondir must fail with ErrUnauthorizedGitDir.
	if _, err := ImportAndValidateCheckpointGitRefs(
		t.Context(), storeDir, "chk-auth", "w-1",
		PrivateGitSnapshot{Refs: map[string]string{"refs/heads/feat": oidA}},
		map[string]string{"primary": primary},
	); !errors.Is(err, ErrUnauthorizedGitDir) {
		t.Fatalf("expected ErrUnauthorizedGitDir without authorized dirs, got %v", err)
	}

	// 2. Sibling .git/objects outside storeDir must not be discovered as a retained source.
	siblingObjs := filepath.Join(storeParent, ".git", "objects")
	siblingOID, err := WriteLooseGitObject(siblingObjs, "blob", []byte("sibling-only\n"))
	testutil.FailErr(t, "write sibling blob", err)
	if _, err := ImportAndValidateCheckpointGitRefs(
		t.Context(), storeDir, "chk-sib", "w-1",
		PrivateGitSnapshot{Refs: map[string]string{"refs/heads/sib": siblingOID}},
		map[string]string{"primary": primary},
		worktreeGit, commonGit,
	); err == nil {
		t.Fatal("expected sibling .git/objects outside storeDir not to be imported")
	}

	// 3. Conflicting ref in packed-refs (with no loose ref) must be rejected with ErrCheckpointRefConflict.
	packedRefsContent := fmt.Sprintf("# pack-refs with: peeled fully-peeled sorted\n%s refs/lycaon/workers/w-1/heads/packed\n", oidB)
	testutil.FailErr(t, "write packed-refs", os.WriteFile(filepath.Join(commonGit, "packed-refs"), []byte(packedRefsContent), 0o644))
	if _, err := ImportAndValidateCheckpointGitRefs(
		t.Context(), storeDir, "chk-packed-conflict", "w-1",
		PrivateGitSnapshot{Refs: map[string]string{"refs/heads/packed": oidA}},
		map[string]string{"primary": primary},
		worktreeGit, commonGit,
	); !errors.Is(err, ErrCheckpointRefConflict) {
		t.Fatalf("expected ErrCheckpointRefConflict against packed-refs, got %v", err)
	}

	// 4. Crash/retry recovery: pre-seed incomplete GitRefImportRecord and one ref already applied, then retry.
	intentRec := GitRefImportRecord{
		CheckpointID: "chk-crash", WorkerID: "w-1",
		PlannedRefs: map[string]string{
			"refs/lycaon/workers/w-1/heads/ok":           oidA,
			"refs/lycaon/checkpoints/chk-crash/heads/ok": oidA,
		},
	}
	rawIntent, _ := json.Marshal(intentRec)
	testutil.FailErr(t, "write intent", os.WriteFile(filepath.Join(storeDir, "promotions", "git-import-w-1--chk-crash.json"), rawIntent, 0o600))
	if _, err := ImportAndValidateCheckpointGitRefs(
		t.Context(), storeDir, "chk-crash", "w-1",
		PrivateGitSnapshot{Refs: map[string]string{"refs/heads/ok": oidA}},
		map[string]string{"primary": primary},
		worktreeGit, commonGit,
	); err != nil {
		t.Fatalf("crash recovery retry failed: %v", err)
	}

	// 5. Concurrent conflicting imports racing on the same destination ref.
	_, err = ImportAndValidateCheckpointGitRefs(
		t.Context(), storeDir, "chk-seed-b", "w-seed",
		PrivateGitSnapshot{Refs: map[string]string{"refs/heads/seed": oidB}},
		map[string]string{"primary": primary},
		worktreeGit, commonGit,
	)
	testutil.FailErr(t, "seed oidB", err)
	var wg sync.WaitGroup
	errs := make([]error, 2)
	oids := []string{oidA, oidB}
	for i := 0; i < 2; i++ {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()
			_, errs[idx] = ImportAndValidateCheckpointGitRefs(
				context.Background(), "", "", "w-race",
				PrivateGitSnapshot{Refs: map[string]string{"refs/heads/race": oids[idx]}},
				map[string]string{"primary": primary},
				worktreeGit, commonGit,
			)
		}(i)
	}
	wg.Wait()
	if (errs[0] == nil && errs[1] == nil) || (errs[0] != nil && errs[1] != nil) {
		t.Fatalf("expected exactly one concurrent winner and one conflict, got err0=%v err1=%v", errs[0], errs[1])
	}
	loserErr := errs[0]
	if loserErr == nil {
		loserErr = errs[1]
	}
	if !errors.Is(loserErr, ErrCheckpointRefConflict) {
		t.Fatalf("expected loser error to wrap ErrCheckpointRefConflict, got %v", loserErr)
	}
}

type failingAfterBytesReader struct {
	r         io.Reader
	remaining int
}

func (f *failingAfterBytesReader) Read(p []byte) (int, error) {
	if f.remaining <= 0 {
		return 0, errors.New("injected mid-copy read error")
	}
	n, _ := f.r.Read(p[:min(len(p), f.remaining)])
	f.remaining -= n
	return n, errors.New("injected mid-copy read error")
}

func TestCheckpointGitImportCopyFailureAndCorruptDestinationRecovery(t *testing.T) {
	dstRepo, storeDir := t.TempDir(), t.TempDir()
	runGitTestCmd(t, dstRepo, "init", "-q")
	_, err := NewStore(storeDir)
	testutil.FailErr(t, "NewStore", err)

	objsDir := filepath.Join(storeDir, "objects")
	oid, err := WriteLooseGitObject(objsDir, "blob", []byte("atomic-object-payload\n"))
	testutil.FailErr(t, "write blob", err)
	dstObjPath := filepath.Join(dstRepo, ".git", "objects", oid[:2], oid[2:])

	assertNoDestOrTemp := func(label string) {
		t.Helper()
		if _, stErr := os.Lstat(dstObjPath); !errors.Is(stErr, os.ErrNotExist) {
			t.Fatalf("%s: partial destination object must not be published, stat err=%v", label, stErr)
		}
		if entries, _ := os.ReadDir(filepath.Dir(dstObjPath)); len(entries) != 0 {
			t.Fatalf("%s: expected temporary files cleaned up, got %d entries", label, len(entries))
		}
	}
	origWrap, origSync, origClose := wrapObjectCopyReaderFn, syncObjectFileFn, closeObjectFileFn
	t.Cleanup(func() { wrapObjectCopyReaderFn, syncObjectFileFn, closeObjectFileFn = origWrap, origSync, origClose })

	// 1. Mid-copy reader failure must not publish tmpFile or leave temp artifacts.
	wrapObjectCopyReaderFn = func(srcPath string, r io.Reader) io.Reader {
		if strings.HasPrefix(srcPath, storeDir) {
			return &failingAfterBytesReader{r: r, remaining: 4}
		}
		return r
	}
	if _, err := ImportAndValidateCheckpointGitRefs(t.Context(), storeDir, "chk-f1", "w-1", PrivateGitSnapshot{Refs: map[string]string{"refs/heads/f": oid}}, map[string]string{"primary": dstRepo}); err == nil {
		t.Fatal("expected mid-copy failure")
	}
	assertNoDestOrTemp("mid-copy")
	wrapObjectCopyReaderFn = origWrap

	// 2. syncFile failure must not publish tmpFile.
	syncObjectFileFn = func(f *os.File) error {
		if strings.HasPrefix(f.Name(), dstRepo) {
			return errors.New("injected sync failure")
		}
		return origSync(f)
	}
	if _, err := ImportAndValidateCheckpointGitRefs(t.Context(), storeDir, "chk-f2", "w-1", PrivateGitSnapshot{Refs: map[string]string{"refs/heads/f": oid}}, map[string]string{"primary": dstRepo}); err == nil {
		t.Fatal("expected sync failure")
	}
	assertNoDestOrTemp("sync")
	syncObjectFileFn = origSync

	// 3. Close failure must not publish tmpFile.
	closeObjectFileFn = func(f *os.File) error {
		name := f.Name()
		_ = f.Close()
		if strings.HasPrefix(name, dstRepo) {
			return errors.New("injected close failure")
		}
		return nil
	}
	if _, err := ImportAndValidateCheckpointGitRefs(t.Context(), storeDir, "chk-f3", "w-1", PrivateGitSnapshot{Refs: map[string]string{"refs/heads/f": oid}}, map[string]string{"primary": dstRepo}); err == nil {
		t.Fatal("expected close failure")
	}
	assertNoDestOrTemp("close")
	closeObjectFileFn = origClose

	// 4. Pre-existing corrupt/truncated destination object is detected and replaced on retry without poisoning.
	testutil.FailErr(t, "mkdir dst prefix", os.MkdirAll(filepath.Dir(dstObjPath), 0o750))
	testutil.FailErr(t, "seed corrupt dst", os.WriteFile(dstObjPath, []byte("corrupt-partial"), 0o444))
	imported, err := ImportAndValidateCheckpointGitRefs(t.Context(), storeDir, "chk-ok", "w-1", PrivateGitSnapshot{Refs: map[string]string{"refs/heads/f": oid}}, map[string]string{"primary": dstRepo})
	testutil.FailErr(t, "retry import after failures and corrupt dst", err)
	if imported["refs/lycaon/workers/w-1/heads/f"] != oid {
		t.Fatalf("unexpected imported refs: %v", imported)
	}
	// 5. Failed copy against an already-valid destination must preserve the valid destination object intact.
	wrapObjectCopyReaderFn = func(_ string, r io.Reader) io.Reader { return &failingAfterBytesReader{r: r, remaining: 4} }
	if err := copyFileStreamingAtomic(filepath.Join(objsDir, oid[:2], oid[2:]), dstObjPath, 0o444); err == nil {
		t.Fatal("expected direct copyFileStreamingAtomic failure")
	}
	wrapObjectCopyReaderFn = origWrap
	if !isValidDestinationGitAsset(filepath.Join(objsDir, oid[:2], oid[2:]), dstObjPath) {
		t.Fatal("existing valid destination object was clobbered by failed copy")
	}
}
