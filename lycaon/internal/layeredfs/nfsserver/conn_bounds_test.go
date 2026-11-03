package nfsserver_test

import (
	"bytes"
	"encoding/binary"
	"net"
	"testing"
	"time"

	"github.com/lycaon/lycaon/internal/layeredfs/nfsserver"
	nfs "github.com/willscott/go-nfs"
)

func TestSeparateMountAndDataConnectionsAndReconnect(t *testing.T) {
	env := newTestEnv(t, nil)
	capCfg := env.createViewAndExport(t, "view-multiconn", true)

	// 1. Mount connection (like macOS mount_nfs opening a separate TCP connection for MOUNT3_MNT).
	mountCli := dialRPC(t, env.Server.Addr(), env.UID, env.GID)
	mst, rootFH := mountCli.mount(capCfg.ExportPath)
	if mst != nfs.MountStatusOk || len(rootFH) != nfsserver.HandleSize {
		t.Fatalf("mountCli.mount: st=%v len=%d", mst, len(rootFH))
	}

	// 2. While mountCli remains open, open two separate NFSv3 data TCP connections and perform I/O.
	dataCli1 := dialRPC(t, env.Server.Addr(), env.UID, env.GID)
	dataCli2 := dialRPC(t, env.Server.Addr(), env.UID, env.GID)

	if nst, attr := dataCli1.getAttr(rootFH); nst != nfs.NFSStatusOk || attr.Type != nfs.FileTypeDirectory {
		t.Fatalf("dataCli1 GETATTR rootFH while mountCli open: st=%v type=%v", nst, attr.Type)
	}
	nst, fileFH := dataCli1.create(rootFH, "split-conn.txt", 0o644, 0, [8]byte{})
	if nst != nfs.NFSStatusOk {
		t.Fatalf("dataCli1 CREATE split-conn.txt: %v", nst)
	}
	if nst, _, _, _ := dataCli2.write(fileFH, 0, []byte("from-data-conn-2"), nfsserver.Unstable); nst != nfs.NFSStatusOk {
		t.Fatalf("dataCli2 WRITE split-conn.txt: %v", nst)
	}
	if nst, got, _ := dataCli1.read(fileFH, 0, 64); nst != nfs.NFSStatusOk || string(got) != "from-data-conn-2" {
		t.Fatalf("dataCli1 READ split-conn.txt: st=%v data=%q", nst, got)
	}

	// 3. Wrong GID must be rejected on both MOUNT and direct NFSv3 RPC (AUTH_SYS checks both UID and GID).
	wrongGID := dialRPC(t, env.Server.Addr(), env.UID, env.GID+777)
	if mst, _ := wrongGID.mount(capCfg.ExportPath); mst != nfs.MountStatusErrAcces {
		t.Fatalf("wrong GID mount: got %v, want MountStatusErrAcces", mst)
	}
	wrongGIDDirect := dialRPC(t, env.Server.Addr(), env.UID, env.GID+777)
	if nst, _ := wrongGIDDirect.getAttr(fileFH); nst != nfs.NFSStatusAccess {
		t.Fatalf("wrong GID direct RPC: got %v, want NFSStatusAccess", nst)
	}

	// 4. Close mountCli and dataCli1/dataCli2, then reconnect on a new TCP socket and COMMIT + READ.
	_ = mountCli.conn.Close()
	_ = dataCli1.conn.Close()
	_ = dataCli2.conn.Close()

	reconnectCli := dialRPC(t, env.Server.Addr(), env.UID, env.GID)
	if nst, _ := reconnectCli.commit(fileFH, 0, 16); nst != nfs.NFSStatusOk {
		t.Fatalf("reconnectCli COMMIT after disconnect: %v", nst)
	}
	if nst, got, _ := reconnectCli.read(fileFH, 0, 64); nst != nfs.NFSStatusOk || string(got) != "from-data-conn-2" {
		t.Fatalf("reconnectCli READ after disconnect: st=%v data=%q", nst, got)
	}

	// 5. Unmount from a separate TCP connection revokes the mount for existing data connections.
	umntCli := dialRPC(t, env.Server.Addr(), env.UID, env.GID)
	var umntArgs bytes.Buffer
	putStr(&umntArgs, capCfg.ExportPath)
	if code, _ := umntCli.call(100005, 3, uint32(nfs.MountProcUmnt), umntArgs.Bytes()); code != nfs.ResponseCodeSuccess {
		t.Fatalf("umntCli UMNT: %v", code)
	}
	if nst, _ := reconnectCli.getAttr(fileFH); nst != nfs.NFSStatusAccess {
		t.Fatalf("expected NFSStatusAccess after UMNT on separate connection, got %v", nst)
	}
}

