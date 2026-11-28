package workspace_test

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/lycaon/lycaon/internal/enginepaths"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/workspace"
)

// TestReconcileStaleSandboxesWaitsAndRevalidatesUnderPreparationGuard verifies
// that a retention snapshot taken while a branch is in preparation cannot
// delete either the branch tree or its metadata after durable retention
// commits, while live siblings survive and a genuinely stale candidate is
// still cleaned up.
func TestReconcileStaleSandboxesWaitsAndRevalidatesUnderPreparationGuard(t *testing.T) {
	sandboxRoot := t.TempDir()
	primary := t.TempDir()

	siblingJob, siblingMeta := seedSandboxPair(t, sandboxRoot, primary, "sibling-worker.1")
	staleJob, staleMeta := seedSandboxPair(t, sandboxRoot, primary, "stale-worker.1")
	inFlightID := "inflight-worker.1"

	releasePrep, err := workspace.AcquireBranchPreparation(t.Context(), inFlightID)
	testutil.FailErr(t, "AcquireBranchPreparation", err)
	inFlightJob, inFlightMeta := seedSandboxPair(t, sandboxRoot, primary, inFlightID)

	var mu sync.Mutex
	retained := map[string]struct{}{"sibling-worker.1": {}}
	snapshotTaken := make(chan struct{})
	var snapshotOnce sync.Once

	var settledMu sync.Mutex
	settled := map[string]int{}
	settle := func(_ context.Context, jobID, _ string) error {
		settledMu.Lock()
		settled[jobID]++
		settledMu.Unlock()
		return nil
	}

	reconcileDone := make(chan struct{})
	var removed int
	var reconcileErr error
	go func() {
		defer close(reconcileDone)
		removed, reconcileErr = workspace.ReconcileStaleSandboxes(
			t.Context(), sandboxRoot, primary, settle,
			func(context.Context) (map[string]struct{}, error) {
				mu.Lock()
				cp := make(map[string]struct{}, len(retained))
				for k := range retained {
					cp[k] = struct{}{}
				}
				mu.Unlock()
				snapshotOnce.Do(func() { close(snapshotTaken) })
				return cp, nil
			},
		)
	}()

	<-snapshotTaken
	// Reconcile has taken its initial snapshot (which does not include
	// inFlightID) and must block on the preparation guard before settling or
	// removing inFlightID.
	select {
	case <-reconcileDone:
		t.Fatal("reconcile completed before preparation guard released")
	default:
	}

	// Publish durable retention before releasing the preparation guard.
	mu.Lock()
	retained[inFlightID] = struct{}{}
	mu.Unlock()
	releasePrep()

	<-reconcileDone
	testutil.FailErr(t, "ReconcileStaleSandboxes", reconcileErr)
	if removed != 1 {
		t.Fatalf("removed = %d, want 1 (only stale candidate)", removed)
	}
	for _, kept := range []string{inFlightJob, inFlightMeta, siblingJob, siblingMeta} {
		if _, err := os.Stat(kept); err != nil {
			t.Fatalf("expected %s to survive reconcile: %v", kept, err)
		}
	}
	for _, gone := range []string{staleJob, staleMeta} {
		if _, err := os.Stat(gone); !os.IsNotExist(err) {
			t.Fatalf("expected stale path %s to be removed, err=%v", gone, err)
		}
	}
	settledMu.Lock()
	defer settledMu.Unlock()
	if settled[inFlightID] != 0 {
		t.Fatalf("in-flight branch was settled %d times, want 0", settled[inFlightID])
	}
	if settled["stale-worker.1"] != 1 {
		t.Fatalf("stale branch settled %d times, want 1", settled["stale-worker.1"])
	}
}

