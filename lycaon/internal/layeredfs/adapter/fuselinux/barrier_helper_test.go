package fuselinux

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"sync"
	"testing"

	execpkg "github.com/lycaon/lycaon/internal/exec"
	"github.com/lycaon/lycaon/internal/layeredfs"
	"github.com/lycaon/lycaon/internal/layeredfs/adapter"
)

func setExecutablePathFnForTest(a *Adapter, fn func() (string, error)) {
	helperRegMu.Lock()
	if fn == nil {
		delete(execPathHooks, a)
	} else {
		execPathHooks[a] = fn
	}
	helperRegMu.Unlock()
}

func setOnHelperStartForTest(a *Adapter, fn func(viewID string, pid int)) {
	helperRegMu.Lock()
	if fn == nil {
		delete(helperStartHook, a)
	} else {
		helperStartHook[a] = fn
	}
	helperRegMu.Unlock()
}

func lastHelperPIDForView(a *Adapter, viewID string) int {
	helperRegMu.Lock()
	defer helperRegMu.Unlock()
	if byView := helperTrackers[a]; byView != nil {
		if tr := byView[viewID]; tr != nil {
			return tr.lastPID
		}
	}
	return 0
}

func activeHelperPIDsForView(a *Adapter, viewID string) []int {
	helperRegMu.Lock()
	defer helperRegMu.Unlock()
	byView := helperTrackers[a]
	if byView == nil || byView[viewID] == nil {
		return nil
	}
	pids := make([]int, 0, len(byView[viewID].activePIDs))
	for pid := range byView[viewID].activePIDs {
		pids = append(pids, pid)
	}
	sort.Ints(pids)
	return pids
}

func TestFuseBarrierFlushHelperSubprocessRegistrationAndCancellation(t *testing.T) {
	tmp := t.TempDir()
	trunkDir := filepath.Join(tmp, "trunk")
	mountSimDir := filepath.Join(tmp, "mount-sim")
	for _, d := range []string{trunkDir, mountSimDir} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatalf("mkdir %s: %v", d, err)
		}
	}
	if err := os.WriteFile(filepath.Join(trunkDir, "hello.txt"), []byte("seed\n"), 0o644); err != nil {
		t.Fatalf("write trunk hello.txt: %v", err)
	}
	if err := os.WriteFile(filepath.Join(mountSimDir, "hello.txt"), []byte("updated\n"), 0o644); err != nil {
		t.Fatalf("write mountSim hello.txt: %v", err)
	}

	core, err := layeredfs.New(layeredfs.Config{
		ViewID:     "view-helper-unit",
		BackingDir: filepath.Join(tmp, "backing"),
		Roots:      []layeredfs.RootSpec{{ID: "primary", Label: "primary", TrunkPath: trunkDir, IsPrimary: true}},
	})
	if err != nil {
		t.Fatalf("layeredfs.New: %v", err)
	}
	t.Cleanup(func() { _ = core.ReleaseView() })
	if _, err := core.WriteFile(layeredfs.QualifiedPath{RootID: "primary", Rel: "hello.txt"}, []byte("updated\n"), 0o644); err != nil {
		t.Fatalf("core.WriteFile: %v", err)
	}

	adp := New(Config{})
	st := &viewState{
		view:        &adapter.PreparedView{ViewID: "view-helper-unit", PrimaryRootID: "primary"},
		core:        core,
		primaryRoot: "primary",
		dirtyPaths:  make(map[string]map[string]struct{}),
	}
	rm := &rootMount{mountPath: mountSimDir}

	// 1. Fail-closed when ExecutablePathFn fails (no fallback to in-process syncClientPath).
	setExecutablePathFnForTest(adp, func() (string, error) {
		return "", errors.New("injected executable resolution failure")
	})
	_, _, failErr := adp.syncMountedRootFiles(t.Context(), st, "primary", rm)
	var aErr *adapter.AdapterError
	if !errors.As(failErr, &aErr) || aErr.ReasonCode != adapter.ReasonBarrierFailed {
		t.Fatalf("expected fail-closed ReasonBarrierFailed AdapterError when executable resolution fails, got %v", failErr)
	}
	setExecutablePathFnForTest(adp, nil)

	// 2. Real child helper subprocess execution and PID registration/deregistration.
	filesSynced, dirsSynced, err := adp.syncMountedRootFiles(t.Context(), st, "primary", rm)
	if err != nil {
		t.Fatalf("syncMountedRootFiles with real helper subprocess failed: %v", err)
	}
	if filesSynced < 1 || dirsSynced < 1 {
		t.Fatalf("expected helper subprocess to sync >=1 file and >=1 dir, got files=%d dirs=%d", filesSynced, dirsSynced)
	}
	helperPID := lastHelperPIDForView(adp, "view-helper-unit")
	if helperPID <= 0 || helperPID == os.Getpid() {
		t.Fatalf("expected distinct child helper PID > 0 (self=%d), got %d", os.Getpid(), helperPID)
	}
	if active := activeHelperPIDsForView(adp, "view-helper-unit"); len(active) != 0 {
		t.Fatalf("expected no active helper PIDs after completion, got %v", active)
	}

	// 3. Helper pause gate + context cancellation terminates child helper and clears active PID registration.
	pauseFile := filepath.Join(tmp, "helper.pause")
	t.Setenv(fuseBarrierFlushPauseFileEnv, pauseFile)
	startedCh := make(chan int, 1)
	setOnHelperStartForTest(adp, func(_ string, pid int) {
		select {
		case startedCh <- pid:
		default:
		}
	})
	defer setOnHelperStartForTest(adp, nil)

	cancelCtx, cancel := context.WithCancel(t.Context())
	defer cancel()
	errCh := make(chan error, 1)
	go func() {
		_, _, cErr := adp.syncMountedRootFiles(cancelCtx, st, "primary", rm)
		errCh <- cErr
	}()

	runningPID := <-startedCh
	if runningPID <= 0 || runningPID == os.Getpid() {
		t.Fatalf("expected valid child helper PID, got %d", runningPID)
	}
	if active := activeHelperPIDsForView(adp, "view-helper-unit"); len(active) != 1 || active[0] != runningPID {
		t.Fatalf("expected active helper PID [%d] while paused, got %v", runningPID, active)
	}

	cancel()
	cErr := <-errCh
	if !errors.Is(cErr, context.Canceled) || classifyBarrierError(cErr) != adapter.ReasonContextCanceled {
		t.Fatalf("expected context.Canceled / ReasonContextCanceled after helper cancellation, got %v", cErr)
	}
	if active := activeHelperPIDsForView(adp, "view-helper-unit"); len(active) != 0 {
		t.Fatalf("expected active helper PIDs cleared after cancellation, got %v", active)
	}
}

