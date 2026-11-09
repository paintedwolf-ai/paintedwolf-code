package nfsserver_test

import (
	"bytes"
	"encoding/binary"
	"errors"
	"io"
	"net"
	"path/filepath"
	"sync/atomic"
	"syscall"
	"testing"
	"time"

	"github.com/lycaon/lycaon/internal/layeredfs"
	"github.com/lycaon/lycaon/internal/layeredfs/nfsserver"
	nfs "github.com/willscott/go-nfs"
)

func TestLoopbackOnlyListenerEnforcement(t *testing.T) {
	env := newTestEnv(t, nil)
	badAddrs := []string{
		"0.0.0.0:0",
		"[::]:0",
		":0",
		"192.0.2.1:2049",
		"invalid-addr",
	}
	for _, addr := range badAddrs {
		srv, err := nfsserver.New(env.Core, nfsserver.Config{ListenAddr: addr})
		if err == nil {
			_ = srv.Close()
			t.Fatalf("expected non-loopback address %q to fail", addr)
		}
		if !errors.Is(err, nfsserver.ErrNonLoopbackAddress) {
			t.Fatalf("addr %q: expected ErrNonLoopbackAddress, got %v", addr, err)
		}
	}
}

func TestTwoWorkersAndUnauthorizedLocalClientIsolation(t *testing.T) {
	env := newTestEnv(t, nil)
	cap1 := env.createViewAndExport(t, "worker-1", true)
	cap2 := env.createViewAndExport(t, "worker-2", true)
	unmountedCap := env.createViewAndExport(t, "worker-unmounted", true)

	// Worker 1 mounts worker-1 and creates secret-1.txt.
	w1 := dialRPC(t, env.Server.Addr(), env.UID, env.GID)
	st1, root1 := w1.mount(cap1.ExportPath)
	if st1 != nfs.MountStatusOk {
		t.Fatalf("worker-1 mount: %v", st1)
	}
	nst, secret1FH := w1.create(root1, "secret-1.txt", 0o600, 0, [8]byte{})
	if nst != nfs.NFSStatusOk {
		t.Fatalf("worker-1 create: %v", nst)
	}
	_, _, _, _ = w1.write(secret1FH, 0, []byte("worker-1-only"), nfsserver.FileSync)

	// Worker 2 mounts worker-2 and creates secret-2.txt.
	w2 := dialRPC(t, env.Server.Addr(), env.UID, env.GID)
	st2, root2 := w2.mount(cap2.ExportPath)
	if st2 != nfs.MountStatusOk {
		t.Fatalf("worker-2 mount: %v", st2)
	}
	nst, secret2FH := w2.create(root2, "secret-2.txt", 0o600, 0, [8]byte{})
	if nst != nfs.NFSStatusOk {
		t.Fatalf("worker-2 create: %v", nst)
	}
	_, _, _, _ = w2.write(secret2FH, 0, []byte("worker-2-only"), nfsserver.FileSync)

	// 1. Worker 1 cannot see secret-2.txt in its own view and cannot replay Worker 2's handle.
	if nst, _, _ := w1.lookup(root1, "secret-2.txt"); nst != nfs.NFSStatusNoEnt {
		t.Fatalf("worker-1 should not see secret-2.txt, got %v", nst)
	}
	if nst, _ := w1.getAttr(secret2FH); nst != nfs.NFSStatusAccess {
		t.Fatalf("worker-1 replaying worker-2 handle: got %v, want NFSStatusAccess", nst)
	}
	if nst, _, _ := w1.read(secret2FH, 0, 64); nst != nfs.NFSStatusAccess {
		t.Fatalf("worker-1 reading worker-2 handle: got %v, want NFSStatusAccess", nst)
	}
	if nst, _, _, _ := w1.write(secret2FH, 0, []byte("tamper"), nfsserver.FileSync); nst != nfs.NFSStatusAccess {
		t.Fatalf("worker-1 writing worker-2 handle: got %v, want NFSStatusAccess", nst)
	}
	if nst, _ := w2.getAttr(secret1FH); nst != nfs.NFSStatusAccess {
		t.Fatalf("worker-2 replaying worker-1 handle: got %v, want NFSStatusAccess", nst)
	}

	// 2. Unauthorized local client attempting guessed export paths -> MountStatusErrAuth.
	rogue := dialRPC(t, env.Server.Addr(), env.UID, env.GID)
	for _, guessed := range []string{
		"/",
		"/views/worker-1/project/guessed-token",
		"/views/worker-2/project/0000000000000000",
		unmountedCap.ExportPath + "-wrong",
	} {
		if mst, _ := rogue.mount(guessed); mst != nfs.MountStatusErrAcces {
			t.Fatalf("rogue mount(%q): got %v, want MountStatusErrAuth", guessed, mst)
		}
	}

	// 3. Unauthorized local client with wrong UID or AUTH_NULL -> MountStatusErrAuth.
	wrongUID := dialRPC(t, env.Server.Addr(), env.UID+777, env.GID)
	if mst, _ := wrongUID.mount(cap1.ExportPath); mst != nfs.MountStatusErrAcces {
		t.Fatalf("wrong UID mount: got %v, want MountStatusErrAuth", mst)
	}
	nullAuth := dialRPC(t, env.Server.Addr(), env.UID, env.GID)
	nullAuth.flavor = nfs.AuthFlavorNull
	if mst, _ := nullAuth.mount(cap1.ExportPath); mst != nfs.MountStatusErrAcces {
		t.Fatalf("AUTH_NULL mount: got %v, want MountStatusErrAuth", mst)
	}

	// 4. Unauthorized local client attempting direct RPC on side connection using Worker 1's handle
	// while Worker 1 is mounted -> rejected with NFSStatusAccess!
	if nst, _ := rogue.getAttr(secret1FH); nst != nfs.NFSStatusAccess {
		t.Fatalf("rogue direct RPC with replayed handle: got %v, want NFSStatusAccess", nst)
	}
	if nst, _ := wrongUID.getAttr(secret1FH); nst != nfs.NFSStatusAccess {
		t.Fatalf("wrong UID direct RPC: got %v, want NFSStatusAccess", nst)
	}
	if nst, _ := nullAuth.getAttr(secret1FH); nst != nfs.NFSStatusAccess {
		t.Fatalf("AUTH_NULL direct RPC: got %v, want NFSStatusAccess", nst)
	}

	// 5. Second loopback connection attempting MOUNTPROC3_MNT with the exact mounted export path
	// is refused by the single-mount latch (entry.mounted) and poisons the connection against handle replay.
	replayMountConn := dialRPC(t, env.Server.Addr(), env.UID, env.GID)
	if st, fh := replayMountConn.mount(cap1.ExportPath); st != nfs.MountStatusErrAcces || len(fh) != 0 {
		t.Fatalf("second mount of exact export path: got st=%v fh=%x, want MountStatusErrAcces", st, fh)
	}
	if nst, _ := replayMountConn.getAttr(root1); nst != nfs.NFSStatusAccess {
		t.Fatalf("GETATTR after rejected second mount: got %v, want NFSStatusAccess", nst)
	}

	// 6. Forged/random or HMAC-tampered 64-byte handles are refused on unmounted connections.
	forgedConn := dialRPC(t, env.Server.Addr(), env.UID, env.GID)
	randomFH := bytes.Repeat([]byte{0xa5}, nfsserver.WireHandleSize)
	if nst, _ := forgedConn.getAttr(randomFH); nst != nfs.NFSStatusAccess {
		t.Fatalf("GETATTR with random 64B handle: got %v, want NFSStatusAccess", nst)
	}
	tamperedFH := append([]byte(nil), root1...)
	tamperedFH[nfsserver.WireHandleSize-1] ^= 0xff
	if nst, _, _ := forgedConn.lookup(tamperedFH, "secret-1.txt"); nst != nfs.NFSStatusAccess {
		t.Fatalf("LOOKUP with tampered HMAC handle: got %v, want NFSStatusAccess", nst)
	}

	// 7. Root credentials (UID 0) are refused for GETATTR/WRITE/CREATE on side connections
	// and refused for mutating WRITE/CREATE even on the mount-bound connection.
	rootSideConn := dialRPC(t, env.Server.Addr(), 0, 0)
	if nst, _ := rootSideConn.getAttr(root1); nst != nfs.NFSStatusAccess {
		t.Fatalf("UID 0 GETATTR on non-mount-bound connection: got %v, want NFSStatusAccess", nst)
	}
	if nst, _, _, _ := rootSideConn.write(secret1FH, 0, []byte("x"), nfsserver.FileSync); nst != nfs.NFSStatusAccess {
		t.Fatalf("UID 0 WRITE on non-mount-bound connection: got %v, want NFSStatusAccess", nst)
	}
	w1.uid, w1.gid = 0, 0
	if nst, _, _, _ := w1.write(secret1FH, 0, []byte("x"), nfsserver.FileSync); nst != nfs.NFSStatusAccess {
		t.Fatalf("UID 0 WRITE on mount-bound connection: got %v, want NFSStatusAccess", nst)
	}
	if nst, _ := w1.create(root1, "root-pwn.txt", 0o644, 0, [8]byte{}); nst != nfs.NFSStatusAccess {
		t.Fatalf("UID 0 CREATE on mount-bound connection: got %v, want NFSStatusAccess", nst)
	}
}