// TestReconcileStaleSandboxesProtectsOrphanMetaDuringPreparation verifies that
// metadata created before its branch tree is visible cannot be deleted by
// orphan-meta cleanup while preparation is in flight, and is revalidated once
// preparation completes.
func TestReconcileStaleSandboxesProtectsOrphanMetaDuringPreparation(t *testing.T) {
	sandboxRoot := t.TempDir()
	primary := t.TempDir()

	inFlightID := "meta-first.1"
	releasePrep, err := workspace.AcquireBranchPreparation(t.Context(), inFlightID)
	testutil.FailErr(t, "AcquireBranchPreparation", err)

	jobDir := enginepaths.JobBranchDir(sandboxRoot, primary, inFlightID)
	metaDir := enginepaths.MetaDirForBranchRoot(jobDir)
	testutil.FailErr(t, "mkdir meta", os.MkdirAll(metaDir, 0o700))
	testutil.FailErr(t, "write meta", os.WriteFile(filepath.Join(metaDir, "state.json"), []byte(`{"strategy":"direct_copy"}`), 0o600))

	staleOrphanJob := enginepaths.JobBranchDir(sandboxRoot, primary, "stale-orphan.1")
	staleOrphanMeta := enginepaths.MetaDirForBranchRoot(staleOrphanJob)
	testutil.FailErr(t, "mkdir stale orphan meta", os.MkdirAll(staleOrphanMeta, 0o700))

	var mu sync.Mutex
	retained := map[string]struct{}{}
	snapshotTaken := make(chan struct{})
	var snapshotOnce sync.Once

	reconcileDone := make(chan struct{})
	var removed int
	var reconcileErr error
	go func() {
		defer close(reconcileDone)
		removed, reconcileErr = workspace.ReconcileStaleSandboxes(
			t.Context(), sandboxRoot, primary, nil,
			func(context.Context) (map[string]struct{}, error) {
				mu.Lock()
				cp := make(map[string]struct{}, len(retained))
				for k := range retained {
					cp[k] = struct{}{}
				}
				mu.Unlock()
				snapshotOnce.Do(func() { close(snapshotTaken) })
				return cp, nil
			},
		)
	}()

	<-snapshotTaken
	// Record retention and create the branch directory before releasing the guard.
	testutil.FailErr(t, "mkdir branch", os.MkdirAll(jobDir, 0o755))
	mu.Lock()
	retained[inFlightID] = struct{}{}
	mu.Unlock()
	releasePrep()

	<-reconcileDone
	testutil.FailErr(t, "ReconcileStaleSandboxes", reconcileErr)
	if removed != 1 {
		t.Fatalf("removed = %d, want 1 (only stale orphan meta)", removed)
	}
	if _, err := os.Stat(metaDir); err != nil {
		t.Fatalf("in-flight meta should survive: %v", err)
	}
	if _, err := os.Stat(staleOrphanMeta); !os.IsNotExist(err) {
		t.Fatalf("stale orphan meta should be removed, err=%v", err)
	}
}

// TestReconcileStaleSandboxesReclaimsInterruptedPreparation verifies that when
// a preparation releases its guard without recording durable retention,
// reconcile reclaims both its tree and metadata.
func TestReconcileStaleSandboxesReclaimsInterruptedPreparation(t *testing.T) {
	sandboxRoot := t.TempDir()
	primary := t.TempDir()

	siblingJob, siblingMeta := seedSandboxPair(t, sandboxRoot, primary, "sibling.1")
	abortedID := "aborted.1"

	releasePrep, err := workspace.AcquireBranchPreparation(t.Context(), abortedID)
	testutil.FailErr(t, "AcquireBranchPreparation", err)
	abortedJob, abortedMeta := seedSandboxPair(t, sandboxRoot, primary, abortedID)

	snapshotTaken := make(chan struct{})
	var calls atomic.Int32
	reconcileDone := make(chan struct{})
	var removed int
	var reconcileErr error
	go func() {
		defer close(reconcileDone)
		removed, reconcileErr = workspace.ReconcileStaleSandboxes(
			t.Context(), sandboxRoot, primary, nil,
			func(context.Context) (map[string]struct{}, error) {
				if calls.Add(1) == 1 {
					close(snapshotTaken)
				}
				return map[string]struct{}{"sibling.1": {}}, nil
			},
		)
	}()

	<-snapshotTaken
	// Release without publishing durable retention (interrupted/failed prep).
	releasePrep()

	<-reconcileDone
	testutil.FailErr(t, "ReconcileStaleSandboxes", reconcileErr)
	if removed != 1 {
		t.Fatalf("removed = %d, want 1 (interrupted preparation)", removed)
	}
	for _, gone := range []string{abortedJob, abortedMeta} {
		if _, err := os.Stat(gone); !os.IsNotExist(err) {
			t.Fatalf("interrupted preparation path %s should be removed, err=%v", gone, err)
		}
	}
	for _, kept := range []string{siblingJob, siblingMeta} {
		if _, err := os.Stat(kept); err != nil {
			t.Fatalf("sibling path %s should survive: %v", kept, err)
		}
	}
}

// TestReconcileStaleSandboxesHonorsContextCancelWhileWaiting verifies that a
// reconcile waiting on an active preparation guard unblocks promptly when its
// context is canceled.
func TestReconcileStaleSandboxesHonorsContextCancelWhileWaiting(t *testing.T) {
	sandboxRoot := t.TempDir()
	primary := t.TempDir()

	heldID := "slow-prep.1"
	releasePrep, err := workspace.AcquireBranchPreparation(t.Context(), heldID)
	testutil.FailErr(t, "AcquireBranchPreparation", err)
	defer releasePrep()
	heldJob, heldMeta := seedSandboxPair(t, sandboxRoot, primary, heldID)

	ctx, cancel := context.WithCancel(t.Context())
	snapshotTaken := make(chan struct{})
	reconcileDone := make(chan error, 1)
	go func() {
		_, err := workspace.ReconcileStaleSandboxes(
			ctx, sandboxRoot, primary, nil,
			func(context.Context) (map[string]struct{}, error) {
				close(snapshotTaken)
				return nil, nil
			},
		)
		reconcileDone <- err
	}()

	<-snapshotTaken
	cancel()
	if err := <-reconcileDone; !errors.Is(err, context.Canceled) {
		t.Fatalf("reconcile err = %v, want context.Canceled", err)
	}
	for _, kept := range []string{heldJob, heldMeta} {
		if _, err := os.Stat(kept); err != nil {
			t.Fatalf("held path %s should survive canceled reconcile: %v", kept, err)
		}
	}
}
