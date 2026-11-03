package fuselinux_test

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"syscall"
	"testing"

	"github.com/lycaon/lycaon/internal/layeredfs"
	"github.com/lycaon/lycaon/internal/layeredfs/adapter"
	"github.com/lycaon/lycaon/internal/layeredfs/adapter/fuselinux"
	"github.com/lycaon/lycaon/internal/layeredfs/checkpoint"
	"golang.org/x/sys/unix"
)

func seedBoundedFlushFixture(t *testing.T, trunkDir, untouchedDir string) []byte {
	t.Helper()
	unreadableTrunk := filepath.Join(trunkDir, "unreadable-000.txt")
	if err := os.WriteFile(unreadableTrunk, []byte("secret\n"), 0o600); err != nil {
		t.Fatalf("write unreadable-000.txt: %v", err)
	}
	if err := os.Chmod(unreadableTrunk, 0o000); err != nil {
		t.Fatalf("chmod 000: %v", err)
	}
	t.Cleanup(func() { _ = os.Chmod(unreadableTrunk, 0o600) })

	if err := syscall.Mkfifo(filepath.Join(trunkDir, "untouched.fifo"), 0o600); err != nil {
		t.Fatalf("mkfifo: %v", err)
	}
	for i := 0; i < 10; i++ {
		p := filepath.Join(untouchedDir, fmt.Sprintf("file-%03d.txt", i))
		if err := os.WriteFile(p, []byte("untouched\n"), 0o644); err != nil {
			t.Fatalf("seed untouched %d: %v", i, err)
		}
	}
	initialMapped := []byte("0123456789abcdef\n")
	if err := os.WriteFile(filepath.Join(trunkDir, "mapped.txt"), initialMapped, 0o644); err != nil {
		t.Fatalf("seed mapped.txt: %v", err)
	}
	if err := os.WriteFile(filepath.Join(trunkDir, "edited.txt"), []byte("before\n"), 0o644); err != nil {
		t.Fatalf("seed edited.txt: %v", err)
	}
	return initialMapped
}

