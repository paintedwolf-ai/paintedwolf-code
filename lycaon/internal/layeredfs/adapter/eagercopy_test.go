package adapter

import (
	"context"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"

	"github.com/lycaon/lycaon/internal/layeredfs"
	"golang.org/x/sys/unix"
)

func TestForcedProbeFailureFullMaterializedWorkerPrivateGitAndCheckpointSuite(t *testing.T) {
	ctx := context.Background()
	tmp := t.TempDir()
	srcRoot := createFixtureRepoWithSubmodule(t, filepath.Join(tmp, "source"))

	forcedFailPrimary := NewMountedAdapter(MountedAdapterConfig{
		AdapterID: AdapterNFSDarwin,
		TargetOS:  "darwin",
		MountProbeFn: func(ctx context.Context, probeDir, backingDir string) (ReasonCode, string, error) {
			return ReasonMountDenied, "forced sandbox mount denial", errors.New("EPERM")
		},
	})
	sel := NewSelector("darwin", nil, forcedFailPrimary, NewEagerCopyAdapter(ReasonNone))

	var progressUpdates []FallbackProgress
	adp, view, probeRes, err := sel.SelectAndPrepare(ctx, ProbeConfig{RuntimeDir: tmp}, PrepareRequest{
		ViewID:     "mat-worker-1",
		RuntimeDir: filepath.Join(tmp, "runtime"),
		Grants:     []RootGrant{{RootID: "repo", Label: "repo", SourcePath: srcRoot, IsPrimary: true}},
		ProgressFn: func(p FallbackProgress) { progressUpdates = append(progressUpdates, p) },
	})
	if err != nil {
		t.Fatalf("SelectAndPrepare failed: %v", err)
	}
	if probeRes.Supported || adp.ID() != AdapterEagerCopy {
		t.Fatalf("expected fallback to eagercopy, got adapter=%s probe=%+v", adp.ID(), probeRes)
	}
	if view.Facts.PreparationKind != PreparationMaterialized || view.Facts.UntouchedSourceVisibility != VisibilityFixed || view.Facts.FallbackReason != ReasonMountDenied {
		t.Fatalf("unexpected materialized facts: %+v", view.Facts)
	}
	if len(progressUpdates) == 0 {
		t.Fatalf("expected progress callbacks during materialized seed")
	}

	verifyMaterializedGitIsolation(t, view)
	verifyFixedSourceVisibilityAndCheckpoint(t, ctx, adp, view, srcRoot)
}

