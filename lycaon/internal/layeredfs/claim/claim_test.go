package claim

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/lycaon/lycaon/internal/layeredfs"
	"github.com/lycaon/lycaon/internal/layeredfs/adapter"
	"github.com/lycaon/lycaon/internal/layeredfs/adapter/nfsdarwin"
	"github.com/lycaon/lycaon/internal/layeredfs/privategit"
	gittestsetup "github.com/lycaon/lycaon/internal/testsetup/git"
	"github.com/lycaon/lycaon/internal/testutil"
)

func TestMain(m *testing.M) {
	gittestsetup.Enable()
	os.Exit(m.Run())
}

type testRetentionRegistrar struct {
	mu     sync.Mutex
	seq    int
	active map[string]string // claimID -> viewID
	fail   error
}

func newTestRetentionRegistrar() *testRetentionRegistrar {
	return &testRetentionRegistrar{active: make(map[string]string)}
}

func (r *testRetentionRegistrar) RegisterClaim(ctx context.Context, workerID, viewID string, _ []string) (string, error) {
	if err := ctx.Err(); err != nil {
		return "", err
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.fail != nil {
		return "", r.fail
	}
	r.seq++
	id := fmt.Sprintf("ret-%s-%s-%d", workerID, viewID, r.seq)
	r.active[id] = viewID
	return id, nil
}

func (r *testRetentionRegistrar) ReleaseClaim(claimID string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	delete(r.active, claimID)
	return nil
}

func (r *testRetentionRegistrar) activeCountForView(viewID string) int {
	r.mu.Lock()
	defer r.mu.Unlock()
	n := 0
	for _, v := range r.active {
		if v == viewID {
			n++
		}
	}
	return n
}

func newMockMountedSelector(targetOS string) *adapter.Selector {
	adpID := adapter.AdapterFUSELinux
	if targetOS == "darwin" {
		adpID = adapter.AdapterNFSDarwin
	}
	mounted := adapter.NewMountedAdapter(adapter.MountedAdapterConfig{
		AdapterID: adpID,
		TargetOS:  targetOS,
		MountProbeFn: func(_ context.Context, _, _ string) (adapter.ReasonCode, string, error) {
			return adapter.ReasonNone, "", nil
		},
		VerifyBackingDenialFn: func(_ context.Context, _ string) (bool, error) {
			return true, nil
		},
		MountViewFn: func(ctx context.Context, _ string, roots []adapter.PreparedRoot) error {
			if err := ctx.Err(); err != nil {
				return err
			}
			for _, r := range roots {
				if err := os.MkdirAll(r.WorkspacePath, 0o750); err != nil {
					return err
				}
			}
			return nil
		},
	})
	return adapter.NewSelector(targetOS, mounted, mounted, adapter.NewEagerCopyAdapter(adapter.ReasonNone))
}

func TestThreeRootMountedClaimOperationCountIdentical1kVs2MAndZeroLowerReadDir(t *testing.T) {
	t.Parallel()
	runTrial := func(t *testing.T, label string, diskFiles int, simulatedTotal int64) (layeredfs.Stats, int64) {
		t.Helper()
		tmp := t.TempDir()
		rootA := filepath.Join(tmp, "root-a")
		rootB := filepath.Join(tmp, "root-b")
		rootLarge := filepath.Join(tmp, "root-large")
		for _, d := range []string{rootA, rootB, rootLarge} {
			testutil.FailErr(t, "mkdir root", os.MkdirAll(d, 0o755))
		}
		testutil.FailErr(t, "seed root-a", os.WriteFile(filepath.Join(rootA, "a.txt"), []byte("a\n"), 0o644))
		testutil.FailErr(t, "seed root-b", os.WriteFile(filepath.Join(rootB, "b.txt"), []byte("b\n"), 0o644))
		for i := 0; i < diskFiles; i++ {
			name := filepath.Join(rootLarge, fmt.Sprintf("f_%04d.txt", i))
			testutil.FailErr(t, "seed large file", os.WriteFile(name, []byte("x"), 0o644))
		}

		pgit, err := privategit.NewManager(privategit.ManagerConfig{BackingDir: filepath.Join(tmp, "pgit")})
		testutil.FailErr(t, "privategit.NewManager", err)
		defer func() { _ = pgit.Store().Close() }()

		var mountCalls atomic.Int64
		coord := NewCoordinator(newMockMountedSelector(runtime.GOOS))
		ret := newTestRetentionRegistrar()

		grants := []adapter.RootGrant{
			{RootID: "primary", Label: "primary", SourcePath: rootA, IsPrimary: true},
			{RootID: "docs", Label: "docs", SourcePath: rootB},
			{
				RootID: "large", Label: "large", SourcePath: rootLarge,
				Estimate: &adapter.SourceIndexEstimate{EstimatedEntries: simulatedTotal, EstimatedBytes: simulatedTotal * 64},
			},
		}

		cv, snap, err := coord.ClaimWorkerView(t.Context(), ClaimRequest{
			Identity: ClaimIdentity{WorkerID: "w-" + label, ProjectID: "proj-1", ViewID: "v-" + label, SourceGeneration: 7},
			Storage:  ClaimStorage{RuntimeDir: filepath.Join(tmp, "runtime")},
			Grants:   grants,
			Hooks: ClaimHooks{
				Retention:  ret,
				PrivateGit: pgit,
				VerifyConfinement: func(_ context.Context, pv *adapter.PreparedView) error {
					mountCalls.Add(int64(len(pv.Roots)))
					return nil
				},
			},
		})
		testutil.FailErr(t, "ClaimWorkerView "+label, err)
		defer func() { _ = coord.ReleaseWorkerView(context.Background(), cv.Identity.ViewID, ret) }()

		if snap.Readiness != StateReady || snap.GitInit != GitInitLazyPending || !cv.Metadata.Adapter.PendingGitInit {
			t.Fatalf("%s: expected ready mounted view with lazy git init, got %+v", label, snap)
		}
		stats := cv.Prepared.Core.Stats()
		if stats.LowerDirReads != 0 || stats.LowerLookups != 0 || stats.PhysicalCopyUps != 0 || stats.CapturedBaseBytes != 0 {
			t.Fatalf("%s: mounted claim must perform zero lower directory reads/copy-ups, got %+v", label, stats)
		}
		return stats, mountCalls.Load()
	}

	stats1k, ops1k := runTrial(t, "1k", 1000, 1_000)
	stats2M, ops2M := runTrial(t, "2M", 100, 2_000_000)
	if stats1k != stats2M || ops1k != ops2M {
		t.Fatalf("expected identical O(selected roots) operation counts for 1k vs 2M roots: stats1k=%+v stats2M=%+v ops1k=%d ops2M=%d",
			stats1k, stats2M, ops1k, ops2M)
	}
}

func TestRealNFSDarwinMountedClaimZeroLowerDirReads(t *testing.T) {
	if runtime.GOOS != "darwin" {
		t.Skip("real nfsdarwin loopback mount test runs on darwin")
	}
	tmp := t.TempDir()
	root1 := filepath.Join(tmp, "r1")
	root2 := filepath.Join(tmp, "r2")
	for _, d := range []string{root1, root2} {
		testutil.FailErr(t, "mkdir", os.MkdirAll(d, 0o755))
	}
	for i := 0; i < 200; i++ {
		testutil.FailErr(t, "write file", os.WriteFile(filepath.Join(root1, fmt.Sprintf("f_%03d.txt", i)), []byte("hello\n"), 0o644))
	}
	testutil.FailErr(t, "write r2", os.WriteFile(filepath.Join(root2, "doc.md"), []byte("doc\n"), 0o644))

	nfsAdp := nfsdarwin.New(nfsdarwin.Config{
		ConfinementCheckFn: func(context.Context, adapter.ProbeConfig) error { return nil },
	})
	sel := adapter.NewSelector("darwin", nil, nfsAdp, adapter.NewEagerCopyAdapter(adapter.ReasonNone))
	coord := NewCoordinator(sel)
	ret := newTestRetentionRegistrar()

	cv, snap, err := coord.ClaimWorkerView(t.Context(), ClaimRequest{
		Identity: ClaimIdentity{WorkerID: "w-nfs", ProjectID: "proj-nfs", ViewID: "v-nfs", SourceGeneration: 1},
		Storage:  ClaimStorage{RuntimeDir: filepath.Join(tmp, "rt"), BackingDir: filepath.Join(tmp, "bk")},
		Grants: []adapter.RootGrant{
			{RootID: "primary", Label: "primary", SourcePath: root1, IsPrimary: true},
			{RootID: "docs", Label: "docs", SourcePath: root2},
		},
		Hooks: ClaimHooks{Retention: ret},
	})
	testutil.FailErr(t, "ClaimWorkerView real nfsdarwin", err)
	defer func() { _ = coord.ReleaseWorkerView(context.Background(), "v-nfs", ret) }()

	if snap.Adapter.AdapterID != adapter.AdapterNFSDarwin || snap.Readiness != StateReady || snap.GitInit != GitInitLazyPending {
		t.Fatalf("expected ready nfsdarwin claim with lazy git init, got %+v", snap)
	}
	if st := cv.Prepared.Core.Stats(); st.LowerDirReads != 0 {
		t.Fatalf("expected 0 LowerDirReads during real nfsdarwin claim, got %+v", st)
	}
}

func TestConcurrentWorkerClaimsIndependentIdentitiesMountsJournalsAndCancellation(t *testing.T) {
	t.Parallel()
	tmp := t.TempDir()
	src := filepath.Join(tmp, "src")
	testutil.FailErr(t, "mkdir src", os.MkdirAll(src, 0o755))
	testutil.FailErr(t, "write src/main.go", os.WriteFile(filepath.Join(src, "main.go"), []byte("package main\n"), 0o644))

	coord := NewCoordinator(newMockMountedSelector(runtime.GOOS))
	ret := newTestRetentionRegistrar()
	grants := []adapter.RootGrant{{RootID: "primary", Label: "primary", SourcePath: src, IsPrimary: true}}

	// Pre-claim worker-0 so we verify worker-B cancellation never releases worker-0's retention.
	cv0, _, err := coord.ClaimWorkerView(t.Context(), ClaimRequest{
		Identity: ClaimIdentity{WorkerID: "worker-0", ProjectID: "p", ViewID: "view-0", SourceGeneration: 1},
		Storage:  ClaimStorage{RuntimeDir: filepath.Join(tmp, "rt")},
		Grants:   grants,
		Hooks:    ClaimHooks{Retention: ret},
	})
	testutil.FailErr(t, "ClaimWorkerView worker-0", err)
	defer func() { _ = coord.ReleaseWorkerView(context.Background(), "view-0", ret) }()

	var wg sync.WaitGroup
	var cvA, cvC *ClaimedView
	var errA, errB, errC error

	wg.Add(3)
	go func() {
		defer wg.Done()
		cvA, _, errA = coord.ClaimWorkerView(t.Context(), ClaimRequest{
			Identity: ClaimIdentity{WorkerID: "worker-A", ProjectID: "p", ViewID: "view-A", SourceGeneration: 2},
			Storage:  ClaimStorage{RuntimeDir: filepath.Join(tmp, "rt")},
			Grants:   grants,
			Hooks:    ClaimHooks{Retention: ret},
		})
	}()
	go func() {
		defer wg.Done()
		ctxB, cancelB := context.WithCancel(t.Context())
		_, _, errB = coord.ClaimWorkerView(ctxB, ClaimRequest{
			Identity: ClaimIdentity{WorkerID: "worker-B", ProjectID: "p", ViewID: "view-B", SourceGeneration: 2},
			Storage:  ClaimStorage{RuntimeDir: filepath.Join(tmp, "rt")},
			Grants:   grants,
			Hooks: ClaimHooks{
				Retention: ret,
				Fault: func(_ context.Context, stage ClaimStage) error {
					if stage == StageVerifyConfinement {
						cancelB()
						return context.Canceled
					}
					return nil
				},
			},
		})
	}()
	go func() {
		defer wg.Done()
		cvC, _, errC = coord.ClaimWorkerView(t.Context(), ClaimRequest{
			Identity: ClaimIdentity{WorkerID: "worker-C", ProjectID: "p", ViewID: "view-C", SourceGeneration: 2},
			Storage:  ClaimStorage{RuntimeDir: filepath.Join(tmp, "rt")},
			Grants:   grants,
			Hooks:    ClaimHooks{Retention: ret},
		})
	}()
	wg.Wait()

	testutil.FailErr(t, "worker-A claim", errA)
	testutil.FailErr(t, "worker-C claim", errC)
	defer func() {
		_ = coord.ReleaseWorkerView(context.Background(), "view-A", ret)
		_ = coord.ReleaseWorkerView(context.Background(), "view-C", ret)
	}()
	if !errors.Is(errB, context.Canceled) {
		t.Fatalf("expected worker-B to fail with context.Canceled, got %v", errB)
	}
	if ret.activeCountForView("view-B") != 0 {
		t.Fatalf("expected 0 leaked retention claims for canceled worker-B")
	}
	if ret.activeCountForView("view-0") != 1 || ret.activeCountForView("view-A") != 1 || ret.activeCountForView("view-C") != 1 {
		t.Fatalf("expected active retention claims preserved for view-0, view-A, view-C; got %+v", ret.active)
	}
	if cvA.Prepared.PrimaryWorkPath == cvC.Prepared.PrimaryWorkPath || cvA.Metadata.JournalPath == cvC.Metadata.JournalPath || cv0.RetentionClaimID == cvA.RetentionClaimID {
		t.Fatalf("expected independent mounts, journals, and retention IDs across concurrent workers")
	}
}
