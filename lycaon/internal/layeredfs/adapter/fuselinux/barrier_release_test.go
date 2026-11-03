package fuselinux_test

import (
	"bytes"
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"syscall"
	"testing"

	"github.com/lycaon/lycaon/internal/layeredfs"
	"github.com/lycaon/lycaon/internal/layeredfs/adapter"
	"github.com/lycaon/lycaon/internal/layeredfs/adapter/fuselinux"
	"github.com/lycaon/lycaon/internal/layeredfs/checkpoint"
)

func TestCheckpointBarrierHookAndDirtyMmapFlushContract(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("real FUSE mount requires linux")
	}

	adp := fuselinux.New(fuselinux.Config{})
	tmp := t.TempDir()
	trunkDir, backingDir, runtimeDir, cpStoreDir := filepath.Join(tmp, "trunk"), filepath.Join(tmp, "backing"), filepath.Join(tmp, "runtime"), filepath.Join(tmp, "cp")
	for _, d := range []string{trunkDir, backingDir, runtimeDir, cpStoreDir} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatalf("mkdir %s: %v", d, err)
		}
	}
	initialBytes := []byte("0123456789abcdef\n")
	if err := os.WriteFile(filepath.Join(trunkDir, "mapped.txt"), initialBytes, 0o644); err != nil {
		t.Fatalf("seed mapped.txt: %v", err)
	}

	const viewID = "view-mmap-contract"
	prep, err := adp.Prepare(t.Context(), adapter.PrepareRequest{
		ViewID:     viewID,
		BackingDir: backingDir,
		RuntimeDir: runtimeDir,
		Grants:     []adapter.RootGrant{{RootID: "primary", Label: "primary", SourcePath: trunkDir, IsPrimary: true}},
	})
	if err != nil {
		t.Fatalf("Prepare: %v", err)
	}
	t.Cleanup(func() { _, _ = adp.Release(t.Context(), viewID) })

	// 1. Missing QuiesceHook must fail with WritersQuiesced=false.
	rec1, _, err := adp.CheckpointBarrier(t.Context(), viewID)
	if err == nil || rec1.WritersQuiesced || rec1.WritableMappingsFlushed {
		t.Fatalf("expected CheckpointBarrier without QuiesceHook to fail, got rec=%+v err=%v", rec1, err)
	}

	// 2. QuiesceHook present but missing FlushClientHook must fail with WritersQuiesced=true, WritableMappingsFlushed=false.
	if err := adp.SetBarrierHooks(viewID, fuselinux.NoopQuiesceHook, nil); err != nil {
		t.Fatalf("SetBarrierHooks: %v", err)
	}
	rec2, _, err := adp.CheckpointBarrier(t.Context(), viewID)
	if err == nil || !rec2.WritersQuiesced || rec2.WritableMappingsFlushed {
		t.Fatalf("expected CheckpointBarrier without FlushClientHook to fail, got rec=%+v err=%v", rec2, err)
	}

	// 3. Open mapped.txt on the FUSE mount, mmap MAP_SHARED, mutate in memory, and flush via SyncMappedRegion.
	mappedPath := filepath.Join(prep.PrimaryWorkPath, "mapped.txt")
	mf, err := os.OpenFile(mappedPath, os.O_RDWR, 0o644)
	if err != nil {
		t.Fatalf("OpenFile mapped.txt: %v", err)
	}
	defer func() { _ = mf.Close() }()

	mappedSlice, err := syscall.Mmap(int(mf.Fd()), 0, len(initialBytes), syscall.PROT_READ|syscall.PROT_WRITE, syscall.MAP_SHARED)
	if err != nil {
		t.Fatalf("syscall.Mmap: %v", err)
	}
	defer func() { _ = syscall.Munmap(mappedSlice) }()

	wantMapped := []byte("mutated-by-mmap!\n")
	copyToMapping(mappedSlice, wantMapped)

	if err := adp.SetBarrierHooks(viewID, fuselinux.NoopQuiesceHook, func(_ context.Context, _ string) error {
		if err := fuselinux.SyncMappedRegion(mappedSlice); err != nil {
			return err
		}
		return mf.Sync()
	}); err != nil {
		t.Fatalf("SetBarrierHooks with mmap sync: %v", err)
	}

	rec3, releaseBarrier, err := adp.CheckpointBarrier(t.Context(), viewID)
	if err != nil || !rec3.WritersQuiesced || !rec3.WritableMappingsFlushed || !rec3.DirtyDataFlushed {
		t.Fatalf("CheckpointBarrier with real mmap flush failed: rec=%+v err=%v", rec3, err)
	}
	if err := releaseBarrier(); err != nil {
		t.Fatalf("releaseBarrier: %v", err)
	}

	gotCore, err := prep.Core.ReadFile(layeredfs.QualifiedPath{RootID: "primary", Rel: "mapped.txt"})
	if err != nil || !bytes.Equal(gotCore, wantMapped) {
		t.Fatalf("expected Core to contain flushed mmap bytes %q, got %q err=%v", string(wantMapped), string(gotCore), err)
	}
	gotTrunk, err := os.ReadFile(filepath.Join(trunkDir, "mapped.txt"))
	if err != nil || !bytes.Equal(gotTrunk, initialBytes) {
		t.Fatalf("expected trunk mapped.txt untouched (%q), got %q err=%v", string(initialBytes), string(gotTrunk), err)
	}

	// Publish checkpoint and verify manifest captures the MAP_SHARED mutation.
	cpStore, err := checkpoint.NewStore(cpStoreDir)
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	pub, err := checkpoint.NewPublisher(checkpoint.PublisherConfig{
		Core: prep.Core, Store: cpStore, WorkerID: "w-mmap", TaskID: "t-mmap", ProjectID: "p-mmap", SourceGeneration: "g1",
		SelectedRoots: []layeredfs.RootSpec{{ID: "primary", Label: "primary", TrunkPath: trunkDir, IsPrimary: true}},
		Callbacks: checkpoint.BarrierCallbacks{
			FlushClientDirtyData: func(ctx context.Context) error { return adp.FlushClientData(ctx, viewID) },
		},
	})
	if err != nil {
		t.Fatalf("NewPublisher: %v", err)
	}
	if _, err := pub.Publish(t.Context(), checkpoint.PublishRequest{CheckpointID: "cp-mmap-1", Trigger: checkpoint.TriggerExplicitCapture}); err != nil {
		t.Fatalf("Publish cp-mmap-1: %v", err)
	}

	// 4. Injected client file Sync error must cause CheckpointBarrier to fail closed.
	adp.SetConfigCallbacksForTest(func(p string) error {
		return errors.New("injected client sync failure on " + p)
	}, nil)
	defer adp.SetConfigCallbacksForTest(nil, nil)

	rec4, _, err := adp.CheckpointBarrier(t.Context(), viewID)
	if err == nil || rec4.DirtyDataFlushed {
		t.Fatalf("expected CheckpointBarrier to fail with DirtyDataFlushed=false on client sync error, got rec=%+v err=%v", rec4, err)
	}
}