func TestBoundedTrackedPathClientFlushSkipsUntouchedUnreadableAndFIFOEntries(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("real FUSE mount requires linux")
	}
	adp := fuselinux.New(fuselinux.Config{})
	tmp := t.TempDir()
	trunkDir, backingDir, runtimeDir := filepath.Join(tmp, "trunk"), filepath.Join(tmp, "backing"), filepath.Join(tmp, "runtime")
	untouchedDir := filepath.Join(trunkDir, "untouched")
	for _, d := range []string{untouchedDir, backingDir, runtimeDir} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatalf("mkdir %s: %v", d, err)
		}
	}
	initialMapped := seedBoundedFlushFixture(t, trunkDir, untouchedDir)

	const viewID = "view-bounded-flush"
	prep, err := adp.Prepare(t.Context(), adapter.PrepareRequest{
		ViewID: viewID, BackingDir: backingDir, RuntimeDir: runtimeDir,
		Grants: []adapter.RootGrant{{RootID: "primary", Label: "primary", SourcePath: trunkDir, IsPrimary: true}},
	})
	if err != nil {
		t.Fatalf("Prepare: %v", err)
	}
	t.Cleanup(func() {
		adp.SetConfigCallbacksForTest(nil, nil)
		_, _ = adp.Release(t.Context(), viewID)
	})

	if err := os.WriteFile(filepath.Join(prep.PrimaryWorkPath, "edited.txt"), []byte("after-edit\n"), 0o644); err != nil {
		t.Fatalf("write edited.txt: %v", err)
	}
	cmd := exec.CommandContext(t.Context(), "/bin/sh", "-c", "printf 'from-shell\\n' > shell.txt")
	cmd.Dir = prep.PrimaryWorkPath
	fuselinux.DetachMountEntry(cmd)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("shell write failed: %v (%s)", err, string(out))
	}

	mf, err := os.OpenFile(filepath.Join(prep.PrimaryWorkPath, "mapped.txt"), os.O_RDWR, 0o644)
	if err != nil {
		t.Fatalf("OpenFile mapped.txt: %v", err)
	}
	defer func() { _ = mf.Close() }()
	mappedSlice, err := syscall.Mmap(int(mf.Fd()), 0, len(initialMapped), syscall.PROT_READ|syscall.PROT_WRITE, syscall.MAP_SHARED)
	if err != nil {
		t.Fatalf("Mmap: %v", err)
	}
	defer func() { _ = syscall.Munmap(mappedSlice) }()
	copyToMapping(mappedSlice, []byte("bounded-mmap-ok!\n"))

	var syncMu sync.Mutex
	var syncedPaths []string
	adp.SetConfigCallbacksForTest(func(p string) error {
		syncMu.Lock()
		syncedPaths = append(syncedPaths, p)
		syncMu.Unlock()
		return nil
	}, nil)
	_ = adp.SetBarrierHooks(viewID, fuselinux.NoopQuiesceHook, func(_ context.Context, _ string) error {
		if err := unix.Msync(mappedSlice, unix.MS_SYNC); err != nil {
			return err
		}
		return mf.Sync()
	})

	rec1, rel1, err := adp.CheckpointBarrier(t.Context(), viewID)
	if err != nil || !rec1.DirtyDataFlushed {
		t.Fatalf("CheckpointBarrier 1 failed: rec=%+v err=%v", rec1, err)
	}
	_ = rel1()
	files1, dirs1 := adp.LastFlushStatsForView(viewID)
	syncMu.Lock()
	for _, p := range syncedPaths {
		if strings.Contains(p, "unreadable-000") || strings.Contains(p, "untouched.fifo") || strings.Contains(p, "/untouched/") {
			syncMu.Unlock()
			t.Fatalf("bounded flush unexpectedly synced untouched path %s", p)
		}
	}
	syncMu.Unlock()

	for i := 10; i < 210; i++ {
		_ = os.WriteFile(filepath.Join(untouchedDir, fmt.Sprintf("file-%03d.txt", i)), []byte("extra\n"), 0o644)
	}
	rec2, rel2, err := adp.CheckpointBarrier(t.Context(), viewID)
	if err != nil || !rec2.DirtyDataFlushed {
		t.Fatalf("CheckpointBarrier 2 failed: rec=%+v err=%v", rec2, err)
	}
	_ = rel2()
	if files2, dirs2 := adp.LastFlushStatsForView(viewID); files2 != files1 || dirs2 != dirs1 {
		t.Fatalf("expected bounded flush counts to stay constant (10->210): first=(%d,%d) second=(%d,%d)", files1, dirs1, files2, dirs2)
	}
}

func TestCheckpointBarrierCancellationDuringHeldInFlightFUSEMutation(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("real FUSE mount requires linux")
	}
	enteredCh, unblockCh := make(chan struct{}), make(chan struct{})
	var hookOnce sync.Once
	adp := fuselinux.New(fuselinux.Config{
		OnMutationHook: func(_ uint32, rel string) {
			if strings.HasSuffix(rel, "held.txt") || rel == "." {
				hookOnce.Do(func() {
					close(enteredCh)
					<-unblockCh
				})
			}
		},
	})
	tmp := t.TempDir()
	trunkDir, backingDir, runtimeDir := filepath.Join(tmp, "trunk"), filepath.Join(tmp, "backing"), filepath.Join(tmp, "runtime")
	for _, d := range []string{trunkDir, backingDir, runtimeDir} {
		_ = os.MkdirAll(d, 0o755)
	}
	_ = os.WriteFile(filepath.Join(trunkDir, "held.txt"), []byte("seed\n"), 0o644)

	const viewID = "view-cancel-inflight"
	prep, err := adp.Prepare(t.Context(), adapter.PrepareRequest{
		ViewID: viewID, BackingDir: backingDir, RuntimeDir: runtimeDir,
		QuiesceHook: fuselinux.NoopQuiesceHook, FlushClientHook: fuselinux.NoWritableMappingsFlushHook,
		Grants: []adapter.RootGrant{{RootID: "primary", Label: "primary", SourcePath: trunkDir, IsPrimary: true}},
	})
	if err != nil {
		t.Fatalf("Prepare: %v", err)
	}
	t.Cleanup(func() {
		select {
		case <-unblockCh:
		default:
			close(unblockCh)
		}
		_, _ = adp.Release(t.Context(), viewID)
	})

	writeDone := make(chan error, 1)
	go func() {
		writeDone <- os.WriteFile(filepath.Join(prep.PrimaryWorkPath, "held.txt"), []byte("mutated-after-hold\n"), 0o644)
	}()
	<-enteredCh

	// Cancel only once the barrier is waiting on the held mutation.
	srv := adp.ServerForView(viewID)
	cancelCtx, cancel := context.WithCancel(t.Context())
	defer cancel()
	go func() {
		for !srv.IsBarrierActive() {
			runtime.Gosched()
		}
		cancel()
	}()
	_, _, bErr := adp.CheckpointBarrier(cancelCtx, viewID)
	var aErr *adapter.AdapterError
	if bErr == nil || !errors.As(bErr, &aErr) || aErr.ReasonCode != adapter.ReasonContextCanceled {
		t.Fatalf("expected ReasonContextCanceled on canceled CheckpointBarrier, got %+v (err=%v)", aErr, bErr)
	}
	if srv.IsBarrierActive() {
		t.Fatalf("expected IsBarrierActive()=false after canceled CheckpointBarrier")
	}

	close(unblockCh)
	if wErr := <-writeDone; wErr != nil {
		t.Fatalf("held write failed after unblock: %v", wErr)
	}
	rec, release, err := adp.CheckpointBarrier(t.Context(), viewID)
	if err != nil || !rec.OperationsDrained || !rec.DirtyDataFlushed {
		t.Fatalf("expected subsequent CheckpointBarrier to succeed, got rec=%+v err=%v", rec, err)
	}
	_ = release()
}