func TestServerBoundsConcurrentConnectionsAndIncompleteRecords(t *testing.T) {
	env := newTestEnv(t, nil)
	srv, err := nfsserver.New(env.Core, nfsserver.Config{
		ListenAddr:        "127.0.0.1:0",
		MaxConnections:    2,
		ReadHeaderTimeout: 100 * time.Millisecond,
		IdleTimeout:       100 * time.Millisecond,
		ExpectedUID:       env.UID,
		ExpectedGID:       env.GID,
	})
	if err != nil {
		t.Fatalf("nfsserver.New bounded: %v", err)
	}
	t.Cleanup(func() { _ = srv.Close() })

	capCfg, err := srv.RegisterExport(env.Core.ViewID(), nfsserver.ProjectRoot, "")
	if err != nil {
		t.Fatalf("RegisterExport: %v", err)
	}

	// 1. Open 2 connections that send incomplete RPC records and stall:
	//    c1 sends only 2 bytes of the 4-byte fragment header;
	//    c2 sends a 4-byte fragment header claiming 64 bytes, followed by only 8 bytes.
	c1, err := net.DialTimeout("tcp", srv.Addr().String(), 2*time.Second)
	if err != nil {
		t.Fatalf("dial c1: %v", err)
	}
	defer func() { _ = c1.Close() }()
	_, _ = c1.Write([]byte{0x80, 0x00})

	c2, err := net.DialTimeout("tcp", srv.Addr().String(), 2*time.Second)
	if err != nil {
		t.Fatalf("dial c2: %v", err)
	}
	defer func() { _ = c2.Close() }()
	var partialFrag [12]byte
	binary.BigEndian.PutUint32(partialFrag[0:4], 64|0x80000000)
	_, _ = c2.Write(partialFrag[:])

	deadline := time.Now().Add(500 * time.Millisecond)
	for srv.ActiveConnections() < 2 && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
	if got := srv.ActiveConnections(); got > 2 {
		t.Fatalf("ActiveConnections exceeded MaxConnections(2): got %d", got)
	}

	// 2. While c1 and c2 occupy the 2 connection slots, a 3rd connection c3 is rejected/closed immediately.
	c3, err := net.DialTimeout("tcp", srv.Addr().String(), 2*time.Second)
	if err != nil {
		t.Fatalf("dial c3: %v", err)
	}
	defer func() { _ = c3.Close() }()
	_ = c3.SetReadDeadline(time.Now().Add(250 * time.Millisecond))
	var buf [8]byte
	if _, err := c3.Read(buf[:]); err == nil {
		t.Fatal("expected excess 3rd connection c3 to be closed immediately when at MaxConnections")
	}
	if got := srv.ActiveConnections(); got > 2 {
		t.Fatalf("ActiveConnections exceeded MaxConnections(2) after c3: got %d", got)
	}

	// 3. Incomplete records on c1 and c2 are settled/closed once ReadHeaderTimeout elapses.
	_ = c1.SetReadDeadline(time.Now().Add(500 * time.Millisecond))
	if _, err := c1.Read(buf[:]); err == nil {
		t.Fatal("expected incomplete header connection c1 to be settled and closed")
	}
	_ = c2.SetReadDeadline(time.Now().Add(500 * time.Millisecond))
	if _, err := c2.Read(buf[:]); err == nil {
		t.Fatal("expected incomplete body connection c2 to be settled and closed")
	}

	waitEmpty := time.Now().Add(500 * time.Millisecond)
	for srv.ActiveConnections() != 0 && time.Now().Before(waitEmpty) {
		time.Sleep(5 * time.Millisecond)
	}
	if got := srv.ActiveConnections(); got != 0 {
		t.Fatalf("expected 0 active connections after timeout settlement, got %d", got)
	}

	// 4. Subsequent normal client connection succeeds once stalled connections are reclaimed.
	cli := dialRPC(t, srv.Addr(), env.UID, env.GID)
	mst, rootFH := cli.mount(capCfg.ExportPath)
	if mst != nfs.MountStatusOk {
		t.Fatalf("MOUNT after connection recovery failed: %v", mst)
	}
	if nst, _ := cli.getAttr(rootFH); nst != nfs.NFSStatusOk {
		t.Fatalf("GETATTR after connection recovery failed: %v", nst)
	}
}