func createFixtureRepoWithSubmodule(t *testing.T, srcRoot string) string {
	t.Helper()
	if err := os.MkdirAll(filepath.Join(srcRoot, ".git", "objects", "aa"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(srcRoot, ".git", "modules", "sub"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(srcRoot, ".git", "HEAD"), []byte("ref: refs/heads/main\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(srcRoot, ".git", "objects", "aa", "11223344556677889900112233445566778899"), []byte("git-object-payload"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(srcRoot, ".git", "modules", "sub", "HEAD"), []byte("ref: refs/heads/sub-branch\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(srcRoot, "sub"), 0o755); err != nil {
		t.Fatal(err)
	}
	// Gitlink file in submodule
	if err := os.WriteFile(filepath.Join(srcRoot, "sub", ".git"), []byte("gitdir: ../.git/modules/sub\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(srcRoot, "sub", "lib.go"), []byte("package sub\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(srcRoot, "main.go"), []byte("package main\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(srcRoot, "untouched.txt"), []byte("initial-fixed-seed"), 0o644); err != nil {
		t.Fatal(err)
	}
	return srcRoot
}

func verifyMaterializedGitIsolation(t *testing.T, view *PreparedView) {
	t.Helper()
	// Raw .git directory and raw gitlink must NOT exist in the materialized seed tree.
	if _, err := os.Lstat(filepath.Join(view.PrimaryWorkPath, ".git")); !os.IsNotExist(err) {
		t.Fatalf("raw .git directory must not be copied into seed tree, err=%v", err)
	}
	if _, err := os.Lstat(filepath.Join(view.PrimaryWorkPath, "sub", ".git")); !os.IsNotExist(err) {
		t.Fatalf("raw submodule gitlink must not be copied into seed tree, err=%v", err)
	}
	if len(view.SeededGitRepos) != 2 {
		t.Fatalf("expected 2 discovered repositories (root + submodule), got %d: %+v", len(view.SeededGitRepos), view.SeededGitRepos)
	}
	// Private Git access through Core must resolve via the interceptor to private retention.
	headBytes, err := view.Core.ReadFile(layeredfs.QualifiedPath{RootID: "repo", Rel: ".git/HEAD"})
	if err != nil || string(headBytes) != "ref: refs/heads/main\n" {
		t.Fatalf("expected private git HEAD read through Core to succeed, got %q err=%v", string(headBytes), err)
	}
}

func verifyFixedSourceVisibilityAndCheckpoint(t *testing.T, ctx context.Context, adp Adapter, view *PreparedView, srcRoot string) {
	t.Helper()
	// Mutate live host source after Prepare; fixed-source visibility must preserve seed content.
	if err := os.WriteFile(filepath.Join(srcRoot, "untouched.txt"), []byte("host-live-mutation"), 0o644); err != nil {
		t.Fatal(err)
	}
	untouchedGot, err := view.Core.ReadFile(layeredfs.QualifiedPath{RootID: "repo", Rel: "untouched.txt"})
	if err != nil || string(untouchedGot) != "initial-fixed-seed" {
		t.Fatalf("materialized view must have fixed source visibility, got %q err=%v", string(untouchedGot), err)
	}

	// Mutate via Core and via direct workspace file edit.
	mainQP := layeredfs.QualifiedPath{RootID: "repo", Rel: "main.go"}
	if _, err := view.Core.WriteFile(mainQP, []byte("package main\n// updated\n"), 0o644); err != nil {
		t.Fatalf("WriteFile main.go failed: %v", err)
	}
	if err := os.WriteFile(filepath.Join(view.PrimaryWorkPath, "sub", "lib.go"), []byte("package sub\n// direct edit\n"), 0o644); err != nil {
		t.Fatalf("direct edit in workspace failed: %v", err)
	}

	receipt, releaseBarrier, err := adp.CheckpointBarrier(ctx, view.ViewID)
	if err != nil || !receipt.DirtyDataFlushed {
		t.Fatalf("CheckpointBarrier failed: receipt=%+v err=%v", receipt, err)
	}
	changes, err := view.Core.EnumerateChanges(true)
	if err != nil {
		t.Fatalf("EnumerateChanges failed: %v", err)
	}
	if len(changes) != 2 {
		t.Fatalf("expected 2 logical changes (main.go and sub/lib.go), got %d: %+v", len(changes), changes)
	}
	for _, ch := range changes {
		if ch.Base.Kind != layeredfs.EntryRegular || ch.Base.SHA256 == "" || ch.Final.SHA256 == ch.Base.SHA256 {
			t.Fatalf("expected pre-captured seed base and distinct final SHA256 for %v, got %+v", ch.Path, ch)
		}
	}
	if err := releaseBarrier(); err != nil {
		t.Fatalf("releaseBarrier failed: %v", err)
	}
	relReceipt, err := adp.Release(ctx, view.ViewID)
	if err != nil || !relReceipt.Settled {
		t.Fatalf("Release failed: %+v err=%v", relReceipt, err)
	}
}

func TestMaterializedLimitsOversizedEstimateUnderestimatedAndUnknownSize(t *testing.T) {
	ctx := context.Background()
	tmp := t.TempDir()
	srcRoot := filepath.Join(tmp, "bigroot")
	_ = os.MkdirAll(filepath.Join(srcRoot, "subA"), 0o755)
	_ = os.MkdirAll(filepath.Join(srcRoot, "subB"), 0o755)
	for i, name := range []string{"subA/f1.txt", "subA/f2.txt", "subB/f3.txt", "subB/f4.txt"} {
		_ = os.WriteFile(filepath.Join(srcRoot, filepath.FromSlash(name)), make([]byte, 200*(i+1)), 0o644)
	}

	adp := NewEagerCopyAdapter(ReasonMountDenied)
	runtimeDir := filepath.Join(tmp, "runtime")

	// 1. Oversized estimate refuses preparation early.
	_, err := adp.Prepare(ctx, PrepareRequest{
		ViewID:     "limit-est",
		RuntimeDir: runtimeDir,
		Grants: []RootGrant{{
			RootID:     "r1",
			SourcePath: srcRoot,
			IsPrimary:  true,
			Estimate:   &SourceIndexEstimate{EstimatedEntries: DefaultMaxEntries + 10, EstimatedBytes: 1024},
		}},
	})
	var limErr *PreparationLimitError
	if !errors.As(err, &limErr) || !limErr.EstimatedRejection || limErr.ReasonCode != ReasonWorkspacePreparationLimit || !limErr.RetryWithSmallerSelection || len(limErr.SuggestedSubroots) != 2 {
		t.Fatalf("expected early PreparationLimitError with suggestions, got %+v (err=%v)", limErr, err)
	}

	// 2. Underestimated root stops at actual entry limit and cleans up incomplete seed.
	_, err = adp.Prepare(ctx, PrepareRequest{
		ViewID:     "limit-underest",
		RuntimeDir: runtimeDir,
		MaxEntries: 3,
		Grants: []RootGrant{{
			RootID:     "r1",
			SourcePath: srcRoot,
			IsPrimary:  true,
			Estimate:   &SourceIndexEstimate{EstimatedEntries: 1, EstimatedBytes: 10},
		}},
	})
	limErr = nil
	if !errors.As(err, &limErr) || limErr.EstimatedRejection || !limErr.CleanedIncompleteSeed || limErr.ObservedEntries <= 3 {
		t.Fatalf("expected actual entry limit failure with cleanup, got %+v (err=%v)", limErr, err)
	}
	if _, statErr := os.Stat(filepath.Join(runtimeDir, "views", "limit-underest")); !os.IsNotExist(statErr) {
		t.Fatalf("incomplete seed directory was not cleaned up: %v", statErr)
	}

	// 3. Unknown-size root stops at actual byte limit mid-copy and cleans up.
	_, err = adp.Prepare(ctx, PrepareRequest{
		ViewID:     "limit-bytes",
		RuntimeDir: runtimeDir,
		MaxBytes:   350,
		Grants:     []RootGrant{{RootID: "r1", SourcePath: srcRoot, IsPrimary: true}},
	})
	limErr = nil
	if !errors.As(err, &limErr) || limErr.EstimatedRejection || !limErr.CleanedIncompleteSeed || limErr.ObservedBytes <= 350 {
		t.Fatalf("expected actual byte limit failure with cleanup, got %+v (err=%v)", limErr, err)
	}
	if _, statErr := os.Stat(filepath.Join(runtimeDir, "views", "limit-bytes")); !os.IsNotExist(statErr) {
		t.Fatalf("incomplete byte-limit seed directory was not cleaned up: %v", statErr)
	}
}

func TestMaterializedCancellationCleansOnlyCanceledPreparation(t *testing.T) {
	ctx := context.Background()
	tmp := t.TempDir()
	srcRoot := filepath.Join(tmp, "src")
	_ = os.MkdirAll(srcRoot, 0o755)
	_ = os.WriteFile(filepath.Join(srcRoot, "a.txt"), []byte("keep-alive"), 0o644)
	_ = os.WriteFile(filepath.Join(srcRoot, "b.txt"), []byte("second"), 0o644)

	runtimeDir := filepath.Join(tmp, "runtime")
	adp := NewEagerCopyAdapter(ReasonMountDenied)

	siblingView, err := adp.Prepare(ctx, PrepareRequest{
		ViewID:     "sibling-active",
		RuntimeDir: runtimeDir,
		Grants:     []RootGrant{{RootID: "r1", SourcePath: srcRoot, IsPrimary: true}},
	})
	if err != nil {
		t.Fatalf("sibling Prepare failed: %v", err)
	}

	cancelCtx, cancel := context.WithCancel(ctx)
	_, cancelErr := adp.Prepare(cancelCtx, PrepareRequest{
		ViewID:     "canceled-prep",
		RuntimeDir: runtimeDir,
		Grants:     []RootGrant{{RootID: "r1", SourcePath: srcRoot, IsPrimary: true}},
		ProgressFn: func(p FallbackProgress) {
			cancel()
		},
	})
	if cancelErr == nil {
		t.Fatalf("expected canceled preparation to fail")
	}
	if _, statErr := os.Stat(filepath.Join(runtimeDir, "views", "canceled-prep")); !os.IsNotExist(statErr) {
		t.Fatalf("canceled view directory must be removed, statErr=%v", statErr)
	}
	// Sibling view must remain untouched and readable.
	got, err := siblingView.Core.ReadFile(layeredfs.QualifiedPath{RootID: "r1", Rel: "a.txt"})
	if err != nil || string(got) != "keep-alive" {
		t.Fatalf("sibling view was disrupted by cancellation: got=%q err=%v", string(got), err)
	}
}

func TestEagerCopySeedMetadataFailureFailsClosedAndUnchangedCaptureIsClean(t *testing.T) {
	ctx := context.Background()
	tmp := t.TempDir()
	srcRoot := filepath.Join(tmp, "src-meta")
	subDir := filepath.Join(srcRoot, "subdir")
	if err := os.MkdirAll(subDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(subDir, 0o775); err != nil {
		t.Fatal(err)
	}
	scriptFile := filepath.Join(subDir, "script.sh")
	if err := os.WriteFile(scriptFile, []byte("#!/bin/sh\necho seed\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(scriptFile, 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("script.sh", filepath.Join(subDir, "link")); err != nil {
		t.Fatal(err)
	}
	roDir := filepath.Join(srcRoot, "rodir")
	if err := os.MkdirAll(roDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(roDir, "inside.txt"), []byte("read-only-dir-child\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(roDir, 0o555); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(roDir, 0o755) })

	xattrSupported := unix.Lsetxattr(scriptFile, "user.seedmeta", []byte("v1"), 0) == nil
	if xattrSupported {
		_ = unix.Lsetxattr(subDir, "user.dirmeta", []byte("d1"), 0)
	}

	adp := NewEagerCopyAdapter(ReasonMountDenied)
	verifySeedMetadataFailuresCleanUp(t, ctx, adp, tmp, srcRoot, xattrSupported)
	verifyPreservedModesAndIntentionalRoDirChmod(t, ctx, adp, tmp, srcRoot, xattrSupported)
}

func verifyPreservedModesAndIntentionalRoDirChmod(
	t *testing.T,
	ctx context.Context,
	adp *EagerCopyAdapter,
	tmp, srcRoot string,
	xattrSupported bool,
) {
	t.Helper()
	view, err := adp.Prepare(ctx, PrepareRequest{
		ViewID:     "meta-clean-view",
		RuntimeDir: filepath.Join(tmp, "rt"),
		Grants:     []RootGrant{{RootID: "repo", Label: "repo", SourcePath: srcRoot, IsPrimary: true}},
	})
	if err != nil {
		t.Fatalf("clean Prepare failed: %v", err)
	}
	defer func() { _, _ = adp.Release(ctx, view.ViewID) }()

	dirInfo, err := os.Lstat(filepath.Join(view.PrimaryWorkPath, "subdir"))
	if err != nil || dirInfo.Mode().Perm() != 0o775 {
		t.Fatalf("expected seeded subdir mode 0775 on disk, err=%v mode=%v", err, dirInfo.Mode())
	}
	roDirPath := filepath.Join(view.PrimaryWorkPath, "rodir")
	roDirInfo, err := os.Lstat(roDirPath)
	if err != nil || roDirInfo.Mode().Perm() != 0o555 {
		t.Fatalf("expected seeded rodir mode 0555 on disk, err=%v mode=%v", err, roDirInfo.Mode())
	}
	roBase, ok := view.Core.CapturedBase(layeredfs.QualifiedPath{RootID: "repo", Rel: "rodir"})
	if !ok || roBase.Mode.Perm() != 0o555 {
		t.Fatalf("expected CapturedBase for rodir mode 0555, ok=%v base=%+v", ok, roBase)
	}
	fileInfo, err := os.Lstat(filepath.Join(view.PrimaryWorkPath, "subdir", "script.sh"))
	if err != nil || fileInfo.Mode().Perm() != 0o750 {
		t.Fatalf("expected seeded script.sh mode 0750 on disk, err=%v mode=%v", err, fileInfo.Mode())
	}

	receipt, relBarrier, err := adp.CheckpointBarrier(ctx, view.ViewID)
	if err != nil {
		t.Fatalf("unchanged CheckpointBarrier failed: %v", err)
	}
	if !receipt.DirtyDataFlushed || view.Core.Stats().LogicalChanges != 0 {
		_ = relBarrier()
		t.Fatalf("expected DirtyDataFlushed and 0 LogicalChanges on unchanged capture, receipt=%+v stats=%+v", receipt, view.Core.Stats())
	}
	changes, err := view.Core.EnumerateChanges(false)
	if err != nil || len(changes) != 0 {
		_ = relBarrier()
		t.Fatalf("expected 0 changes on unchanged capture, err=%v changes=%+v", err, changes)
	}
	if xattrSupported {
		val, err := view.Core.GetXattr(layeredfs.QualifiedPath{RootID: "repo", Rel: "subdir/script.sh"}, "user.seedmeta", true)
		if err != nil || string(val) != "v1" {
			_ = relBarrier()
			t.Fatalf("expected preserved seed xattr user.seedmeta=v1 in Core, err=%v val=%q", err, string(val))
		}
	}
	if err := relBarrier(); err != nil {
		t.Fatalf("relBarrier failed: %v", err)
	}

	// Intentional worker mode edit on rodir (0555 -> 0755) must be captured on the next CheckpointBarrier.
	if err := os.Chmod(roDirPath, 0o755); err != nil {
		t.Fatalf("chmod rodir to 0755 failed: %v", err)
	}
	_, relBarrier2, err := adp.CheckpointBarrier(ctx, view.ViewID)
	if err != nil {
		t.Fatalf("second CheckpointBarrier after rodir chmod failed: %v", err)
	}
	changes2, err := view.Core.EnumerateChanges(true)
	_ = relBarrier2()
	if err != nil || len(changes2) != 1 || changes2[0].Path.Rel != "rodir" ||
		changes2[0].Base.Mode.Perm() != 0o555 || changes2[0].Final.Mode.Perm() != 0o755 {
		t.Fatalf("expected rodir mode change 0555->0755 to be captured, err=%v changes=%+v", err, changes2)
	}
	relReceipt, err := adp.Release(ctx, view.ViewID)
	if err != nil || !relReceipt.Settled {
		t.Fatalf("Release after rodir test failed: receipt=%+v err=%v", relReceipt, err)
	}
}

func verifySeedMetadataFailuresCleanUp(
	t *testing.T,
	ctx context.Context,
	adp *EagerCopyAdapter,
	tmp, srcRoot string,
	xattrSupported bool,
) {
	t.Helper()
	// 1. Destination chmod failure during Prepare must fail closed and clean up target/backing dirs.
	chmodTargetDir := filepath.Join(tmp, "chmod-fail-target")
	chmodBackingDir := filepath.Join(tmp, "chmod-fail-backing")
	chmodPathHook = func(p string, mode fs.FileMode) error {
		if strings.HasSuffix(p, "script.sh") {
			return syscall.EPERM
		}
		return nil
	}
	_, err := adp.Prepare(ctx, PrepareRequest{
		ViewID:         "meta-chmod-fail",
		RuntimeDir:     filepath.Join(tmp, "rt"),
		BackingDir:     chmodBackingDir,
		TargetRootDirs: map[string]string{"repo": chmodTargetDir},
		Grants:         []RootGrant{{RootID: "repo", Label: "repo", SourcePath: srcRoot, IsPrimary: true}},
	})
	chmodPathHook = nil
	if !errors.Is(err, syscall.EPERM) {
		t.Fatalf("expected Prepare to fail with EPERM on destination chmod failure, got %v", err)
	}
	if _, statErr := os.Stat(chmodTargetDir); !errors.Is(statErr, os.ErrNotExist) {
		t.Fatalf("expected failed Prepare to clean up TargetRootDir, statErr=%v", statErr)
	}
	if _, statErr := os.Stat(chmodBackingDir); !errors.Is(statErr, os.ErrNotExist) {
		t.Fatalf("expected failed Prepare to clean up BackingDir, statErr=%v", statErr)
	}
	if _, accErr := adp.CommandAccess("meta-chmod-fail"); accErr == nil {
		t.Fatalf("expected failed Prepare not to publish ready view")
	}

	// 2. Destination writePathXattrs failure during Prepare must fail closed and clean up target/backing dirs.
	if !xattrSupported {
		return
	}
	xattrTargetDir := filepath.Join(tmp, "xattr-fail-target")
	xattrBackingDir := filepath.Join(tmp, "xattr-fail-backing")
	writePathXattrsHook = func(p, name string, value []byte) error {
		if strings.HasSuffix(p, "script.sh") {
			return syscall.EIO
		}
		return nil
	}
	_, err = adp.Prepare(ctx, PrepareRequest{
		ViewID:         "meta-xattr-fail",
		RuntimeDir:     filepath.Join(tmp, "rt"),
		BackingDir:     xattrBackingDir,
		TargetRootDirs: map[string]string{"repo": xattrTargetDir},
		Grants:         []RootGrant{{RootID: "repo", Label: "repo", SourcePath: srcRoot, IsPrimary: true}},
	})
	writePathXattrsHook = nil
	if !errors.Is(err, syscall.EIO) {
		t.Fatalf("expected Prepare to fail with EIO on destination writePathXattrs failure, got %v", err)
	}
	if _, statErr := os.Stat(xattrTargetDir); !errors.Is(statErr, os.ErrNotExist) {
		t.Fatalf("expected failed Prepare to clean up TargetRootDir on xattr error, statErr=%v", statErr)
	}
	if _, accErr := adp.CommandAccess("meta-xattr-fail"); accErr == nil {
		t.Fatalf("expected failed Prepare not to publish ready view on xattr error")
	}
}