func TestReleaseRetainsStateOnUnmountAndRuntimeCleanupFailureAndSucceedsOnRetry(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("real FUSE mount requires linux")
	}

	adp := fuselinux.New(fuselinux.Config{})
	tmp := t.TempDir()
	trunkDir, backingDir, runtimeDir := filepath.Join(tmp, "trunk"), filepath.Join(tmp, "backing"), filepath.Join(tmp, "runtime")
	for _, d := range []string{trunkDir, backingDir, runtimeDir} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatalf("mkdir %s: %v", d, err)
		}
	}
	if err := os.WriteFile(filepath.Join(trunkDir, "retry.txt"), []byte("seed\n"), 0o644); err != nil {
		t.Fatalf("seed retry.txt: %v", err)
	}

	const viewID = "view-release-retry"
	prep, err := adp.Prepare(t.Context(), adapter.PrepareRequest{
		ViewID: viewID, BackingDir: backingDir, RuntimeDir: runtimeDir,
		QuiesceHook: fuselinux.NoopQuiesceHook, FlushClientHook: fuselinux.NoWritableMappingsFlushHook,
		Grants: []adapter.RootGrant{{RootID: "primary", Label: "primary", SourcePath: trunkDir, IsPrimary: true}},
	})
	if err != nil {
		t.Fatalf("Prepare: %v", err)
	}
	t.Cleanup(func() {
		adp.SetFusermountBinForTest("")
		adp.SetConfigCallbacksForTest(nil, nil)
		_, _ = adp.Release(t.Context(), viewID)
	})

	if err := os.WriteFile(filepath.Join(prep.PrimaryWorkPath, "retry.txt"), []byte("dirty\n"), 0o644); err != nil {
		t.Fatalf("write retry.txt: %v", err)
	}

	// 1. Inject unmount failure by pointing FusermountBin at /bin/false; Release must fail and retain view state.
	adp.SetFusermountBinForTest("/bin/false")
	rec1, err := adp.Release(t.Context(), viewID)
	if err == nil || rec1.Settled {
		t.Fatalf("expected Release to fail when unmount fails, got rec=%+v err=%v", rec1, err)
	}
	if adp.ServerForView(viewID) == nil || adp.CoreForView(viewID) == nil {
		t.Fatalf("expected view %s state and server to remain tracked when unmount fails", viewID)
	}

	// 2. Restore FusermountBin, inject RemoveAllFn failure; Release must unmount but retain view state for runtime cleanup retry.
	adp.SetFusermountBinForTest("")
	adp.SetConfigCallbacksForTest(nil, func(p string) error {
		return errors.New("injected runtime dir remove failure on " + p)
	})
	rec2, err := adp.Release(t.Context(), viewID)
	if err == nil || rec2.Settled {
		t.Fatalf("expected Release to detach mount but fail runtime cleanup, got rec=%+v err=%v", rec2, err)
	}
	if adp.CoreForView(viewID) == nil {
		t.Fatalf("expected view %s to remain tracked when runtime cleanup fails", viewID)
	}

	// 3. Clear RemoveAllFn fault; retry Release must succeed and deregister view.
	adp.SetConfigCallbacksForTest(nil, nil)
	rec3, err := adp.Release(t.Context(), viewID)
	if err != nil || !rec3.Settled || len(rec3.CleanedPaths) == 0 {
		t.Fatalf("expected retry Release to succeed, got rec=%+v err=%v", rec3, err)
	}
	if adp.IsViewActive(viewID) {
		t.Fatalf("expected IsViewActive(%q)=false after successful Release", viewID)
	}
}