func TestBoundedRPCFramingAndHugeOpaquePrefixRejection(t *testing.T) {
	env := newTestEnv(t, nil)
	capCfg := env.createViewAndExport(t, "view-bounds", true)

	// 1. Oversized RPC record fragment header (> MaxRequestBytes) must be rejected immediately without allocating.
	conn, err := net.DialTimeout("tcp", env.Server.Addr().String(), 3*time.Second)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer func() { _ = conn.Close() }()

	var hugeFrame [4]byte
	// Declare a 512 MiB last-fragment header.
	binary.BigEndian.PutUint32(hugeFrame[:], uint32(512<<20)|0x80000000)
	if _, err := conn.Write(hugeFrame[:]); err != nil {
		t.Fatalf("write huge header: %v", err)
	}
	_ = conn.SetReadDeadline(time.Now().Add(2 * time.Second))
	var buf [16]byte
	if _, err := conn.Read(buf[:]); err == nil {
		t.Fatal("expected server to close connection on oversized RPC frame")
	}

	// 2. Small valid RPC frame containing a 1 GiB opaque length prefix inside GETATTR body
	// must be rejected with NFSStatusInval without allocating 1 GiB.
	cli := dialRPC(t, env.Server.Addr(), env.UID, env.GID)
	_, _ = cli.mount(capCfg.ExportPath)
	var bogusBody bytes.Buffer
	putU32(&bogusBody, 1<<30) // 1 GiB length prefix in a 4-byte body
	code, r := cli.call(100003, 3, uint32(nfs.NFSProcedureGetAttr), bogusBody.Bytes())
	if code != nfs.ResponseCodeSuccess {
		t.Fatalf("expected RPC success wrapper, got %v", code)
	}
	if st := nfs.NFSStatus(getU32(t, r)); st != nfs.NFSStatusInval {
		t.Fatalf("expected NFSStatusInval for huge opaque prefix, got %v", st)
	}
}

