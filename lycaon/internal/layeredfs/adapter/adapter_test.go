package adapter

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"github.com/lycaon/lycaon/internal/layeredfs"
)

func TestSelectorSupportedAndUnsupportedFixtures(t *testing.T) {
	ctx := context.Background()
	tmp := t.TempDir()

	fuseSupported := NewMountedAdapter(MountedAdapterConfig{
		AdapterID: AdapterFUSELinux,
		TargetOS:  "linux",
		MountProbeFn: func(ctx context.Context, probeDir, backingDir string) (ReasonCode, string, error) {
			return ReasonNone, "", nil
		},
	})
	darwinSupported := NewMountedAdapter(MountedAdapterConfig{
		AdapterID: AdapterNFSDarwin,
		TargetOS:  "darwin",
		MountProbeFn: func(ctx context.Context, probeDir, backingDir string) (ReasonCode, string, error) {
			return ReasonNone, "", nil
		},
	})

	source := filepath.Join(tmp, "source")
	if err := os.MkdirAll(source, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(source, "f.txt"), []byte("f"), 0o644); err != nil {
		t.Fatal(err)
	}
	overBudget := func(sel *Selector) ChooseRequest {
		sel.BudgetFor = ZeroBudget
		return ChooseRequest{Probe: ProbeConfig{RuntimeDir: tmp}, Grants: []RootGrant{{RootID: "r", SourcePath: source, IsPrimary: true}}}
	}
	selLinux := NewSelector("linux", fuseSupported, darwinSupported, NewEagerCopyAdapter(ReasonNone))
	linuxChoice, err := selLinux.Choose(ctx, overBudget(selLinux))
	adp, res := linuxChoice.Adapter, linuxChoice.Probe
	if err != nil || !res.Supported || adp.ID() != AdapterFUSELinux {
		t.Fatalf("expected fuselinux supported, got id=%s supported=%v err=%v", adp.ID(), res.Supported, err)
	}
	if !res.MountExercised || !res.ReadExercised || !res.WriteExercised || !res.BackingDenialVerified || !res.CleanedUp {
		t.Fatalf("incomplete probe steps: %+v", res)
	}
	facts := adp.Facts()
	if facts.PreparationKind != PreparationMounted || facts.UntouchedSourceVisibility != VisibilityLive || !facts.SupportsPrivateGit || !facts.SupportsCheckpointBarrier {
		t.Fatalf("unexpected mounted facts: %+v", facts)
	}

	selDarwin := NewSelector("darwin", fuseSupported, darwinSupported, NewEagerCopyAdapter(ReasonNone))
	darwinChoice, err := selDarwin.Choose(ctx, overBudget(selDarwin))
	adpD, resD := darwinChoice.Adapter, darwinChoice.Probe
	if err != nil || !resD.Supported || adpD.ID() != AdapterNFSDarwin {
		t.Fatalf("expected nfsdarwin supported, got id=%s supported=%v err=%v", adpD.ID(), resD.Supported, err)
	}

	var deniedCalls atomic.Int32
	mountDeniedAdp := NewMountedAdapter(MountedAdapterConfig{
		AdapterID: AdapterFUSELinux,
		TargetOS:  "linux",
		MountProbeFn: func(ctx context.Context, probeDir, backingDir string) (ReasonCode, string, error) {
			deniedCalls.Add(1)
			return ReasonMountDenied, "sandbox blocked mount", errors.New("EPERM")
		},
	})
	selDenied := NewSelector("linux", mountDeniedAdp, darwinSupported, NewEagerCopyAdapter(ReasonNone))
	deniedChoice, err := selDenied.Choose(ctx, overBudget(selDenied))
	var denied *AdapterError
	if !errors.As(err, &denied) || denied.ReasonCode != ReasonMountDenied || deniedChoice.Adapter != nil {
		t.Fatalf("permission denial must refuse over-limit preparation: choice=%+v err=%v", deniedChoice, err)
	}
	if deniedCalls.Load() != 1 || deniedChoice.Probe.Attempts != 1 {
		t.Fatalf("permission failure must not be retried: calls=%d attempts=%d", deniedCalls.Load(), deniedChoice.Probe.Attempts)
	}
}

