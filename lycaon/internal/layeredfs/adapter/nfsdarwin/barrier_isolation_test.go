package nfsdarwin_test

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"syscall"
	"testing"
	"time"

	"github.com/lycaon/lycaon/internal/layeredfs"
	"github.com/lycaon/lycaon/internal/layeredfs/adapter"
	"github.com/lycaon/lycaon/internal/layeredfs/adapter/nfsdarwin"
	"github.com/lycaon/lycaon/internal/layeredfs/checkpoint"
	"github.com/lycaon/lycaon/internal/layeredfs/nfsserver"
	nfs "github.com/willscott/go-nfs"
)

func TestBoundedClientFlushSkipsUntouchedUnreadableAndSpecialFiles(t *testing.T) {
	if runtime.GOOS != "darwin" {
		t.Skip("nfsdarwin requires darwin")
	}

	tmp := t.TempDir()
	trunkDir := filepath.Join(tmp, "trunk")
	if err := os.MkdirAll(trunkDir, 0o755); err != nil {
		t.Fatalf("mkdir trunk: %v", err)
	}
	unreadablePath := filepath.Join(trunkDir, "unreadable-0000.txt")
	if err := os.WriteFile(unreadablePath, []byte("secret-unreadable\n"), 0o600); err != nil {
		t.Fatalf("write unreadable: %v", err)
	}
	if err := os.Chmod(unreadablePath, 0o000); err != nil {
		t.Fatalf("chmod 0000: %v", err)
	}
	t.Cleanup(func() { _ = os.Chmod(unreadablePath, 0o600) })

	fifoPath := filepath.Join(trunkDir, "untouched.fifo")
	if err := syscall.Mkfifo(fifoPath, 0o600); err != nil {
		t.Fatalf("mkfifo: %v", err)
	}
	for i := range 10 {
		p := filepath.Join(trunkDir, fmt.Sprintf("untouched-%03d.txt", i))
		if err := os.WriteFile(p, []byte("untouched\n"), 0o644); err != nil {
			t.Fatalf("write %s: %v", p, err)
		}
	}
	if err := os.WriteFile(filepath.Join(trunkDir, "work.txt"), []byte("initial-work\n"), 0o644); err != nil {
		t.Fatalf("write work.txt: %v", err)
	}
	if err := os.WriteFile(filepath.Join(trunkDir, "mapped.txt"), []byte("0123456789abcdef\n"), 0o644); err != nil {
		t.Fatalf("write mapped.txt: %v", err)
	}

	filesSmall, dirsSmall := runBoundedFlushPublishPass(t, tmp, trunkDir, "small")

	for i := 10; i < 210; i++ {
		p := filepath.Join(trunkDir, fmt.Sprintf("untouched-%03d.txt", i))
		if err := os.WriteFile(p, []byte("untouched-extra\n"), 0o644); err != nil {
			t.Fatalf("write %s: %v", p, err)
		}
	}
	filesLarge, dirsLarge := runBoundedFlushPublishPass(t, tmp, trunkDir, "large")

	if filesSmall != filesLarge || dirsSmall != dirsLarge {
		t.Fatalf("expected identical bounded sync counts across 10 vs 210 untouched trunk files, got small=(%d,%d) large=(%d,%d)", filesSmall, dirsSmall, filesLarge, dirsLarge)
	}
	if filesLarge != 2 || dirsLarge != 1 {
		t.Fatalf("expected exactly 2 modified files and 1 mount root dir synced, got files=%d dirs=%d", filesLarge, dirsLarge)
	}
}

