//go:build linux

package fuselinux_test

import (
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"

	"github.com/lycaon/lycaon/internal/layeredfs/adapter"
	"github.com/lycaon/lycaon/internal/layeredfs/adapter/fuselinux"
)

type lockWaitEvent struct {
	unique, start, end uint64
	waiting            bool
}

type lockWaitLog struct {
	events chan lockWaitEvent
}

func newLockWaitLog() *lockWaitLog { return &lockWaitLog{events: make(chan lockWaitEvent, 256)} }

func (l *lockWaitLog) record(unique, start, end uint64, waiting bool) {
	l.events <- lockWaitEvent{unique: unique, start: start, end: end, waiting: waiting}
}

// next returns the next event for the byte range, leaving other ranges' events available to later calls.
func (l *lockWaitLog) next(t *testing.T, start, end uint64, waiting bool) lockWaitEvent {
	t.Helper()
	var found lockWaitEvent
	var held []lockWaitEvent
	awaitCondition(t, "lock wait event", func() bool {
		select {
		case ev := <-l.events:
			if ev.start == start && ev.end == end && ev.waiting == waiting {
				found = ev
				return true
			}
			held = append(held, ev)
		default:
		}
		return false
	})
	for _, ev := range held {
		l.events <- ev
	}
	return found
}

func prepareLockView(t *testing.T, cfg fuselinux.Config, viewID string) (*fuselinux.Adapter, string) {
	t.Helper()
	adp := fuselinux.New(cfg)
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
	prep, err := adp.Prepare(t.Context(), adapter.PrepareRequest{
		ViewID: viewID, BackingDir: backingDir, RuntimeDir: runtimeDir,
		Grants: []adapter.RootGrant{{RootID: "primary", Label: "primary", SourcePath: trunkDir, IsPrimary: true}},
	})
	if err != nil {
		t.Fatalf("Prepare: %v", err)
	}
	t.Cleanup(func() { _, _ = adp.Release(t.Context(), viewID) })
	return adp, prep.PrimaryWorkPath
}

func TestMountedSignalInterruptsOnlyItsBlockedLockAndRestarts(t *testing.T) {
	log := newLockWaitLog()
	adp, mnt := prepareLockView(t, fuselinux.Config{OnLockWait: log.record}, "view-lock-interrupt")
	srv := adp.ServerForView("view-lock-interrupt")
	lockFile := filepath.Join(mnt, "locks.bin")

	procA, procB, procC := startLockWorkerProc(t, "procA"), startLockWorkerProc(t, "procB"), startLockWorkerProc(t, "procC")
	procA.exec("OPEN fd1 "+lockFile, "OK ")
	pidB, tidB := procB.openWithThread(lockFile)
	openC := procC.exec("OPEN fd1 "+lockFile, "OK ")
	pidC := strings.TrimPrefix(openC, "OK ")

	procA.exec("SETLK fd1 WRLCK 0 100", "OK")
	procB.send("SETLKW fd1 WRLCK 0 50")
	procC.send("SETLKW fd1 WRLCK 50 50")
	first := log.next(t, 0, 49, true)
	blockedC := log.next(t, 50, 99, true)

	// A caught signal reaches the waiter through the kernel's FUSE_INTERRUPT. The server ends that
	// request alone, and the kernel restarts the system call as a request with a new identity.
	if err := syscall.Tgkill(pidB, tidB, syscall.SIGUSR1); err != nil {
		t.Fatalf("tgkill: %v", err)
	}
	if settled := log.next(t, 0, 49, false); settled.unique != first.unique {
		t.Fatalf("settled request %d, want interrupted request %d", settled.unique, first.unique)
	}
	restarted := log.next(t, 0, 49, true)
	if restarted.unique == first.unique || restarted.unique == blockedC.unique {
		t.Fatalf("restart reused identity %d (first=%d other=%d)", restarted.unique, first.unique, blockedC.unique)
	}
	if got := srv.LockWaiterCountForTest(); got != 2 {
		t.Fatalf("waiters after restart = %d, want 2", got)
	}

	// A fatal signal ends the restarted request for good, without touching the other waiter.
	if err := procB.cmd.Process.Kill(); err != nil {
		t.Fatalf("kill: %v", err)
	}
	if settled := log.next(t, 0, 49, false); settled.unique != restarted.unique {
		t.Fatalf("settled request %d, want killed request %d", settled.unique, restarted.unique)
	}
	if got := srv.LockWaiterCountForTest(); got != 1 {
		t.Fatalf("waiters after kill = %d, want 1", got)
	}

	// Releasing the conflict grants the surviving waiter; the canceled request acquired nothing.
	procA.exec("SETLK fd1 UNLCK 0 100", "OK")
	procC.recv("OK")
	procA.exec("GETLK fd1 WRLCK 0 50", "UNLCK")
	procA.exec("GETLK fd1 WRLCK 50 50", "HELD WRLCK 50 50 "+pidC)
	procC.exec("SETLK fd1 UNLCK 50 50", "OK")
	procA.exec("CLOSE fd1", "OK")
	procC.exec("CLOSE fd1", "OK")
}

