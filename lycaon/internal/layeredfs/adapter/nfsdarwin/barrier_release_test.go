package nfsdarwin_test

import (
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"io"
	"net"
	"os"
	"path/filepath"
	"runtime"
	"syscall"
	"testing"

	"github.com/lycaon/lycaon/internal/layeredfs"
	"github.com/lycaon/lycaon/internal/layeredfs/adapter"
	"github.com/lycaon/lycaon/internal/layeredfs/adapter/nfsdarwin"
	"github.com/lycaon/lycaon/internal/layeredfs/checkpoint"
	"github.com/lycaon/lycaon/internal/layeredfs/nfsserver"
	nfs "github.com/willscott/go-nfs"
)

func TestCheckpointBarrierDrainsPendingUnstableWritesAndExcludesNewMutations(t *testing.T) {
	if runtime.GOOS != "darwin" {
		t.Skip("nfsdarwin requires darwin")
	}

	adp := nfsdarwin.New(nfsdarwin.Config{})
	tmp := t.TempDir()
	trunkDir := filepath.Join(tmp, "trunk")
	backingDir := filepath.Join(tmp, "backing")
	runtimeDir := filepath.Join(tmp, "runtime")
	cpStoreDir := filepath.Join(tmp, "cp-store")
	for _, d := range []string{trunkDir, backingDir, runtimeDir, cpStoreDir} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatalf("mkdir %s: %v", d, err)
		}
	}
	if err := os.WriteFile(filepath.Join(trunkDir, "pending.txt"), []byte("original-trunk\n"), 0o644); err != nil {
		t.Fatalf("seed pending.txt: %v", err)
	}

	const viewID = "view-unstable-barrier"
	prep, err := adp.Prepare(t.Context(), adapter.PrepareRequest{
		ViewID:          viewID,
		BackingDir:      backingDir,
		RuntimeDir:      runtimeDir,
		QuiesceHook:     nfsdarwin.NoopQuiesceHook,
		FlushClientHook: nfsdarwin.NoWritableMappingsFlushHook,
		Grants:          []adapter.RootGrant{{RootID: "primary", Label: "primary", SourcePath: trunkDir, IsPrimary: true}},
	})
	if err != nil {
		t.Fatalf("Prepare: %v", err)
	}
	t.Cleanup(func() { _, _ = adp.Release(t.Context(), viewID) })

	srv := adp.ServerForView(viewID)
	facts, ok := adp.MountFactsForView(viewID)
	if !ok || srv == nil {
		t.Fatalf("missing server or facts for view %s", viewID)
	}

	// Stage a nonempty UNSTABLE write directly on the view's live NFSv3 server without sending COMMIT.
	rpcConn, rootFH := mountTestNFSConn(t, srv.Addr().String(), facts.ExportPath, uint32(os.Getuid()), uint32(os.Getgid()))
	defer func() { _ = rpcConn.Close() }()
	fileFH := lookupTestNFSFile(t, rpcConn, rootFH, "pending.txt", uint32(os.Getuid()), uint32(os.Getgid()))

	unstablePayload := []byte("staged-unstable-before-barrier\n")
	if st := sendTestNFSWrite(t, rpcConn, fileFH, 0, unstablePayload, uint32(nfsserver.Unstable), uint32(os.Getuid()), uint32(os.Getgid())); st != nfs.NFSStatusOk {
		t.Fatalf("expected NFSStatusOk staging UNSTABLE write, got %v", st)
	}
	if viewBytes, viewChunks, _ := nfsserver.PendingUnstableStats(srv, viewID); viewBytes != len(unstablePayload) || viewChunks != 1 {
		t.Fatalf("expected PendingUnstableStats=(%d, 1) before CheckpointBarrier, got (%d, %d)", len(unstablePayload), viewBytes, viewChunks)
	}

	// Execute CheckpointBarrier: must drain the pending UNSTABLE write to Core BEFORE Core freezes,
	// and hold the server mutation gate while Core's barrier is active.
	receipt, releaseBarrier, err := adp.CheckpointBarrier(t.Context(), viewID)
	if err != nil {
		t.Fatalf("CheckpointBarrier failed with pending UNSTABLE writes: %v", err)
	}
	if !receipt.WritersQuiesced || !receipt.OperationsDrained || !receipt.DirtyDataFlushed || !receipt.WritableMappingsFlushed {
		t.Fatalf("unexpected BarrierReceipt flags: %+v", receipt)
	}
	if viewBytes, viewChunks, _ := nfsserver.PendingUnstableStats(srv, viewID); viewBytes != 0 || viewChunks != 0 {
		t.Fatalf("expected PendingUnstableStats=(0, 0) after CheckpointBarrier drain, got (%d, %d)", viewBytes, viewChunks)
	}
	gotCoreBytes, err := prep.Core.ReadFile(layeredfs.QualifiedPath{RootID: "primary", Rel: "pending.txt"})
	if err != nil || !bytes.Equal(gotCoreBytes, unstablePayload) {
		t.Fatalf("expected Core to contain drained UNSTABLE payload %q, got %q err=%v", string(unstablePayload), string(gotCoreBytes), err)
	}

	// Verify that a concurrent mutating write RPC while the barrier is held is rejected with NFSStatusJukebox.
	if st := sendTestNFSWrite(t, rpcConn, fileFH, 0, []byte("excluded-during-barrier\n"), uint32(nfsserver.Unstable), uint32(os.Getuid()), uint32(os.Getgid())); st != nfs.NFSStatusJukebox {
		t.Fatalf("expected concurrent write during barrier to be rejected with NFSStatusJukebox, got %v", st)
	}
	if err := releaseBarrier(); err != nil {
		t.Fatalf("releaseBarrier: %v", err)
	}

	// Also verify Publisher.Publish captures pending UNSTABLE data through its callbacks.
	unstablePayload2 := []byte("staged-unstable-for-publisher!\n")
	if st := sendTestNFSWrite(t, rpcConn, fileFH, 0, unstablePayload2, uint32(nfsserver.Unstable), uint32(os.Getuid()), uint32(os.Getgid())); st != nfs.NFSStatusOk {
		t.Fatalf("expected NFSStatusOk staging second UNSTABLE write after barrier release, got %v", st)
	}
	cpStore, err := checkpoint.NewStore(cpStoreDir)
	if err != nil {
		t.Fatalf("checkpoint.NewStore: %v", err)
	}
	pub, err := checkpoint.NewPublisher(checkpoint.PublisherConfig{
		Core:             prep.Core,
		Store:            cpStore,
		WorkerID:         "worker-unstable",
		TaskID:           "task-unstable",
		ProjectID:        "proj-unstable",
		SourceGeneration: "gen-1",
		SelectedRoots:    []layeredfs.RootSpec{{ID: "primary", Label: "primary", TrunkPath: trunkDir, IsPrimary: true}},
		Callbacks: checkpoint.BarrierCallbacks{
			QuiesceWorkers: func(ctx context.Context) error {
				return adp.FlushClientData(ctx, viewID)
			},
			FlushClientDirtyData: func(ctx context.Context) error {
				return adp.FlushClientData(ctx, viewID)
			},
		},
	})
	if err != nil {
		t.Fatalf("checkpoint.NewPublisher: %v", err)
	}
	pubRes, err := pub.Publish(t.Context(), checkpoint.PublishRequest{
		CheckpointID: "cp-unstable-1",
		Trigger:      checkpoint.TriggerExplicitCapture,
	})
	if err != nil || pubRes.Manifest.CheckpointID != "cp-unstable-1" {
		t.Fatalf("Publish failed: %v", err)
	}
	gotCoreAfterPub, err := prep.Core.ReadFile(layeredfs.QualifiedPath{RootID: "primary", Rel: "pending.txt"})
	if err != nil || !bytes.Equal(gotCoreAfterPub, unstablePayload2) {
		t.Fatalf("expected Core to contain second drained UNSTABLE payload %q after Publish, got %q err=%v", string(unstablePayload2), string(gotCoreAfterPub), err)
	}
}