func TestReleaseDoesNotUnmountUnownedPath(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("real FUSE mount requires linux")
	}

	adp := fuselinux.New(fuselinux.Config{})
	tmp := t.TempDir()
	trunk1, backing1, runtime1 := filepath.Join(tmp, "t1"), filepath.Join(tmp, "b1"), filepath.Join(tmp, "r1")
	trunk2, backing2, runtime2 := filepath.Join(tmp, "t2"), filepath.Join(tmp, "b2"), filepath.Join(tmp, "r2")
	for _, d := range []string{trunk1, backing1, runtime1, trunk2, backing2, runtime2} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatalf("mkdir %s: %v", d, err)
		}
	}
	_ = os.WriteFile(filepath.Join(trunk1, "f.txt"), []byte("v1\n"), 0o644)
	_ = os.WriteFile(filepath.Join(trunk2, "f.txt"), []byte("v2\n"), 0o644)

	prep1, err := adp.Prepare(t.Context(), adapter.PrepareRequest{
		ViewID: "v-own-1", BackingDir: backing1, RuntimeDir: runtime1,
		Grants: []adapter.RootGrant{{RootID: "primary", Label: "primary", SourcePath: trunk1, IsPrimary: true}},
	})
	if err != nil {
		t.Fatalf("Prepare v-own-1: %v", err)
	}
	defer func() { _, _ = adp.Release(t.Context(), "v-own-1") }()

	prep2, err := adp.Prepare(t.Context(), adapter.PrepareRequest{
		ViewID: "v-own-2", BackingDir: backing2, RuntimeDir: runtime2,
		Grants: []adapter.RootGrant{{RootID: "primary", Label: "primary", SourcePath: trunk2, IsPrimary: true}},
	})
	if err != nil {
		t.Fatalf("Prepare v-own-2: %v", err)
	}
	defer func() { _, _ = adp.Release(t.Context(), "v-own-2") }()

	// Point v-own-1's recorded mountPath at v-own-2's live mount with mismatched FSName ownership;
	// releasing v-own-1 must NOT unmount v-own-2!
	realMount1 := prep1.PrimaryWorkPath
	if err := adp.DisownMountForTest("v-own-1", prep2.PrimaryWorkPath); err != nil {
		t.Fatalf("DisownMountForTest: %v", err)
	}
	_, _ = adp.Release(t.Context(), "v-own-1")
	_ = exec.Command("fusermount3", "-u", "-z", "--", realMount1).Run()

	if got, err := os.ReadFile(filepath.Join(prep2.PrimaryWorkPath, "f.txt")); err != nil || string(got) != "v2\n" {
		t.Fatalf("expected v-own-2 mount to remain intact after disowned v-own-1 release, got %q err=%v", string(got), err)
	}
}