func TestMountedLockSaturationKeepsFileOperationsAndUnlockWorking(t *testing.T) {
	const viewID = "view-lock-saturation"
	log := newLockWaitLog()
	adp, mnt := prepareLockView(t, fuselinux.Config{MaxConcurrentRequests: 2, MaxLockWaiters: 2, OnLockWait: log.record}, viewID)
	srv := adp.ServerForView(viewID)
	lockFile := filepath.Join(mnt, "locks.bin")

	procA, procB, procC, procD := startLockWorkerProc(t, "procA"), startLockWorkerProc(t, "procB"), startLockWorkerProc(t, "procC"), startLockWorkerProc(t, "procD")
	openA := procA.exec("OPEN fd1 "+lockFile, "OK ")
	pidA := strings.TrimPrefix(openA, "OK ")
	procB.exec("OPEN fd1 "+lockFile, "OK ")
	procC.exec("OPEN fd1 "+lockFile, "OK ")
	procD.exec("OPEN fd1 "+lockFile, "OK ")

	procA.exec("SETLK fd1 WRLCK 0 100", "OK")
	procB.send("SETLKW fd1 WRLCK 0 50")
	procC.send("SETLKW fd1 WRLCK 50 50")
	log.next(t, 0, 49, true)
	log.next(t, 50, 99, true)

	// Past the waiter limit a blocking request gets ENOLCK, the only errno its contract allows.
	procD.exec("SETLKW fd1 WRLCK 0 100", "ENOLCK")
	if got := srv.LockWaiterCountForTest(); got != 2 {
		t.Fatalf("waiters = %d, want the limit of 2", got)
	}

	// Waiters occupy no worker, so more concurrent file operations than workers all succeed. A lock
	// holder can do its work and reach its unlock however many others wait on it.
	opErrs := make(chan error, 16)
	for i := range cap(opErrs) {
		go func() {
			name := filepath.Join(mnt, "work-"+strconv.Itoa(i))
			if err := os.WriteFile(name, []byte("held work\n"), 0o644); err != nil {
				opErrs <- err
				return
			}
			_, err := os.Stat(name)
			opErrs <- err
		}()
	}
	for range cap(opErrs) {
		if err := <-opErrs; err != nil {
			t.Fatalf("file operation while waiters are saturated: %v", err)
		}
	}

	procD.exec("GETLK fd1 WRLCK 0 100", "HELD WRLCK 0 100 "+pidA)
	procA.exec("SETLK fd1 UNLCK 0 100", "OK")
	procB.recv("OK")
	procC.recv("OK")

	procB.exec("SETLK fd1 UNLCK 0 50", "OK")
	procC.exec("SETLK fd1 UNLCK 50 50", "OK")
	procD.exec("SETLKW fd1 WRLCK 0 100", "OK")
	procD.exec("SETLK fd1 UNLCK 0 100", "OK")
	for _, p := range []*lockWorkerProc{procA, procB, procC, procD} {
		p.exec("CLOSE fd1", "OK")
	}
}