func TestCheckpointBarrierHookAndDirtyMmapFlushContract(t *testing.T) {
	if runtime.GOOS != "darwin" {
		t.Skip("nfsdarwin requires darwin")
	}

	adp := nfsdarwin.New(nfsdarwin.Config{})
	tmp := t.TempDir()
	trunkDir := filepath.Join(tmp, "trunk")
	backingDir := filepath.Join(tmp, "backing")
	runtimeDir := filepath.Join(tmp, "runtime")
	for _, d := range []string{trunkDir, backingDir, runtimeDir} {
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

	// 1. Missing QuiesceHook must fail with ReasonBarrierFailed and WritersQuiesced=false.
	rec1, _, err := adp.CheckpointBarrier(t.Context(), viewID)
	if err == nil || rec1.WritersQuiesced || rec1.WritableMappingsFlushed {
		t.Fatalf("expected CheckpointBarrier without QuiesceHook to fail with false flags, got rec=%+v err=%v", rec1, err)
	}

	// 2. QuiesceHook present but missing FlushClientHook must fail with WritersQuiesced=true, WritableMappingsFlushed=false.
	if err := adp.SetBarrierHooks(viewID, nfsdarwin.NoopQuiesceHook, nil); err != nil {
		t.Fatalf("SetBarrierHooks: %v", err)
	}
	rec2, _, err := adp.CheckpointBarrier(t.Context(), viewID)
	if err == nil || !rec2.WritersQuiesced || rec2.WritableMappingsFlushed {
		t.Fatalf("expected CheckpointBarrier without FlushClientHook to fail with WritersQuiesced=true WritableMappingsFlushed=false, got rec=%+v err=%v", rec2, err)
	}

	// 3. Open mapped.txt through the real /sbin/mount_nfs mount, mmap it MAP_SHARED, mutate in memory,
	// and flush via SyncMappedRegion in FlushClientHook during CheckpointBarrier.
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
	copy(mappedSlice, wantMapped)

	if err := adp.SetBarrierHooks(viewID, nfsdarwin.NoopQuiesceHook, func(_ context.Context, _ string) error {
		if err := nfsdarwin.SyncMappedRegion(mappedSlice); err != nil {
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

	// 4. Injected client file Sync error must cause FlushClientData and CheckpointBarrier to fail closed.
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
	if runtime.GOOS != "darwin" {
		t.Skip("nfsdarwin requires darwin")
	}

	adp := nfsdarwin.New(nfsdarwin.Config{})
	tmp := t.TempDir()
	trunkDir := filepath.Join(tmp, "trunk")
	if err := os.MkdirAll(trunkDir, 0o755); err != nil {
		t.Fatalf("mkdir trunk: %v", err)
	}
	if err := os.WriteFile(filepath.Join(trunkDir, "file.txt"), []byte("seed\n"), 0o644); err != nil {
		t.Fatalf("seed file.txt: %v", err)
	}

	prep1, err := adp.Prepare(t.Context(), adapter.PrepareRequest{
		ViewID:     "view-retry-1",
		BackingDir: filepath.Join(tmp, "backing-1"),
		RuntimeDir: filepath.Join(tmp, "runtime-1"),
		Grants:     []adapter.RootGrant{{RootID: "primary", Label: "primary", SourcePath: trunkDir, IsPrimary: true}},
	})
	if err != nil {
		t.Fatalf("Prepare view-retry-1: %v", err)
	}
	t.Cleanup(func() {
		adp.SetUmountBinForTest("", "")
		adp.SetConfigCallbacksForTest(nil, nil)
		_, _ = adp.Release(t.Context(), "view-retry-1")
	})

	prep2, err := adp.Prepare(t.Context(), adapter.PrepareRequest{
		ViewID:     "view-keep-2",
		BackingDir: filepath.Join(tmp, "backing-2"),
		RuntimeDir: filepath.Join(tmp, "runtime-2"),
		Grants:     []adapter.RootGrant{{RootID: "primary", Label: "primary", SourcePath: trunkDir, IsPrimary: true}},
	})
	if err != nil {
		t.Fatalf("Prepare view-keep-2: %v", err)
	}
	t.Cleanup(func() { _, _ = adp.Release(t.Context(), "view-keep-2") })

	// Attempt 1: inject unmount failure on view-retry-1.
	adp.SetUmountBinForTest("/usr/bin/false", "/usr/bin/false")
	rel1, err := adp.Release(t.Context(), "view-retry-1")
	if err == nil || rel1.Settled || rel1.ReasonCode != adapter.ReasonCleanupFailed || len(rel1.CleanedPaths) != 0 {
		t.Fatalf("expected first Release to fail unmount with Settled=false, got rel=%+v err=%v", rel1, err)
	}
	if adp.ServerForView("view-retry-1") == nil || adp.CoreForView("view-retry-1") == nil {
		t.Fatalf("expected view-retry-1 state and server retained after unmount failure")
	}
	if !adp.IsViewActive("view-keep-2") {
		t.Fatalf("expected unrelated view-keep-2 to remain active and healthy")
	}
	if got2, rErr := os.ReadFile(filepath.Join(prep2.PrimaryWorkPath, "file.txt")); rErr != nil || string(got2) != "seed\n" {
		t.Fatalf("expected view-keep-2 mount readable, got %q err=%v", string(got2), rErr)
	}

	// Attempt 2: restore real unmount binaries, but inject RuntimeViewDir removal failure.
	adp.SetUmountBinForTest("", "")
	adp.SetConfigCallbacksForTest(nil, func(path string) error {
		return errors.New("injected RemoveAll failure on " + path)
	})
	rel2, err := adp.Release(t.Context(), "view-retry-1")
	if err == nil || rel2.Settled || rel2.ReasonCode != adapter.ReasonCleanupFailed || len(rel2.CleanedPaths) != 0 {
		t.Fatalf("expected second Release to fail RuntimeViewDir cleanup with Settled=false and empty CleanedPaths, got rel=%+v err=%v", rel2, err)
	}
	if adp.CoreForView("view-retry-1") == nil {
		t.Fatalf("expected view-retry-1 entry retained in adapter until RuntimeViewDir removal succeeds")
	}

	// Attempt 3: clear RuntimeViewDir removal failure and retry Release -> must settle cleanly.
	adp.SetConfigCallbacksForTest(nil, nil)
	rel3, err := adp.Release(t.Context(), "view-retry-1")
	if err != nil || !rel3.Settled || rel3.ReasonCode != adapter.ReasonNone || len(rel3.CleanedPaths) != 1 || rel3.CleanedPaths[0] != prep1.RuntimeViewDir {
		t.Fatalf("expected third Release to settle cleanly, got rel=%+v err=%v", rel3, err)
	}
	if _, statErr := os.Stat(prep1.RuntimeViewDir); !errors.Is(statErr, os.ErrNotExist) {
		t.Fatalf("expected RuntimeViewDir %s removed after settled Release, got err=%v", prep1.RuntimeViewDir, statErr)
	}
	if !adp.IsViewActive("view-keep-2") {
		t.Fatalf("expected unrelated view-keep-2 still active after view-retry-1 settled")
	}
}

func mountTestNFSConn(t *testing.T, addr, exportPath string, uid, gid uint32) (net.Conn, []byte) {
	t.Helper()
	conn, err := net.Dial("tcp", addr)
	if err != nil {
		t.Fatalf("dial %s: %v", addr, err)
	}
	var args bytes.Buffer
	writeTestXDRString(&args, exportPath)
	reply := sendTestRPCCall(t, conn, 1, 100005, 3, uint32(nfs.MountProcMount), uid, gid, args.Bytes())
	r := bytes.NewReader(reply)
	status := readTestUint32(t, r)
	if status != uint32(nfs.MountStatusOk) {
		t.Fatalf("MOUNTPROC3_MNT status=%d", status)
	}
	fhLen := readTestUint32(t, r)
	fh := make([]byte, fhLen)
	if _, err := io.ReadFull(r, fh); err != nil {
		t.Fatalf("read root FH: %v", err)
	}
	return conn, fh
}

func lookupTestNFSFile(t *testing.T, conn net.Conn, dirFH []byte, name string, uid, gid uint32) []byte {
	t.Helper()
	var args bytes.Buffer
	writeTestXDROpaque(&args, dirFH)
	writeTestXDRString(&args, name)
	reply := sendTestRPCCall(t, conn, 2, 100003, 3, uint32(nfs.NFSProcedureLookup), uid, gid, args.Bytes())
	r := bytes.NewReader(reply)
	status := readTestUint32(t, r)
	if status != uint32(nfs.NFSStatusOk) {
		t.Fatalf("LOOKUP(%s) status=%d", name, status)
	}
	fhLen := readTestUint32(t, r)
	fh := make([]byte, fhLen)
	if _, err := io.ReadFull(r, fh); err != nil {
		t.Fatalf("read file FH: %v", err)
	}
	return fh
}

func sendTestNFSWrite(t *testing.T, conn net.Conn, fileFH []byte, offset uint64, data []byte, stableHow, uid, gid uint32) nfs.NFSStatus {
	t.Helper()
	var args bytes.Buffer
	writeTestXDROpaque(&args, fileFH)
	_ = binary.Write(&args, binary.BigEndian, offset)
	_ = binary.Write(&args, binary.BigEndian, uint32(len(data)))
	_ = binary.Write(&args, binary.BigEndian, stableHow)
	writeTestXDROpaque(&args, data)
	reply := sendTestRPCCall(t, conn, 3, 100003, 3, uint32(nfs.NFSProcedureWrite), uid, gid, args.Bytes())
	r := bytes.NewReader(reply)
	return nfs.NFSStatus(readTestUint32(t, r))
}

func sendTestRPCCall(t *testing.T, conn net.Conn, xid, prog, vers, proc, uid, gid uint32, args []byte) []byte {
	t.Helper()
	var cred bytes.Buffer
	_ = binary.Write(&cred, binary.BigEndian, uint32(1))
	writeTestXDRString(&cred, "localhost")
	_ = binary.Write(&cred, binary.BigEndian, uid)
	_ = binary.Write(&cred, binary.BigEndian, gid)
	_ = binary.Write(&cred, binary.BigEndian, uint32(0))

	var msg bytes.Buffer
	for _, v := range []uint32{xid, 0, 2, prog, vers, proc, uint32(nfs.AuthFlavorUnix)} {
		_ = binary.Write(&msg, binary.BigEndian, v)
	}
	writeTestXDROpaque(&msg, cred.Bytes())
	_ = binary.Write(&msg, binary.BigEndian, uint32(nfs.AuthFlavorNull))
	_ = binary.Write(&msg, binary.BigEndian, uint32(0))
	_, _ = msg.Write(args)

	var frame bytes.Buffer
	_ = binary.Write(&frame, binary.BigEndian, uint32(msg.Len())|0x80000000)
	_, _ = frame.Write(msg.Bytes())
	if _, err := conn.Write(frame.Bytes()); err != nil {
		t.Fatalf("write RPC call: %v", err)
	}
	var hdrBuf [4]byte
	if _, err := io.ReadFull(conn, hdrBuf[:]); err != nil {
		t.Fatalf("read RPC reply header: %v", err)
	}
	length := binary.BigEndian.Uint32(hdrBuf[:]) & 0x7fffffff
	payload := make([]byte, length)
	if _, err := io.ReadFull(conn, payload); err != nil {
		t.Fatalf("read RPC reply body: %v", err)
	}
	return payload[24:]
}

func writeTestXDROpaque(w *bytes.Buffer, b []byte) {
	_ = binary.Write(w, binary.BigEndian, uint32(len(b)))
	_, _ = w.Write(b)
	if pad := (4 - (len(b) & 3)) & 3; pad > 0 {
		var z [4]byte
		_, _ = w.Write(z[:pad])
	}
}

func writeTestXDRString(w *bytes.Buffer, s string) { writeTestXDROpaque(w, []byte(s)) }

func readTestUint32(t *testing.T, r io.Reader) uint32 {
	t.Helper()
	var v uint32
	if err := binary.Read(r, binary.BigEndian, &v); err != nil {
		t.Fatalf("read uint32: %v", err)
	}
	return v
}
