package nfsserver_test

import (
	"bytes"
	"encoding/binary"
	"errors"
	"io"
	"net"
	"testing"
	"time"

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
