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
	if runtime.GOOS != "darwin" { t.Skip("nfsdarwin requires darwin") }
	adp := nfsdarwin.New(nfsdarwin.Config{})
	tmp := t.TempDir()
	trunkDir, backingDir, runtimeDir, cpStoreDir := filepath.Join(tmp, "trunk"), filepath.Join(tmp, "backing"), filepath.Join(tmp, "runtime"), filepath.Join(tmp, "cp-store")
	for _, d := range []string{trunkDir, backingDir, runtimeDir, cpStoreDir} {
		if err := os.MkdirAll(d, 0o755); err != nil { t.Fatalf("mkdir %s: %v", d, err) }
	}
	if err := os.WriteFile(filepath.Join(trunkDir, "pending.txt"), []byte("original-trunk\n"), 0o644); err != nil { t.Fatalf("seed pending.txt: %v", err) }

	const viewID = "view-unstable-barrier"
	prep, err := adp.Prepare(t.Context(), adapter.PrepareRequest{
		ViewID: viewID, BackingDir: backingDir, RuntimeDir: runtimeDir,
		QuiesceHook: nfsdarwin.NoopQuiesceHook, FlushClientHook: nfsdarwin.NoWritableMappingsFlushHook,
		Grants: []adapter.RootGrant{{RootID: "primary", Label: "primary", SourcePath: trunkDir, IsPrimary: true}},
	})
	if err != nil { t.Fatalf("Prepare: %v", err) }
	t.Cleanup(func() { _, _ = adp.Release(t.Context(), viewID) })

	srv := adp.ServerForView(viewID)
	facts, ok := adp.MountFactsForView(viewID)
	if !ok || srv == nil { t.Fatalf("missing server or facts for view %s", viewID) }

	uid, gid := uint32(os.Getuid()), uint32(os.Getgid())
	rpcConn, rootFH := mountTestNFSConn(t, srv.Addr().String(), facts.ExportPath, uid, gid)
	defer func() { _ = rpcConn.Close() }()
	fileFH := lookupTestNFSFile(t, rpcConn, rootFH, "pending.txt", uid, gid)

	unstablePayload := []byte("staged-unstable-before-barrier\n")
	if st := sendTestNFSWrite(t, rpcConn, fileFH, 0, unstablePayload, uint32(nfsserver.Unstable), uid, gid); st != nfs.NFSStatusOk {
		t.Fatalf("expected NFSStatusOk staging UNSTABLE write, got %v", st)
	}
	if viewBytes, viewChunks, _ := nfsserver.PendingUnstableStats(srv, viewID); viewBytes != len(unstablePayload) || viewChunks != 1 {
		t.Fatalf("expected PendingUnstableStats=(%d, 1) before publish, got (%d, %d)", len(unstablePayload), viewBytes, viewChunks)
	}

	cpStore, err := checkpoint.NewStore(cpStoreDir)
	if err != nil { t.Fatalf("checkpoint.NewStore: %v", err) }
	pub, err := checkpoint.NewPublisher(checkpoint.PublisherConfig{
		Core: prep.Core, Store: cpStore, WorkerID: "worker-unstable", TaskID: "task-unstable",
		ProjectID: "proj-unstable", SourceGeneration: "gen-1",
		SelectedRoots: []layeredfs.RootSpec{{ID: "primary", Label: "primary", TrunkPath: trunkDir, IsPrimary: true}},
		Callbacks:     adp.NewBarrierProvider(viewID),
	})
	if err != nil { t.Fatalf("checkpoint.NewPublisher: %v", err) }

	concurrentJukeboxChecked := false
	pubRes, err := pub.Publish(t.Context(), checkpoint.PublishRequest{
		CheckpointID: "cp-unstable-1", Trigger: checkpoint.TriggerExplicitCapture,
		FaultInjector: func(stage checkpoint.FaultStage) error {
			if stage == checkpoint.FaultAfterBarrier {
				if vb, vc, _ := nfsserver.PendingUnstableStats(srv, viewID); vb != 0 || vc != 0 {
					return errors.New("expected UNSTABLE writes drained before FaultAfterBarrier")
				}
				if st := sendTestNFSWrite(t, rpcConn, fileFH, 0, []byte("excluded-during-capture\n"), uint32(nfsserver.Unstable), uid, gid); st != nfs.NFSStatusJukebox {
					t.Errorf("expected concurrent write during Publish capture to receive NFSStatusJukebox, got %v", st)
				}
				concurrentJukeboxChecked = true
			}
			return nil
		},
	})
	if err != nil || pubRes.Manifest.CheckpointID != "cp-unstable-1" || !concurrentJukeboxChecked {
		t.Fatalf("Publish failed: err=%v checked=%v", err, concurrentJukeboxChecked)
	}
	if blobBytes := readCheckpointEntryBlob(t, cpStore, "cp-unstable-1", "pending.txt"); !bytes.Equal(blobBytes, unstablePayload) {
		t.Fatalf("expected durable checkpoint blob %q, got %q", string(unstablePayload), string(blobBytes))
	}
}