func TestFuseMountedBarrierFlushCancelAndAbortOrdering(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("real FUSE mount requires linux")
	}
	tmp := t.TempDir()
	trunkDir, backingDir, runtimeDir := filepath.Join(tmp, "trunk"), filepath.Join(tmp, "backing"), filepath.Join(tmp, "runtime")
	for _, d := range []string{trunkDir, backingDir, runtimeDir} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatalf("mkdir %s: %v", d, err)
		}
	}
	if err := os.WriteFile(filepath.Join(trunkDir, "slot.txt"), []byte("v1\n"), 0o644); err != nil {
		t.Fatalf("seed slot.txt: %v", err)
	}

	adp := New(Config{})
	const viewID = "view-mounted-cancel-abort"
	prep, err := adp.Prepare(t.Context(), adapter.PrepareRequest{
		ViewID: viewID, BackingDir: backingDir, RuntimeDir: runtimeDir,
		QuiesceHook: NoopQuiesceHook, FlushClientHook: NoWritableMappingsFlushHook,
		Grants: []adapter.RootGrant{{RootID: "primary", Label: "primary", SourcePath: trunkDir, IsPrimary: true}},
	})
	if err != nil {
		t.Fatalf("Prepare: %v", err)
	}
	t.Cleanup(func() { _, _ = adp.Release(context.Background(), viewID) })

	if _, err := prep.Core.WriteFile(layeredfs.QualifiedPath{RootID: "primary", Rel: "slot.txt"}, []byte("v2\n"), 0o644); err != nil {
		t.Fatalf("Core.WriteFile: %v", err)
	}

	pauseFile := filepath.Join(tmp, "mounted-helper.pause")
	t.Setenv(fuseBarrierFlushPauseFileEnv, pauseFile)
	startedCh := make(chan int, 1)
	setOnHelperStartForTest(adp, func(_ string, pid int) {
		select {
		case startedCh <- pid:
		default:
		}
	})
	defer setOnHelperStartForTest(adp, nil)

	cancelCtx, cancel := context.WithCancel(t.Context())
	defer cancel()
	errCh := make(chan error, 1)
	go func() {
		_, rel, bErr := adp.CheckpointBarrier(cancelCtx, viewID)
		if rel != nil {
			_ = rel()
		}
		errCh <- bErr
	}()

	runningPID := <-startedCh
	if runningPID <= 0 || runningPID == os.Getpid() {
		t.Fatalf("expected mounted CheckpointBarrier to spawn child helper PID, got %d", runningPID)
	}
	if active := activeHelperPIDsForView(adp, viewID); len(active) != 1 || active[0] != runningPID {
		t.Fatalf("expected active helper PID [%d] on mounted view, got %v", runningPID, active)
	}

	// Cancel the in-flight barrier helper before aborting/stopping the FUSE server and releasing the mount.
	cancel()
	cErr := <-errCh
	var aErr *adapter.AdapterError
	if !errors.As(cErr, &aErr) || aErr.ReasonCode != adapter.ReasonContextCanceled || !errors.Is(cErr, context.Canceled) {
		t.Fatalf("expected ReasonContextCanceled AdapterError on barrier cancel, got %v", cErr)
	}
	if active := activeHelperPIDsForView(adp, viewID); len(active) != 0 {
		t.Fatalf("expected active helper PIDs cleared before mount abort/release, got %v", active)
	}
	if err := adp.KillServerForView(viewID); err != nil {
		t.Fatalf("KillServerForView after helper cancellation: %v", err)
	}
	relRec, relErr := adp.Release(context.Background(), viewID)
	if relErr != nil || !relRec.Settled {
		t.Fatalf("Release after helper cancellation and server abort failed: rec=%+v err=%v", relRec, relErr)
	}
}