func TestPublisherHoldsAdapterServerBarrierDuringCaptureAndPreservesPriorCheckpointOnFlushFailure(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("real FUSE mount requires linux")
	}
	adp := fuselinux.New(fuselinux.Config{})
	tmp := t.TempDir()
	trunkDir, backingDir, runtimeDir, cpDir := filepath.Join(tmp, "trunk"), filepath.Join(tmp, "backing"), filepath.Join(tmp, "runtime"), filepath.Join(tmp, "cp")
	for _, d := range []string{trunkDir, backingDir, runtimeDir, cpDir} {
		_ = os.MkdirAll(d, 0o755)
	}
	initialBytes := []byte("0123456789abcdef\n")
	_ = os.WriteFile(filepath.Join(trunkDir, "mapped.txt"), initialBytes, 0o644)

	const viewID = "view-pub-barrier"
	prep, err := adp.Prepare(t.Context(), adapter.PrepareRequest{
		ViewID: viewID, BackingDir: backingDir, RuntimeDir: runtimeDir,
		Grants: []adapter.RootGrant{{RootID: "primary", Label: "primary", SourcePath: trunkDir, IsPrimary: true}},
	})
	if err != nil {
		t.Fatalf("Prepare: %v", err)
	}
	t.Cleanup(func() {
		adp.SetConfigCallbacksForTest(nil, nil)
		_, _ = adp.Release(t.Context(), viewID)
	})

	mf, err := os.OpenFile(filepath.Join(prep.PrimaryWorkPath, "mapped.txt"), os.O_RDWR, 0o644)
	if err != nil {
		t.Fatalf("OpenFile: %v", err)
	}
	defer func() { _ = mf.Close() }()
	mappedSlice, err := syscall.Mmap(int(mf.Fd()), 0, len(initialBytes), syscall.PROT_READ|syscall.PROT_WRITE, syscall.MAP_SHARED)
	if err != nil {
		t.Fatalf("Mmap: %v", err)
	}
	defer func() { _ = syscall.Munmap(mappedSlice) }()
	wantCP1 := []byte("cp1-mmap-payload\n")
	copyToMapping(mappedSlice, wantCP1)

	_ = adp.SetBarrierHooks(viewID, fuselinux.NoopQuiesceHook, func(_ context.Context, _ string) error {
		if err := unix.Msync(mappedSlice, unix.MS_SYNC); err != nil {
			return err
		}
		return mf.Sync()
	})
	cpStore, err := checkpoint.NewStore(cpDir)
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	pub, err := checkpoint.NewPublisher(checkpoint.PublisherConfig{
		Core: prep.Core, Store: cpStore, WorkerID: "w-pub", TaskID: "t-pub", ProjectID: "p-pub", SourceGeneration: "g1",
		SelectedRoots: []layeredfs.RootSpec{{ID: "primary", Label: "primary", TrunkPath: trunkDir, IsPrimary: true}},
		Callbacks:     adp.NewBarrierProvider(viewID),
	})
	if err != nil {
		t.Fatalf("NewPublisher: %v", err)
	}

	srv := adp.ServerForView(viewID)
	var gateVerified bool
	gatedWrite := make(chan error, 1)
	pubRes1, err := pub.Publish(t.Context(), checkpoint.PublishRequest{
		CheckpointID: "cp-1", Trigger: checkpoint.TriggerExplicitCapture,
		FaultInjector: func(stage checkpoint.FaultStage) error {
			if stage == checkpoint.FaultAfterBarrier {
				if !srv.IsBarrierActive() {
					return errors.New("expected Server.IsBarrierActive()==true during FaultAfterBarrier")
				}
				go func() {
					gatedWrite <- os.WriteFile(filepath.Join(prep.PrimaryWorkPath, "blocked.txt"), []byte("blocked\n"), 0o644)
				}()
				awaitCondition(t, "mutation held at the barrier gate", func() bool { return srv.GatedMutationsForTest() > 0 })
				gateVerified = true
			}
			return nil
		},
	})
	if err != nil || !gateVerified || srv.IsBarrierActive() {
		t.Fatalf("Publish cp-1 failed: gateVerified=%v active=%v err=%v", gateVerified, srv.IsBarrierActive(), err)
	}
	// The held write completes for the worker once the barrier ends, outside the checkpoint.
	if wErr := <-gatedWrite; wErr != nil {
		t.Fatalf("write held by the barrier must succeed after it ends: %v", wErr)
	}
	verifyPublishedCP1AndFailedCP2(t, adp, pub, cpStore, srv, pubRes1, mappedSlice, wantCP1)
}