func TestCheckpointBarrierHookAndDirtyMmapFlushContract(t *testing.T) {
	if runtime.GOOS != "darwin" { t.Skip("nfsdarwin requires darwin") }
	adp := nfsdarwin.New(nfsdarwin.Config{})
	tmp := t.TempDir()
	trunkDir, backingDir, runtimeDir, cpStoreDir := filepath.Join(tmp, "trunk"), filepath.Join(tmp, "backing"), filepath.Join(tmp, "runtime"), filepath.Join(tmp, "cp-store")
	for _, d := range []string{trunkDir, backingDir, runtimeDir, cpStoreDir} {
		if err := os.MkdirAll(d, 0o755); err != nil { t.Fatalf("mkdir %s: %v", d, err) }
	}
	initialBytes := []byte("0123456789abcdef\n")
	if err := os.WriteFile(filepath.Join(trunkDir, "mapped.txt"), initialBytes, 0o644); err != nil { t.Fatalf("seed mapped.txt: %v", err) }

	const viewID = "view-mmap-contract"
	prep, err := adp.Prepare(t.Context(), adapter.PrepareRequest{
		ViewID: viewID, BackingDir: backingDir, RuntimeDir: runtimeDir,
		Grants: []adapter.RootGrant{{RootID: "primary", Label: "primary", SourcePath: trunkDir, IsPrimary: true}},
	})
	if err != nil { t.Fatalf("Prepare: %v", err) }
	t.Cleanup(func() { _, _ = adp.Release(t.Context(), viewID) })

	if rec1, _, bErr := adp.CheckpointBarrier(t.Context(), viewID); bErr == nil || rec1.WritersQuiesced || rec1.WritableMappingsFlushed {
		t.Fatalf("expected CheckpointBarrier without QuiesceHook to fail with false flags, got rec=%+v err=%v", rec1, bErr)
	}
	if err := adp.SetBarrierHooks(viewID, nfsdarwin.NoopQuiesceHook, nil); err != nil { t.Fatalf("SetBarrierHooks: %v", err) }
	if rec2, _, bErr := adp.CheckpointBarrier(t.Context(), viewID); bErr == nil || !rec2.WritersQuiesced || rec2.WritableMappingsFlushed {
		t.Fatalf("expected CheckpointBarrier without FlushClientHook to fail, got rec=%+v err=%v", rec2, bErr)
	}

	mf, err := os.OpenFile(filepath.Join(prep.PrimaryWorkPath, "mapped.txt"), os.O_RDWR, 0o644)
	if err != nil { t.Fatalf("OpenFile mapped.txt: %v", err) }
	defer func() { _ = mf.Close() }()
	_, _ = mf.ReadAt(make([]byte, len(initialBytes)), 0)

	mappedSlice, err := syscall.Mmap(int(mf.Fd()), 0, len(initialBytes), syscall.PROT_READ|syscall.PROT_WRITE, syscall.MAP_SHARED)
	if err != nil { t.Fatalf("syscall.Mmap: %v", err) }
	defer func() { _ = syscall.Munmap(mappedSlice) }()

	wantMapped := []byte("mutated-by-mmap!\n")
	copy(mappedSlice, wantMapped)
	if err := adp.SetBarrierHooks(viewID, nfsdarwin.NoopQuiesceHook, func(_ context.Context, _ string) error {
		if err := nfsdarwin.SyncMappedRegion(mappedSlice); err != nil { return err }
		return mf.Sync()
	}); err != nil { t.Fatalf("SetBarrierHooks with mmap sync: %v", err) }

	cpStore, err := checkpoint.NewStore(cpStoreDir)
	if err != nil { t.Fatalf("checkpoint.NewStore: %v", err) }
	pub, err := checkpoint.NewPublisher(checkpoint.PublisherConfig{
		Core: prep.Core, Store: cpStore, WorkerID: "worker-mmap", TaskID: "task-mmap",
		ProjectID: "proj-mmap", SourceGeneration: "gen-1",
		SelectedRoots: []layeredfs.RootSpec{{ID: "primary", Label: "primary", TrunkPath: trunkDir, IsPrimary: true}},
		Callbacks:     adp.NewBarrierProvider(viewID),
	})
	if err != nil { t.Fatalf("checkpoint.NewPublisher: %v", err) }
	if _, err := pub.Publish(t.Context(), checkpoint.PublishRequest{CheckpointID: "cp-mmap-1", Trigger: checkpoint.TriggerExplicitCapture}); err != nil {
		t.Fatalf("Publish cp-mmap-1 failed: %v", err)
	}
	if gotBlob := readCheckpointEntryBlob(t, cpStore, "cp-mmap-1", "mapped.txt"); !bytes.Equal(gotBlob, wantMapped) {
		t.Fatalf("expected published cp-mmap-1 blob %q, got %q", string(wantMapped), string(gotBlob))
	}

	adp.SetConfigCallbacksForTest(func(p string) error { return errors.New("injected sync failure on " + p) }, nil)
	defer adp.SetConfigCallbacksForTest(nil, nil)
	if _, err := pub.Publish(t.Context(), checkpoint.PublishRequest{CheckpointID: "cp-mmap-2", Trigger: checkpoint.TriggerExplicitCapture}); err == nil {
		t.Fatalf("expected Publish cp-mmap-2 to fail on injected sync error")
	}
	if gotBlob := readCheckpointEntryBlob(t, cpStore, "cp-mmap-1", "mapped.txt"); !bytes.Equal(gotBlob, wantMapped) {
		t.Fatalf("expected earlier cp-mmap-1 blob preserved (%q), got %q", string(wantMapped), string(gotBlob))
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
		ViewID: "view-retry-1", BackingDir: filepath.Join(tmp, "backing-1"), RuntimeDir: filepath.Join(tmp, "runtime-1"),
		Grants: []adapter.RootGrant{{RootID: "primary", Label: "primary", SourcePath: trunkDir, IsPrimary: true}},
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
		ViewID: "view-keep-2", BackingDir: filepath.Join(tmp, "backing-2"), RuntimeDir: filepath.Join(tmp, "runtime-2"),
		Grants: []adapter.RootGrant{{RootID: "primary", Label: "primary", SourcePath: trunkDir, IsPrimary: true}},
	})
	if err != nil {
		t.Fatalf("Prepare view-keep-2: %v", err)
	}
	t.Cleanup(func() { _, _ = adp.Release(t.Context(), "view-keep-2") })

	adp.SetUmountBinForTest("/usr/bin/false", "/usr/bin/false")
	if rel1, rErr := adp.Release(t.Context(), "view-retry-1"); rErr == nil || rel1.Settled || rel1.ReasonCode != adapter.ReasonCleanupFailed {
		t.Fatalf("expected first Release to fail unmount with Settled=false, got rel=%+v err=%v", rel1, rErr)
	}
	if adp.ServerForView("view-retry-1") == nil || !adp.IsViewActive("view-keep-2") {
		t.Fatalf("expected view-retry-1 retained and view-keep-2 still active")
	}
	if got2, rErr := os.ReadFile(filepath.Join(prep2.PrimaryWorkPath, "file.txt")); rErr != nil || string(got2) != "seed\n" {
		t.Fatalf("expected view-keep-2 mount readable, got %q err=%v", string(got2), rErr)
	}

	adp.SetUmountBinForTest("", "")
	adp.SetConfigCallbacksForTest(nil, func(path string) error { return errors.New("injected RemoveAll failure on " + path) })
	if rel2, rErr := adp.Release(t.Context(), "view-retry-1"); rErr == nil || rel2.Settled || len(rel2.CleanedPaths) != 0 {
		t.Fatalf("expected second Release to fail RuntimeViewDir cleanup with Settled=false, got rel=%+v err=%v", rel2, rErr)
	}

	adp.SetConfigCallbacksForTest(nil, nil)
	rel3, err := adp.Release(t.Context(), "view-retry-1")
	if err != nil || !rel3.Settled || len(rel3.CleanedPaths) != 1 || rel3.CleanedPaths[0] != prep1.RuntimeViewDir {
		t.Fatalf("expected third Release to settle cleanly, got rel=%+v err=%v", rel3, err)
	}
}