func TestAppleDoubleLowerXattrRoundTripAndOrdinaryDotUnderscorePreservation(t *testing.T) {
	env := newTestEnv(t, nil)
	capCfg := env.createViewAndExport(t, "view-ad-lower", true)
	cli := dialRPC(t, env.Server.Addr(), env.UID, env.GID)
	_, rootFH := cli.mount(capCfg.ExportPath)

	readmeQP := layeredfs.QualifiedPath{RootID: nfsserver.ProjectRoot, Rel: "README.md"}
	quarantine := []byte("0081;66000000;Safari;")
	if err := env.Core.SetXattr(readmeQP, "com.apple.quarantine", quarantine, true); err != nil {
		t.Fatalf("SetXattr README.md: %v", err)
	}

	// 1. LOOKUP and READ synthesized ._README.md for lower file with xattrs, then round-trip to copy.md.
	nst, adReadmeFH, adAttr := cli.lookup(rootFH, "._README.md")
	if nst != nfs.NFSStatusOk || adAttr.Filesize == 0 {
		t.Fatalf("LOOKUP ._README.md: st=%v size=%d", nst, adAttr.Filesize)
	}
	nst, adData, eof := cli.read(adReadmeFH, 0, 4096)
	if nst != nfs.NFSStatusOk || !eof || len(adData) == 0 {
		t.Fatalf("READ ._README.md: st=%v eof=%v len=%d", nst, eof, len(adData))
	}
	_, copyFH := cli.create(rootFH, "README-copy.md", 0o644, 0, [8]byte{})
	_, _, _, _ = cli.write(copyFH, 0, []byte("# Copy\n"), nfsserver.FileSync)
	_, adCopyFH := cli.create(rootFH, "._README-copy.md", 0o644, 0, [8]byte{})
	_, _, _, _ = cli.write(adCopyFH, 0, adData, nfsserver.FileSync)

	copyQP := layeredfs.QualifiedPath{RootID: nfsserver.ProjectRoot, Rel: "README-copy.md"}
	gotQ, err := env.Core.GetXattr(copyQP, "com.apple.quarantine", true)
	if err != nil || !bytes.Equal(gotQ, quarantine) {
		t.Fatalf("quarantine round-trip mismatch: got=%q err=%v", gotQ, err)
	}

	// 2. Ordinary ._* file with non-AppleDouble partial content and rename is preserved intact.
	_, plainFH := cli.create(rootFH, "._ordinary.bin", 0o644, 0, [8]byte{})
	rawBytes := []byte("ordinary-non-appledouble-payload")
	_, _, _, _ = cli.write(plainFH, 0, rawBytes[:12], nfsserver.Unstable)
	_, _, _, _ = cli.write(plainFH, 12, rawBytes[12:], nfsserver.FileSync)
	var ren bytes.Buffer
	putBytes(&ren, rootFH)
	putStr(&ren, "._ordinary.bin")
	putBytes(&ren, rootFH)
	putStr(&ren, "._ordinary-renamed.bin")
	_, rr := cli.call(100003, 3, uint32(nfs.NFSProcedureRename), ren.Bytes())
	if st := nfs.NFSStatus(getU32(t, rr)); st != nfs.NFSStatusOk {
		t.Fatalf("RENAME ._ordinary.bin: %v", st)
	}
	gotRaw, err := env.Core.ReadFile(layeredfs.QualifiedPath{RootID: nfsserver.ProjectRoot, Rel: "._ordinary-renamed.bin"})
	if err != nil || !bytes.Equal(gotRaw, rawBytes) {
		t.Fatalf("ordinary ._* content lost: got=%q err=%v", gotRaw, err)
	}
}

