package adapter

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/lycaon/lycaon/internal/layeredfs"
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

func TestWorkspaceSelectionNarrowsMaterializedRootsWhileTaskPathHintsDoNot(t *testing.T) {
	ctx := context.Background()
	tmp := t.TempDir()
	rootA := filepath.Join(tmp, "rootA")
	rootB := filepath.Join(tmp, "rootB")
	_ = os.MkdirAll(filepath.Join(rootA, "pkg", "alpha"), 0o755)
	_ = os.MkdirAll(filepath.Join(rootA, "pkg", "beta"), 0o755)
	_ = os.MkdirAll(filepath.Join(rootB, "docs"), 0o755)
	_ = os.WriteFile(filepath.Join(rootA, "pkg", "alpha", "service.go"), []byte("package alpha\n"), 0o644)
	_ = os.WriteFile(filepath.Join(rootA, "pkg", "beta", "excluded.go"), []byte("package beta\n"), 0o644)
	_ = os.WriteFile(filepath.Join(rootB, "docs", "readme.md"), []byte("# docs\n"), 0o644)

	grants := []RootGrant{
		{RootID: "rootA", Label: "A", SourcePath: rootA, IsPrimary: true},
		{RootID: "rootB", Label: "B", SourcePath: rootB},
	}
	adp := NewEagerCopyAdapter(ReasonNone)

	// Narrow to rootA subroot pkg/alpha, while passing TaskPathHints pointing to rootB and pkg/beta.
	view, err := adp.Prepare(ctx, PrepareRequest{
		ViewID:     "narrowed-view",
		RuntimeDir: filepath.Join(tmp, "runtime"),
		Grants:     grants,
		Selection: WorkspaceSelection{
			RootIDs:  []string{"rootA"},
			Subroots: map[string]string{"rootA": "pkg/alpha"},
		},
		TaskPathHints: []string{"rootB/docs/readme.md", "rootA/pkg/beta/excluded.go"},
	})
	if err != nil {
		t.Fatalf("Prepare narrowed failed: %v", err)
	}
	if len(view.Roots) != 1 || view.Roots[0].RootID != "rootA" || view.Roots[0].Subroot != "pkg/alpha" {
		t.Fatalf("expected single narrowed rootA:pkg/alpha, got %+v", view.Roots)
	}
	if _, err := view.Core.ReadFile(layeredfs.QualifiedPath{RootID: "rootA", Rel: "service.go"}); err != nil {
		t.Fatalf("expected service.go in narrowed subroot, err=%v", err)
	}
	if _, err := view.Core.ReadFile(layeredfs.QualifiedPath{RootID: "rootA", Rel: "excluded.go"}); !os.IsNotExist(err) {
		t.Fatalf("excluded sibling subroot must not be materialized: %v", err)
	}

	// Reject unauthorized root selection.
	_, unauthErr := adp.Prepare(ctx, PrepareRequest{
		ViewID:     "unauth-view",
		RuntimeDir: filepath.Join(tmp, "runtime"),
		Grants:     grants,
		Selection:  WorkspaceSelection{RootIDs: []string{"ungranted"}},
	})
	var aErr *AdapterError
	if !errors.As(unauthErr, &aErr) || aErr.ReasonCode != ReasonUnauthorizedRoot {
		t.Fatalf("expected ReasonUnauthorizedRoot, got %v", unauthErr)
	}
}