func runBoundedFlushPublishPass(t *testing.T, tmp, trunkDir, label string) (filesSynced, dirsSynced int) {
	t.Helper()
	adp := nfsdarwin.New(nfsdarwin.Config{})
	viewID := "view-bounded-" + label
	prep, err := adp.Prepare(t.Context(), adapter.PrepareRequest{
		ViewID: viewID, BackingDir: filepath.Join(tmp, "backing-"+label), RuntimeDir: filepath.Join(tmp, "runtime-"+label),
		QuiesceHook: nfsdarwin.NoopQuiesceHook, FlushClientHook: nfsdarwin.NoWritableMappingsFlushHook,
		Grants: []adapter.RootGrant{{RootID: "primary", Label: "primary", SourcePath: trunkDir, IsPrimary: true}},
	})
	if err != nil {
		t.Fatalf("Prepare %s: %v", viewID, err)
	}
	defer func() { _, _ = adp.Release(t.Context(), viewID) }()

	wantWork := []byte("native-write-" + label + "\n")
	if err := os.WriteFile(filepath.Join(prep.PrimaryWorkPath, "work.txt"), wantWork, 0o644); err != nil {
		t.Fatalf("WriteFile work.txt: %v", err)
	}
	mf, err := os.OpenFile(filepath.Join(prep.PrimaryWorkPath, "mapped.txt"), os.O_RDWR, 0o644)
	if err != nil {
		t.Fatalf("OpenFile mapped.txt: %v", err)
	}
	defer func() { _ = mf.Close() }()
	_, _ = mf.ReadAt(make([]byte, 17), 0)
	mappedSlice, err := syscall.Mmap(int(mf.Fd()), 0, 17, syscall.PROT_READ|syscall.PROT_WRITE, syscall.MAP_SHARED)
	if err != nil {
		t.Fatalf("Mmap: %v", err)
	}
	defer func() { _ = syscall.Munmap(mappedSlice) }()
	wantMapped := []byte("mmap-bound-" + label[:5] + "\n")
	copy(mappedSlice, wantMapped)

	if err := adp.SetBarrierHooks(viewID, nfsdarwin.NoopQuiesceHook, func(_ context.Context, _ string) error {
		if err := nfsdarwin.SyncMappedRegion(mappedSlice); err != nil {
			return err
		}
		return mf.Sync()
	}); err != nil {
		t.Fatalf("SetBarrierHooks: %v", err)
	}

	cpStore, err := checkpoint.NewStore(filepath.Join(tmp, "cp-store-"+label))
	if err != nil {
		t.Fatalf("checkpoint.NewStore: %v", err)
	}
	pub, err := checkpoint.NewPublisher(checkpoint.PublisherConfig{
		Core: prep.Core, Store: cpStore, WorkerID: "worker-" + label, TaskID: "task-" + label,
		ProjectID: "proj-" + label, SourceGeneration: "gen-1",
		SelectedRoots: []layeredfs.RootSpec{{ID: "primary", Label: "primary", TrunkPath: trunkDir, IsPrimary: true}},
		Callbacks:     adp.NewBarrierProvider(viewID),
	})
	if err != nil {
		t.Fatalf("checkpoint.NewPublisher: %v", err)
	}
	cpID := "cp-" + label
	if _, err := pub.Publish(t.Context(), checkpoint.PublishRequest{CheckpointID: cpID, Trigger: checkpoint.TriggerExplicitCapture}); err != nil {
		t.Fatalf("Publish %s: %v", cpID, err)
	}
	if got := readCheckpointEntryBlob(t, cpStore, cpID, "work.txt"); !bytes.Equal(got, wantWork) {
		t.Fatalf("work.txt blob mismatch: got %q want %q", string(got), string(wantWork))
	}
	if got := readCheckpointEntryBlob(t, cpStore, cpID, "mapped.txt"); !bytes.Equal(got, wantMapped) {
		t.Fatalf("mapped.txt blob mismatch: got %q want %q", string(got), string(wantMapped))
	}
	return adp.LastFlushStatsForView(viewID)
}