func verifyPublishedCP1AndFailedCP2(
	t *testing.T, adp *fuselinux.Adapter, pub *checkpoint.Publisher, cpStore *checkpoint.Store,
	srv *fuselinux.Server, pubRes1 checkpoint.PublishResult, mappedSlice, wantCP1 []byte,
) {
	t.Helper()
	var mappedSHA string
	for _, e := range pubRes1.Manifest.Entries {
		if e.Path.Rel == "mapped.txt" {
			mappedSHA = e.Final.SHA256
		}
		if e.Path.Rel == "blocked.txt" {
			t.Fatalf("blocked.txt must not appear in cp-1 manifest")
		}
	}
	blobBytes, err := os.ReadFile(cpStore.BlobPath(mappedSHA))
	if err != nil || !bytes.Equal(blobBytes, wantCP1) {
		t.Fatalf("expected cp-1 blob %q, got %q err=%v", string(wantCP1), string(blobBytes), err)
	}
	copyToMapping(mappedSlice, []byte("cp2-unflushed!!!\n"))
	adp.SetConfigCallbacksForTest(func(p string) error {
		return errors.New("injected flush failure on " + p)
	}, nil)
	if _, err := pub.Publish(t.Context(), checkpoint.PublishRequest{CheckpointID: "cp-2", Trigger: checkpoint.TriggerExplicitCapture}); err == nil {
		t.Fatalf("expected Publish cp-2 to fail on injected flush failure")
	} else {
		var aErr *adapter.AdapterError
		if !errors.As(err, &aErr) || aErr.ReasonCode != adapter.ReasonBarrierFailed {
			t.Fatalf("expected ReasonBarrierFailed on Publish cp-2, got %v", err)
		}
	}
	if srv.IsBarrierActive() {
		t.Fatalf("expected Server.IsBarrierActive()==false after failed Publish cp-2")
	}
	if _, err := cpStore.LoadManifest("cp-2"); err == nil {
		t.Fatalf("expected cp-2 manifest not to be published on flush failure")
	}
	if m1, err := cpStore.LoadManifest("cp-1"); err != nil || m1.CheckpointID != "cp-1" {
		t.Fatalf("expected cp-1 manifest intact after cp-2 failure, got %+v err=%v", m1, err)
	}
}
