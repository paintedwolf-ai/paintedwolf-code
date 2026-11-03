//go:build linux

package fuselinux_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/lycaon/lycaon/internal/layeredfs/adapter"
	"github.com/lycaon/lycaon/internal/layeredfs/adapter/fuselinux"
)

func waitForBlockedLockRange(t *testing.T, srv *fuselinux.Server, start, end uint64) uint64 {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if u, ok := srv.BlockedLockWaiterForRange(start, end); ok {
			return u
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for blocked F_SETLKW waiter on [%d, %d] (waiters=%v)", start, end, srv.BlockedLockWaiters())
	return 0
}

func TestMountedBlockingLockInterruptionAndSelectiveCancellation(t *testing.T) {
	adp := fuselinux.New(fuselinux.Config{})
	tmp := t.TempDir()
	trunkDir, backingDir, runtimeDir := filepath.Join(tmp, "trunk"), filepath.Join(tmp, "backing"), filepath.Join(tmp, "runtime")
	for _, d := range []string{trunkDir, backingDir, runtimeDir} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatalf("mkdir %s: %v", d, err)
		}
	}
	if err := os.WriteFile(filepath.Join(trunkDir, "locks.bin"), make([]byte, 1024), 0o644); err != nil {
		t.Fatalf("seed locks.bin: %v", err)
	}

	const viewID = "view-lock-interrupt"
	prep, err := adp.Prepare(t.Context(), adapter.PrepareRequest{
		ViewID: viewID, BackingDir: backingDir, RuntimeDir: runtimeDir,
		Grants: []adapter.RootGrant{{RootID: "primary", Label: "primary", SourcePath: trunkDir, IsPrimary: true}},
	})
	if err != nil {
		t.Fatalf("Prepare: %v", err)
	}
	t.Cleanup(func() { _, _ = adp.Release(t.Context(), viewID) })

	srv := adp.ServerForView(viewID)
	procA := startLockWorkerProc(t, "procA")
	procB := startLockWorkerProc(t, "procB")
	procC := startLockWorkerProc(t, "procC")
	lockFile := filepath.Join(prep.PrimaryWorkPath, "locks.bin")

	procA.exec("OPEN fd1 "+lockFile, "OK ")
	procB.exec("OPEN fd1 "+lockFile, "OK ")
	openC := procC.exec("OPEN fd1 "+lockFile, "OK ")
	pidC := strings.TrimPrefix(openC, "OK ")

	// 1. procA holds write lock on [0, 99].
	procA.exec("SETLK fd1 WRLCK 0 100", "OK")

	// 2. procB blocks on [0, 49] and procC blocks on [50, 99] via F_SETLKW.
	procB.send("SETLKW fd1 WRLCK 0 50")
	procC.send("SETLKW fd1 WRLCK 50 50")

	uniqueB := waitForBlockedLockRange(t, srv, 0, 49)
	uniqueC := waitForBlockedLockRange(t, srv, 50, 99)
	if uniqueB == uniqueC {
		t.Fatalf("expected distinct unique request IDs for procB and procC, got %d", uniqueB)
	}

	// 3. Interrupt ONLY procB's request (uniqueB) via FUSE_INTERRUPT on the control lane.
	// procB must return EINTR promptly while procC remains blocked on [50, 99].
	srv.SendInterrupt(uniqueB)
	procB.recv("EINTR")

	waitersAfterInterrupt := srv.BlockedLockWaiters()
	if len(waitersAfterInterrupt) != 1 || waitersAfterInterrupt[0] != uniqueC {
		t.Fatalf("expected only procC (%d) to remain waiting after interrupting procB (%d), got %v", uniqueC, uniqueB, waitersAfterInterrupt)
	}

	// 4. Release procA's [0, 99] lock: procC must acquire [50, 99], while canceled procB's [0, 49] must stay unlocked!
	procA.exec("SETLK fd1 UNLCK 0 100", "OK")
	procC.recv("OK")

	procA.exec("GETLK fd1 WRLCK 0 50", "UNLCK")
	procA.exec("GETLK fd1 WRLCK 50 50", "HELD WRLCK 50 50 "+pidC)

	procC.exec("SETLK fd1 UNLCK 50 50", "OK")
	procA.exec("CLOSE fd1", "OK")
	procB.exec("CLOSE fd1", "OK")
	procC.exec("CLOSE fd1", "OK")
}