func readCheckpointEntryBlob(t *testing.T, cpStore *checkpoint.Store, checkpointID, relPath string) []byte {
	t.Helper()
	m, err := cpStore.LoadManifest(checkpointID)
	if err != nil { t.Fatalf("LoadManifest(%s): %v", checkpointID, err) }
	for _, entry := range m.Entries {
		if entry.Path.Rel == relPath {
			data, err := cpStore.ReadBlob(entry.Final.SHA256)
			if err != nil { t.Fatalf("ReadBlob(%s): %v", entry.Final.SHA256, err) }
			return data
		}
	}
	t.Fatalf("entry %s not found in manifest %s (entries=%+v)", relPath, checkpointID, m.Entries)
	return nil
}

func mountTestNFSConn(t *testing.T, addr, exportPath string, uid, gid uint32) (net.Conn, []byte) {
	t.Helper()
	conn, err := net.Dial("tcp", addr)
	if err != nil { t.Fatalf("dial %s: %v", addr, err) }
	var args bytes.Buffer
	writeTestXDRString(&args, exportPath)
	r := bytes.NewReader(sendTestRPCCall(t, conn, 1, 100005, 3, uint32(nfs.MountProcMount), uid, gid, args.Bytes()))
	if st := readTestUint32(t, r); st != uint32(nfs.MountStatusOk) { t.Fatalf("MOUNTPROC3_MNT status=%d", st) }
	fh := make([]byte, readTestUint32(t, r))
	if _, err := io.ReadFull(r, fh); err != nil { t.Fatalf("read root FH: %v", err) }
	return conn, fh
}