func TestSelectorTransientRetryOnce(t *testing.T) {
	ctx := context.Background()
	tmp := t.TempDir()

	var transientRecoveredCalls atomic.Int32
	recoveringAdp := NewMountedAdapter(MountedAdapterConfig{
		AdapterID: AdapterNFSDarwin,
		TargetOS:  "darwin",
		MountProbeFn: func(ctx context.Context, probeDir, backingDir string) (ReasonCode, string, error) {
			if transientRecoveredCalls.Add(1) == 1 {
				return ReasonTransientStartupFailure, "nfsd loopback warm-up timeout", errors.New("timeout")
			}
			return ReasonNone, "", nil
		},
	})
	source := filepath.Join(tmp, "source")
	if err := os.MkdirAll(source, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(source, "f.txt"), []byte("f"), 0o644); err != nil {
		t.Fatal(err)
	}
	overBudget := func(sel *Selector) ChooseRequest {
		sel.BudgetFor = ZeroBudget
		return ChooseRequest{Probe: ProbeConfig{RuntimeDir: tmp}, Grants: []RootGrant{{RootID: "r", SourcePath: source, IsPrimary: true}}}
	}
	selRecover := NewSelector("darwin", nil, recoveringAdp, NewEagerCopyAdapter(ReasonNone))
	recovered, err := selRecover.Choose(ctx, overBudget(selRecover))
	adp, res := recovered.Adapter, recovered.Probe
	if err != nil || !res.Supported || adp.ID() != AdapterNFSDarwin || res.Attempts != 2 {
		t.Fatalf("expected transient retry to succeed on attempt 2, got id=%s supported=%v attempts=%d err=%v", adp.ID(), res.Supported, res.Attempts, err)
	}

	var persistentTransientCalls atomic.Int32
	failingTransientAdp := NewMountedAdapter(MountedAdapterConfig{
		AdapterID: AdapterNFSDarwin,
		TargetOS:  "darwin",
		MountProbeFn: func(ctx context.Context, probeDir, backingDir string) (ReasonCode, string, error) {
			persistentTransientCalls.Add(1)
			return ReasonTransientStartupFailure, "persistent port bind contention", errors.New("timeout")
		},
	})
	selFail := NewSelector("darwin", nil, failingTransientAdp, NewEagerCopyAdapter(ReasonNone))
	failed, err := selFail.Choose(ctx, overBudget(selFail))
	if persistentTransientCalls.Load() != 2 || failed.Probe.Attempts != 2 {
		t.Fatalf("expected exactly 2 attempts (1 retry), got calls=%d attempts=%d", persistentTransientCalls.Load(), failed.Probe.Attempts)
	}
	var aErr *AdapterError
	if !errors.As(err, &aErr) || aErr.ReasonCode != ReasonTransientStartupFailure || failed.Adapter != nil {
		t.Fatalf("a transient failure must start no copy: choice=%+v err=%v", failed, err)
	}
}