func TestMountedLockWaiterSaturationControlLaneUnlockAndRecovery(t *testing.T) {
	adp := fuselinux.New(fuselinux.Config{
		MaxConcurrentRequests: 2,
		MaxLockWaiters:        2,
	})
	tmp := t.TempDir()
	trunkDir, backingDir, runtimeDir := filepath.Join(tmp, "trunk"), filepath.Join(tmp, "backing"), filepath.Join(tmp, "runtime")
	for _, d := range []string{trunkDir, backingDir, runtimeDir} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatalf("mkdir %s: %v", d, err)
		}
	}
	if err := os.WriteFile(filepath.Join(trunkDir, "locks.bin"), make([]byte, 1024), 0o644); err != nil {
		t.Fatalf("seed locks.bin: %v", err)
	}

	const viewID = "view-lock-saturation"
	prep, err := adp.Prepare(t.Context(), adapter.PrepareRequest{
		ViewID: viewID, BackingDir: backingDir, RuntimeDir: runtimeDir,
		Grants: []adapter.RootGrant{{RootID: "primary", Label: "primary", SourcePath: trunkDir, IsPrimary: true}},
	})
	if err != nil {
		t.Fatalf("Prepare: %v", err)
	}
	t.Cleanup(func() { _, _ = adp.Release(t.Context(), viewID) })

	srv := adp.ServerForView(viewID)
	procA := startLockWorkerProc(t, "procA")
	procB := startLockWorkerProc(t, "procB")
	procC := startLockWorkerProc(t, "procC")
	procD := startLockWorkerProc(t, "procD")
	lockFile := filepath.Join(prep.PrimaryWorkPath, "locks.bin")

	openA := procA.exec("OPEN fd1 "+lockFile, "OK ")
	pidA := strings.TrimPrefix(openA, "OK ")
	procB.exec("OPEN fd1 "+lockFile, "OK ")
	procC.exec("OPEN fd1 "+lockFile, "OK ")
	procD.exec("OPEN fd1 "+lockFile, "OK ")

	// 1. procA locks [0, 99].
	procA.exec("SETLK fd1 WRLCK 0 100", "OK")

	// 2. Saturate all 2 general worker & lock-waiter slots with procB ([0, 49]) and procC ([50, 99]).
	procB.send("SETLKW fd1 WRLCK 0 50")
	procC.send("SETLKW fd1 WRLCK 50 50")
	_ = waitForBlockedLockRange(t, srv, 0, 49)
	_ = waitForBlockedLockRange(t, srv, 50, 99)

	// 3. With all 2 worker slots occupied, a 3rd blocking SETLKW from procD is rejected immediately
	// with EAGAIN ("CONFLICT") rather than accumulating unbounded goroutines or hanging.
	procD.exec("SETLKW fd1 WRLCK 0 100", "CONFLICT")
	if got := srv.ActiveRequestCount(); got > 2 {
		t.Fatalf("expected ActiveRequestCount <= 2 under saturation, got %d", got)
	}

	// 4. While general worker slots remain 100% saturated, the dedicated control lane still processes
	// GETLK queries and SETLK(F_UNLCK) unlock operations to drain the blocked waiters and recover!
	procD.exec("GETLK fd1 WRLCK 0 100", "HELD WRLCK 0 100 "+pidA)
	procA.exec("SETLK fd1 UNLCK 0 100", "OK")

	procB.recv("OK")
	procC.recv("OK")

	// 5. After draining the saturated lock waiters, normal lock and file operations succeed cleanly.
	procB.exec("SETLK fd1 UNLCK 0 50", "OK")
	procC.exec("SETLK fd1 UNLCK 50 50", "OK")
	procD.exec("SETLKW fd1 WRLCK 0 100", "OK")
	procD.exec("SETLK fd1 UNLCK 0 100", "OK")

	procA.exec("CLOSE fd1", "OK")
	procB.exec("CLOSE fd1", "OK")
	procC.exec("CLOSE fd1", "OK")
	procD.exec("CLOSE fd1", "OK")
}