func getU32(t *testing.T, r *bytes.Reader) uint32 {
	t.Helper()
	var b [4]byte
	if _, err := io.ReadFull(r, b[:]); err != nil {
		t.Fatalf("getU32: %v", err)
	}
	return binary.BigEndian.Uint32(b[:])
}

func getU64(t *testing.T, r *bytes.Reader) uint64 {
	t.Helper()
	var b [8]byte
	if _, err := io.ReadFull(r, b[:]); err != nil {
		t.Fatalf("getU64: %v", err)
	}
	return binary.BigEndian.Uint64(b[:])
}

func getBytes(t *testing.T, r *bytes.Reader) []byte {
	t.Helper()
	n := getU32(t, r)
	out := make([]byte, n)
	if _, err := io.ReadFull(r, out); err != nil {
		t.Fatalf("getBytes(%d): %v", n, err)
	}
	if pad := (4 - (n & 3)) & 3; pad > 0 {
		var discard [4]byte
		_, _ = io.ReadFull(r, discard[:pad])
	}
	return out
}

func getFattr3(t *testing.T, r *bytes.Reader) nfs.FileAttribute {
	t.Helper()
	var a nfs.FileAttribute
	a.Type = nfs.FileType(getU32(t, r))
	a.FileMode = getU32(t, r)
	a.Nlink = getU32(t, r)
	a.UID = getU32(t, r)
	a.GID = getU32(t, r)
	a.Filesize = getU64(t, r)
	a.Used = getU64(t, r)
	a.SpecData[0] = getU32(t, r)
	a.SpecData[1] = getU32(t, r)
	a.FSID = getU64(t, r)
	a.Fileid = getU64(t, r)
	a.Atime = nfs.FileTime{Seconds: getU32(t, r), Nseconds: getU32(t, r)}
	a.Mtime = nfs.FileTime{Seconds: getU32(t, r), Nseconds: getU32(t, r)}
	a.Ctime = nfs.FileTime{Seconds: getU32(t, r), Nseconds: getU32(t, r)}
	return a
}