func lookupTestNFSFile(t *testing.T, conn net.Conn, dirFH []byte, name string, uid, gid uint32) []byte {
	t.Helper()
	var args bytes.Buffer
	writeTestXDROpaque(&args, dirFH); writeTestXDRString(&args, name)
	r := bytes.NewReader(sendTestRPCCall(t, conn, 2, 100003, 3, uint32(nfs.NFSProcedureLookup), uid, gid, args.Bytes()))
	if st := readTestUint32(t, r); st != uint32(nfs.NFSStatusOk) { t.Fatalf("LOOKUP(%s) status=%d", name, st) }
	fh := make([]byte, readTestUint32(t, r))
	if _, err := io.ReadFull(r, fh); err != nil { t.Fatalf("read file FH: %v", err) }
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
	return nfs.NFSStatus(readTestUint32(t, bytes.NewReader(sendTestRPCCall(t, conn, 3, 100003, 3, uint32(nfs.NFSProcedureWrite), uid, gid, args.Bytes()))))
}

func sendTestRPCCall(t *testing.T, conn net.Conn, xid, prog, vers, proc, uid, gid uint32, args []byte) []byte {
	t.Helper()
	var cred, msg, frame bytes.Buffer
	_ = binary.Write(&cred, binary.BigEndian, uint32(1)); writeTestXDRString(&cred, "localhost")
	for _, v := range []uint32{uid, gid, 0} { _ = binary.Write(&cred, binary.BigEndian, v) }
	for _, v := range []uint32{xid, 0, 2, prog, vers, proc, uint32(nfs.AuthFlavorUnix)} { _ = binary.Write(&msg, binary.BigEndian, v) }
	writeTestXDROpaque(&msg, cred.Bytes())
	_ = binary.Write(&msg, binary.BigEndian, uint32(nfs.AuthFlavorNull)); _ = binary.Write(&msg, binary.BigEndian, uint32(0))
	_, _ = msg.Write(args)
	_ = binary.Write(&frame, binary.BigEndian, uint32(msg.Len())|0x80000000); _, _ = frame.Write(msg.Bytes())
	if _, err := conn.Write(frame.Bytes()); err != nil { t.Fatalf("write RPC call: %v", err) }
	var hdrBuf [4]byte
	if _, err := io.ReadFull(conn, hdrBuf[:]); err != nil { t.Fatalf("read RPC reply header: %v", err) }
	payload := make([]byte, binary.BigEndian.Uint32(hdrBuf[:])&0x7fffffff)
	if _, err := io.ReadFull(conn, payload); err != nil { t.Fatalf("read RPC reply body: %v", err) }
	return payload[24:]
}

func writeTestXDROpaque(w *bytes.Buffer, b []byte) {
	_ = binary.Write(w, binary.BigEndian, uint32(len(b))); _, _ = w.Write(b)
	if pad := (4 - (len(b) & 3)) & 3; pad > 0 { var z [4]byte; _, _ = w.Write(z[:pad]) }
}

func writeTestXDRString(w *bytes.Buffer, s string) { writeTestXDROpaque(w, []byte(s)) }

func readTestUint32(t *testing.T, r io.Reader) uint32 {
	t.Helper()
	var v uint32
	if err := binary.Read(r, binary.BigEndian, &v); err != nil { t.Fatalf("read uint32: %v", err) }
	return v
}