func TestFuseMountedOneSchedulerSlotForcedGCBarrier(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("real FUSE mount requires linux")
	}
	tmp := t.TempDir()
	trunkDir, backingDir, runtimeDir := filepath.Join(tmp, "trunk"), filepath.Join(tmp, "backing"), filepath.Join(tmp, "runtime")
	for _, d := range []string{trunkDir, backingDir, runtimeDir} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatalf("mkdir %s: %v", d, err)
		}
	}
	if err := os.WriteFile(filepath.Join(trunkDir, "slot.txt"), []byte("v1\n"), 0o644); err != nil {
		t.Fatalf("seed slot.txt: %v", err)
	}

	adp := New(Config{})
	const viewID = "view-mounted-one-slot-gc"
	prep, err := adp.Prepare(t.Context(), adapter.PrepareRequest{
		ViewID: viewID, BackingDir: backingDir, RuntimeDir: runtimeDir,
		QuiesceHook: NoopQuiesceHook, FlushClientHook: NoWritableMappingsFlushHook,
		Grants: []adapter.RootGrant{{RootID: "primary", Label: "primary", SourcePath: trunkDir, IsPrimary: true}},
	})
	if err != nil {
		t.Fatalf("Prepare: %v", err)
	}
	t.Cleanup(func() { _, _ = adp.Release(context.Background(), viewID) })

	defer runtime.GOMAXPROCS(runtime.GOMAXPROCS(1))
	stop := make(chan struct{})
	var collector sync.WaitGroup
	collector.Go(func() {
		for {
			select {
			case <-stop:
				return
			default:
				runtime.GC()
			}
		}
	})
	defer func() {
		close(stop)
		collector.Wait()
	}()

	const rounds = 10
	for round := range rounds {
		out, code, wErr := execpkg.Run(t.Context(), "/bin/sh", []string{"-c", "printf 'round\\n' > slot.txt"}, execpkg.ExecOpts{
			Launch: execpkg.HostLaunch("fuse_one_slot_write"), Dir: prep.PrimaryWorkPath,
		})
		if wErr != nil || code != 0 {
			t.Fatalf("round %d write failed: exit=%d err=%v out=%q", round, code, wErr, string(out))
		}
		if fErr := adp.FlushClientData(t.Context(), viewID); fErr != nil {
			t.Fatalf("round %d FlushClientData under GOMAXPROCS(1)+GC failed: %v", round, fErr)
		}
		rec, release, bErr := adp.CheckpointBarrier(t.Context(), viewID)
		if bErr != nil || !rec.WritersQuiesced || !rec.WritableMappingsFlushed || !rec.OperationsDrained || !rec.DirtyDataFlushed {
			t.Fatalf("round %d CheckpointBarrier under GOMAXPROCS(1)+GC failed: rec=%+v err=%v", round, rec, bErr)
		}
		if rErr := release(); rErr != nil {
			t.Fatalf("round %d release barrier: %v", round, rErr)
		}
		if pid := lastHelperPIDForView(adp, viewID); pid <= 0 || pid == os.Getpid() {
			t.Fatalf("round %d expected child helper PID, got %d", round, pid)
		}
	}
}