func getPostOpAttr(t *testing.T, r *bytes.Reader) nfs.FileAttribute {
	t.Helper()
	if getU32(t, r) == 0 {
		return nfs.FileAttribute{}
	}
	return getFattr3(t, r)
}

func skipWcc(t *testing.T, r *bytes.Reader) {
	t.Helper()
	if getU32(t, r) == 1 {
		_ = getU64(t, r)
		_ = getU64(t, r)
		_ = getU64(t, r)
	}
	_ = getPostOpAttr(t, r)
}

func TestOwnerCreatedReadOnlyWriteSyncAndFaultPropagation(t *testing.T) {
	var (
		nodeSyncCalls atomic.Int32
		failNodeSync  atomic.Bool
		failOnSync    atomic.Bool
	)
	env := newTestEnv(t, &nfsserver.FaultInjector{
		OnSync: func(_ nfsserver.ViewID, _ layeredfs.QualifiedPath) error {
			if failOnSync.Load() {
				return syscall.EIO
			}
			return nil
		},
	})
	viewCore, err := layeredfs.New(layeredfs.Config{
		ViewID:     "view-ro-sync",
		BackingDir: filepath.Join(env.StateDir, "view-ro-sync"),
		Roots: []layeredfs.RootSpec{
			{ID: nfsserver.ProjectRoot, Label: "project", TrunkPath: env.ProjDir, IsPrimary: true},
			{ID: nfsserver.WorktreeRoot, Label: "worktree", TrunkPath: env.WorkDir},
		},
		FaultHooks: &layeredfs.FaultHooks{
			JournalSyncHook: func(phase string) error {
				if phase == "handle_sync" {
					nodeSyncCalls.Add(1)
					if failNodeSync.Load() {
						return syscall.EIO
					}
				}
				return nil
			},
		},
	})
	if err != nil {
		t.Fatalf("layeredfs.New(view-ro-sync): %v", err)
	}
	t.Cleanup(func() { _ = viewCore.ReleaseView() })
	nfsserver.BindViewCore(env.Server, "view-ro-sync", viewCore, false)
	capCfg, err := env.Server.RegisterExport("view-ro-sync", nfsserver.ProjectRoot, "")
	if err != nil {
		t.Fatalf("RegisterExport: %v", err)
	}

	cli := dialRPC(t, env.Server.Addr(), env.UID, env.GID)
	_, rootFH := cli.mount(capCfg.ExportPath)
	nst, roFH := cli.create(rootFH, "git-obj-0444.idx", 0o444, 0, [8]byte{})
	if nst != nfs.NFSStatusOk {
		t.Fatalf("CREATE git-obj-0444.idx: %v", nst)
	}

	// 1. DATA_SYNC write on owner-created 0444 file must invoke the backing sync ("handle_sync").
	if st, _, comm, _ := cli.write(roFH, 0, []byte("pack-header-"), nfsserver.DataSync); st != nfs.NFSStatusOk || comm != nfsserver.DataSync {
		t.Fatalf("DATA_SYNC write: st=%v comm=%v", st, comm)
	}
	if got := nodeSyncCalls.Load(); got != 1 {
		t.Fatalf("expected 1 node_sync call after DATA_SYNC write, got %d", got)
	}

	// 2. Injected the backing sync failure on FILE_SYNC write must reject stability acknowledgment.
	failNodeSync.Store(true)
	if st, _, _, _ := cli.write(roFH, 12, []byte("pack-body"), nfsserver.FileSync); st != nfs.NFSStatusIO {
		t.Fatalf("expected NFSStatusIO when the backing sync fails on FILE_SYNC, got %v", st)
	}
	failNodeSync.Store(false)
	if st, _, comm, _ := cli.write(roFH, 12, []byte("pack-body"), nfsserver.FileSync); st != nfs.NFSStatusOk || comm != nfsserver.FileSync {
		t.Fatalf("FILE_SYNC retry: st=%v comm=%v", st, comm)
	}
	if got := nodeSyncCalls.Load(); got != 3 {
		t.Fatalf("expected 3 node_sync calls after FILE_SYNC retry, got %d", got)
	}
	if st, attr := cli.getAttr(roFH); st != nfs.NFSStatusOk || attr.FileMode&0o777 != 0o444 {
		t.Fatalf("expected 0444 mode preserved, got st=%v mode=%#o", st, attr.FileMode&0o777)
	}

	// 3. UNSTABLE write + COMMIT must invoke the backing sync and retain pending chunks if sync fails.
	if st, _, _, _ := cli.write(roFH, 21, []byte("-trailer"), nfsserver.Unstable); st != nfs.NFSStatusOk {
		t.Fatalf("UNSTABLE write: %v", st)
	}
	failNodeSync.Store(true)
	if st, _ := cli.commit(roFH, 0, 29); st != nfs.NFSStatusIO {
		t.Fatalf("expected NFSStatusIO when the backing sync fails on COMMIT, got %v", st)
	}
	if vb, vc, _ := nfsserver.PendingUnstableStats(env.Server, "view-ro-sync"); vb != 8 || vc != 1 {
		t.Fatalf("expected pending chunk retained after the backing sync failure, got vb=%d vc=%d", vb, vc)
	}
	failNodeSync.Store(false)
	failOnSync.Store(true)
	if st, _ := cli.commit(roFH, 0, 29); st != nfs.NFSStatusIO {
		t.Fatalf("expected NFSStatusIO when OnSync fails on COMMIT, got %v", st)
	}
	failOnSync.Store(false)
	if st, _ := cli.commit(roFH, 0, 29); st != nfs.NFSStatusOk {
		t.Fatalf("COMMIT retry: %v", st)
	}
	if got := nodeSyncCalls.Load(); got != 5 {
		t.Fatalf("expected 5 total node_sync calls after COMMIT, got %d", got)
	}
	if st, data, _ := cli.read(roFH, 0, 64); st != nfs.NFSStatusOk || string(data) != "pack-header-pack-body-trailer" {
		t.Fatalf("READ after COMMIT: st=%v data=%q", st, data)
	}
	if err := env.Server.FlushForCheckpoint(t.Context(), "view-ro-sync"); err != nil {
		t.Fatalf("FlushForCheckpoint: %v", err)
	}
	snap, err := viewCore.LoadPersistedJournal()
	if err != nil || len(snap.Changes) == 0 {
		t.Fatalf("LoadPersistedJournal: changes=%d err=%v", len(snap.Changes), err)
	}
}