func TestMountedPrepareCommandAccessBarrierAndRelease(t *testing.T) {
	ctx := context.Background()
	tmp := t.TempDir()
	srcRoot := filepath.Join(tmp, "repo")
	if err := os.MkdirAll(filepath.Join(srcRoot, "nested", ".git"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(srcRoot, "hello.txt"), []byte("v1"), 0o644); err != nil {
		t.Fatal(err)
	}

	var mountCalls atomic.Int32
	adp := NewMountedAdapter(MountedAdapterConfig{
		AdapterID: AdapterNFSDarwin,
		TargetOS:  "darwin",
		MountProbeFn: func(ctx context.Context, probeDir, backingDir string) (ReasonCode, string, error) {
			return ReasonNone, "", nil
		},
		MountViewFn: func(ctx context.Context, viewID string, roots []PreparedRoot) error {
			mountCalls.Add(1)
			return nil
		},
	})

	view, err := adp.Prepare(ctx, PrepareRequest{
		ViewID:     "view-mounted-1",
		RuntimeDir: filepath.Join(tmp, "runtime"),
		Grants: []RootGrant{
			{RootID: "main", Label: "main", SourcePath: srcRoot, IsPrimary: true},
		},
	})
	if err != nil {
		t.Fatalf("Prepare failed: %v", err)
	}
	// Mounted Prepare must not discover nested repositories or read lower directories.
	if st := view.Core.Stats(); st.LowerDirReads != 0 || st.LowerLookups != 0 {
		t.Fatalf("mounted Prepare must not scan lower directories, got stats=%+v", st)
	}

	ca1, err := adp.CommandAccess(view.ViewID)
	if err != nil {
		t.Fatalf("CommandAccess 1 failed: %v", err)
	}
	ca2, err := adp.CommandAccess(view.ViewID)
	if err != nil {
		t.Fatalf("CommandAccess 2 failed: %v", err)
	}
	if mountCalls.Load() != 1 || ca1.AccessCount != 1 || ca2.AccessCount != 2 {
		t.Fatalf("CommandAccess must not remount: mountCalls=%d ca1=%d ca2=%d", mountCalls.Load(), ca1.AccessCount, ca2.AccessCount)
	}

	receipt, releaseBarrier, err := adp.CheckpointBarrier(ctx, view.ViewID)
	if err != nil {
		t.Fatalf("CheckpointBarrier failed: %v", err)
	}
	if !receipt.WritersQuiesced || !receipt.OperationsDrained || !receipt.DirtyDataFlushed || !receipt.WritableMappingsFlushed {
		t.Fatalf("incomplete barrier receipt: %+v", receipt)
	}
	// Mutations must be blocked while barrier is active.
	if _, mErr := view.Core.WriteFile(layeredfs.QualifiedPath{RootID: "main", Rel: "hello.txt"}, []byte("v2"), 0o644); !errors.Is(mErr, layeredfs.ErrBarrierActive) {
		t.Fatalf("expected ErrBarrierActive during barrier, got %v", mErr)
	}
	if err := releaseBarrier(); err != nil {
		t.Fatalf("releaseBarrier failed: %v", err)
	}
	if _, mErr := view.Core.WriteFile(layeredfs.QualifiedPath{RootID: "main", Rel: "hello.txt"}, []byte("v2"), 0o644); mErr != nil {
		t.Fatalf("write after barrier release failed: %v", mErr)
	}

	relReceipt, err := adp.Release(ctx, view.ViewID)
	if err != nil || !relReceipt.Settled {
		t.Fatalf("Release failed: receipt=%+v err=%v", relReceipt, err)
	}
}

func TestBarrierFailureReleasesBarrierAndCleanupFailureHonesty(t *testing.T) {
	ctx := context.Background()
	tmp := t.TempDir()
	srcRoot := filepath.Join(tmp, "src")
	_ = os.MkdirAll(srcRoot, 0o755)

	adp := NewMountedAdapter(MountedAdapterConfig{
		AdapterID: AdapterFUSELinux,
		TargetOS:  "linux",
		MountProbeFn: func(ctx context.Context, probeDir, backingDir string) (ReasonCode, string, error) {
			return ReasonNone, "", nil
		},
		UnmountViewFn: func(ctx context.Context, viewID string, runtimeDir string) error {
			return errors.New("device or resource busy")
		},
	})

	view, err := adp.Prepare(ctx, PrepareRequest{
		ViewID:     "view-barrier-fail",
		RuntimeDir: filepath.Join(tmp, "rt"),
		Grants:     []RootGrant{{RootID: "r1", SourcePath: srcRoot, IsPrimary: true}},
		FlushClientHook: func(ctx context.Context, viewID string) error {
			return errors.New("mmap msync EIO")
		},
	})
	if err != nil {
		t.Fatalf("Prepare failed: %v", err)
	}

	_, _, bErr := adp.CheckpointBarrier(ctx, view.ViewID)
	if bErr == nil {
		t.Fatalf("expected barrier failure when FlushClientHook fails")
	}
	// Verify the barrier was released on the failure path so the view is not stuck locked.
	if _, wErr := view.Core.WriteFile(layeredfs.QualifiedPath{RootID: "r1", Rel: "after-fail.txt"}, []byte("ok"), 0o644); wErr != nil {
		t.Fatalf("barrier was not released on failure path: %v", wErr)
	}

	// Verify deadline expiration is not treated as proof of cleanup.
	expiredCtx, cancel := context.WithDeadline(ctx, time.Now().Add(-time.Second))
	defer cancel()
	relReceipt, rErr := adp.Release(expiredCtx, view.ViewID)
	if rErr == nil || relReceipt.Settled || relReceipt.ReasonCode != ReasonCleanupFailed {
		t.Fatalf("expected unsettled release on expired deadline, got receipt=%+v err=%v", relReceipt, rErr)
	}
}

func TestActiveWorkerDataSurvivesAdapterFailure(t *testing.T) {
	ctx := context.Background()
	tmp := t.TempDir()
	srcRoot := filepath.Join(tmp, "src")
	_ = os.MkdirAll(srcRoot, 0o755)
	_ = os.WriteFile(filepath.Join(srcRoot, "tracked.txt"), []byte("original"), 0o644)

	primaryAdp := NewMountedAdapter(MountedAdapterConfig{
		AdapterID: AdapterNFSDarwin,
		TargetOS:  "darwin",
		MountProbeFn: func(ctx context.Context, probeDir, backingDir string) (ReasonCode, string, error) {
			return ReasonNone, "", nil
		},
	})
	sel := NewSelector("darwin", nil, primaryAdp, NewEagerCopyAdapter(ReasonNone))
	sel.BudgetFor = ZeroBudget
	grants := []RootGrant{{RootID: "root", SourcePath: srcRoot, IsPrimary: true}}
	choice, err := sel.Choose(ctx, ChooseRequest{Probe: ProbeConfig{RuntimeDir: tmp}, Grants: grants})
	if err != nil || choice.Adapter.ID() != AdapterNFSDarwin {
		t.Fatalf("Choose failed: %v", err)
	}
	view, err := choice.Adapter.Prepare(ctx, PrepareRequest{ViewID: "active-worker-1", RuntimeDir: filepath.Join(tmp, "runtime"), Grants: grants})
	if err != nil {
		t.Fatalf("Prepare failed: %v", err)
	}
	sel.Registry.Register(view, choice.Adapter)

	// Mutate active worker view.
	qp := layeredfs.QualifiedPath{RootID: "root", Rel: "tracked.txt"}
	if _, err := view.Core.WriteFile(qp, []byte("worker-mutated-data"), 0o644); err != nil {
		t.Fatalf("WriteFile failed: %v", err)
	}

	// Silent transition without checkpoint reconstruction (#1666) must be refused and preserve data.
	recovery, recErr := sel.Registry.RecoverOrTransition(ctx, view.ViewID, ReasonCleanupFailed, nil, false)
	if recErr == nil {
		t.Fatalf("expected RecoverOrTransition without checkpoint reconstruction to return error")
	}
	var aErr *AdapterError
	if !errors.As(recErr, &aErr) || aErr.ReasonCode != ReasonActiveViewMutated {
		t.Fatalf("expected ReasonActiveViewMutated, got %v", recErr)
	}
	if !recovery.Mutated || !recovery.RequiresCheckpointReconstruction || len(recovery.PreservedChanges) != 1 {
		t.Fatalf("expected mutated state to be preserved, got %+v", recovery)
	}

	// Unsupported replacement adapter must not erase active worker data.
	unsupportedReplacement := NewMountedAdapter(MountedAdapterConfig{
		AdapterID: AdapterFUSELinux,
		TargetOS:  "linux",
		MountProbeFn: func(ctx context.Context, probeDir, backingDir string) (ReasonCode, string, error) {
			return ReasonUnsupportedKernel, "no fuse", errors.New("enosys")
		},
	})
	rec2, recErr2 := sel.Registry.RecoverOrTransition(ctx, view.ViewID, ReasonCleanupFailed, unsupportedReplacement, true)
	if recErr2 == nil || len(rec2.PreservedChanges) != 1 {
		t.Fatalf("expected unsupported replacement error with preserved changes, got rec=%+v err=%v", rec2, recErr2)
	}
	gotBytes, err := view.Core.ReadFile(qp)
	if err != nil || string(gotBytes) != "worker-mutated-data" {
		t.Fatalf("active worker data was lost after unsupported replacement: %q err=%v", string(gotBytes), err)
	}
}