func TestBarrierDrainCancellationAndPerViewIsolationAndGateRetention(t *testing.T) {
	if runtime.GOOS != "darwin" { t.Skip("nfsdarwin requires darwin") }
	tmp := t.TempDir()
	trunkDir := filepath.Join(tmp, "trunk")
	if err := os.MkdirAll(trunkDir, 0o755); err != nil { t.Fatalf("mkdir trunk: %v", err) }
	if err := os.WriteFile(filepath.Join(trunkDir, "shared.txt"), []byte("seed\n"), 0o644); err != nil { t.Fatalf("write shared.txt: %v", err) }

	writeEntered := make(chan string, 4)
	releaseHeldWrite := make(chan struct{})
	fi := &nfsserver.FaultInjector{
		OnWrite: func(viewID string, _ layeredfs.QualifiedPath, _ uint64, _ int, _ nfsserver.WriteStability) error {
			writeEntered <- viewID
			<-releaseHeldWrite
			return nil
		},
	}

	adp := nfsdarwin.New(nfsdarwin.Config{FaultInjector: fi})
	const viewA = "view-cancel-A"
	_, err := adp.Prepare(t.Context(), adapter.PrepareRequest{
		ViewID: viewA, BackingDir: filepath.Join(tmp, "backing-A"), RuntimeDir: filepath.Join(tmp, "runtime-A"),
		QuiesceHook: nfsdarwin.NoopQuiesceHook, FlushClientHook: nfsdarwin.NoWritableMappingsFlushHook,
		Grants: []adapter.RootGrant{{RootID: "primary", Label: "primary", SourcePath: trunkDir, IsPrimary: true}},
	})
	if err != nil { t.Fatalf("Prepare viewA: %v", err) }
	t.Cleanup(func() { _, _ = adp.Release(t.Context(), viewA) })

	srv := adp.ServerForView(viewA)
	factsA, _ := adp.MountFactsForView(viewA)
	uid, gid := uint32(os.Getuid()), uint32(os.Getgid())

	verifySharedServerPerViewIsolation(t, tmp, trunkDir, adp, srv, viewA, uid, gid, writeEntered, releaseHeldWrite)

	releaseHeldWrite = make(chan struct{})
	connA, rootFHA := mountTestNFSConn(t, srv.Addr().String(), factsA.ExportPath, uid, gid)
	defer func() { _ = connA.Close() }()
	fileFHA := lookupTestNFSFile(t, connA, rootFHA, "shared.txt", uid, gid)

	writeDoneA := make(chan nfs.NFSStatus, 1)
	go func() {
		writeDoneA <- sendTestNFSWrite(t, connA, fileFHA, 0, []byte("held-on-A\n"), uint32(nfsserver.Unstable), uid, gid)
	}()
	if gotView := <-writeEntered; gotView != viewA {
		t.Fatalf("expected held write on %s, got %s", viewA, gotView)
	}

	cancelCtx, cancel := context.WithTimeout(t.Context(), 40*time.Millisecond)
	defer cancel()
	_, _, cancelErr := adp.CheckpointBarrier(cancelCtx, viewA)
	var aErr *adapter.AdapterError
	if !errors.As(cancelErr, &aErr) || aErr.ReasonCode != adapter.ReasonContextCanceled {
		t.Fatalf("expected CheckpointBarrier cancellation to return ReasonContextCanceled, got %v", cancelErr)
	}

	close(releaseHeldWrite)
	if st := <-writeDoneA; st != nfs.NFSStatusOk {
		t.Fatalf("expected held write on viewA to complete after cancel, got %v", st)
	}
	if gated, inFlight, _, _ := srv.GateStateForTest(); gated != 0 || inFlight != 0 {
		t.Fatalf("expected 0 gated/inFlight views after canceled barrier + write completion, got gated=%d inFlight=%d", gated, inFlight)
	}

	recRetry, relRetry, err := adp.CheckpointBarrier(t.Context(), viewA)
	if err != nil || !recRetry.OperationsDrained {
		t.Fatalf("expected retry CheckpointBarrier on viewA to succeed, got %v", err)
	}
	_ = relRetry()
	if _, err := adp.Release(t.Context(), viewA); err != nil {
		t.Fatalf("Release viewA: %v", err)
	}
	if gated, inFlight, dirty, handshakes := srv.GateStateForTest(); gated != 0 || inFlight != 0 || dirty != 0 || handshakes != 0 {
		t.Fatalf("expected 0 retained gate state after server close, got (%d, %d, %d, %d)", gated, inFlight, dirty, handshakes)
	}
}

func verifySharedServerPerViewIsolation(
	t *testing.T, tmp, trunkDir string, adp *nfsdarwin.Adapter, srv *nfsserver.Server,
	viewA string, uid, gid uint32, writeEntered <-chan string, releaseHeldWrite chan struct{},
) {
	t.Helper()
	const viewB = "view-shared-B"
	coreB, err := layeredfs.New(layeredfs.Config{
		ViewID: viewB, BackingDir: filepath.Join(tmp, "backing-B"),
		Roots: []layeredfs.RootSpec{{ID: "primary", Label: "primary", TrunkPath: trunkDir, IsPrimary: true}},
	})
	if err != nil {
		t.Fatalf("layeredfs.New coreB: %v", err)
	}
	t.Cleanup(func() { _ = coreB.ReleaseView() })
	nfsserver.BindViewCore(srv, viewB, coreB, false)
	capB, err := srv.RegisterExportWithUID(viewB, "primary", "tok-view-b", uid, gid)
	if err != nil {
		t.Fatalf("RegisterExportWithUID viewB: %v", err)
	}

	connB, rootFHB := mountTestNFSConn(t, srv.Addr().String(), capB.ExportPath, uid, gid)
	defer func() { _ = connB.Close() }()
	fileFHB := lookupTestNFSFile(t, connB, rootFHB, "shared.txt", uid, gid)

	writeDoneB := make(chan nfs.NFSStatus, 1)
	go func() {
		writeDoneB <- sendTestNFSWrite(t, connB, fileFHB, 0, []byte("held-on-B\n"), uint32(nfsserver.Unstable), uid, gid)
	}()
	if gotView := <-writeEntered; gotView != viewB {
		t.Fatalf("expected held write on %s, got %s", viewB, gotView)
	}

	recA, relA, err := adp.CheckpointBarrier(t.Context(), viewA)
	if err != nil || !recA.OperationsDrained {
		t.Fatalf("expected CheckpointBarrier on viewA to succeed while viewB write is in flight, got rec=%+v err=%v", recA, err)
	}
	_ = relA()
	close(releaseHeldWrite)
	if st := <-writeDoneB; st != nfs.NFSStatusOk {
		t.Fatalf("expected held write on viewB to finish with NFSStatusOk, got %v", st)
	}
	srv.ReleaseExport(viewB)
}
