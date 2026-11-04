package nfsserver_test

import (
	"bytes"
	"encoding/binary"
	"fmt"
	"net"
	"sync/atomic"
	"syscall"
	"testing"
	"time"

	"github.com/lycaon/lycaon/internal/layeredfs"
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

	// 5. UMNT is advisory: another connection cannot revoke a live mount with it.
	umntCli := dialRPC(t, env.Server.Addr(), env.UID, env.GID)
	var umntArgs bytes.Buffer
	putStr(&umntArgs, capCfg.ExportPath)
	if code, _ := umntCli.call(100005, 3, uint32(nfs.MountProcUmnt), umntArgs.Bytes()); code != nfs.ResponseCodeSuccess {
		t.Fatalf("umntCli UMNT: %v", code)
	}
	if nst, _ := reconnectCli.getAttr(fileFH); nst != nfs.NFSStatusOk {
		t.Fatalf("expected the mount to survive UMNT from a separate connection, got %v", nst)
	}
}

func TestServerBoundsConcurrentConnectionsAndIncompleteRecords(t *testing.T) {
	env := newTestEnv(t, nil)
	srv, err := nfsserver.New(env.Core, nfsserver.Config{
		ListenAddr: "127.0.0.1:0", MaxConnections: 2,
		ReadHeaderTimeout: 100 * time.Millisecond, IdleTimeout: 100 * time.Millisecond,
		ExpectedUID: env.UID, ExpectedGID: env.GID,
	})
	if err != nil {
		t.Fatalf("nfsserver.New bounded: %v", err)
	}
	t.Cleanup(func() { _ = srv.Close() })
	capCfg, err := srv.RegisterExport(env.Core.ViewID(), nfsserver.ProjectRoot, "")
	if err != nil {
		t.Fatalf("RegisterExport: %v", err)
	}

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

	for deadline := time.Now().Add(500 * time.Millisecond); srv.ActiveConnections() < 2 && time.Now().Before(deadline); {
		time.Sleep(5 * time.Millisecond)
	}
	if got := srv.ActiveConnections(); got > 2 {
		t.Fatalf("ActiveConnections exceeded MaxConnections(2): got %d", got)
	}

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

	_ = c1.SetReadDeadline(time.Now().Add(500 * time.Millisecond))
	if _, err := c1.Read(buf[:]); err == nil {
		t.Fatal("expected incomplete header connection c1 to be settled and closed")
	}
	_ = c2.SetReadDeadline(time.Now().Add(500 * time.Millisecond))
	if _, err := c2.Read(buf[:]); err == nil {
		t.Fatal("expected incomplete body connection c2 to be settled and closed")
	}
	for waitEmpty := time.Now().Add(500 * time.Millisecond); srv.ActiveConnections() != 0 && time.Now().Before(waitEmpty); {
		time.Sleep(5 * time.Millisecond)
	}
	if got := srv.ActiveConnections(); got != 0 {
		t.Fatalf("expected 0 active connections after timeout settlement, got %d", got)
	}

	cli := dialRPC(t, srv.Addr(), env.UID, env.GID)
	mst, rootFH := cli.mount(capCfg.ExportPath)
	if mst != nfs.MountStatusOk {
		t.Fatalf("MOUNT after connection recovery failed: %v", mst)
	}
	if nst, _ := cli.getAttr(rootFH); nst != nfs.NFSStatusOk {
		t.Fatalf("GETATTR after connection recovery failed: %v", nst)
	}
}

func TestConcurrentUnstableWriteDuringCommitSurvivesAndFlushes(t *testing.T) {
	syncEntered, releaseSync := make(chan struct{}, 4), make(chan struct{})
	var pauseSync, failSync atomic.Bool
	env := newTestEnv(t, &nfsserver.FaultInjector{
		OnSync: func(_ nfsserver.ViewID, _ layeredfs.QualifiedPath) error {
			if pauseSync.CompareAndSwap(true, false) {
				syncEntered <- struct{}{}
				<-releaseSync
			}
			if failSync.Load() {
				return syscall.EIO
			}
			return nil
		},
	})
	capCfg := env.createViewAndExport(t, "view-conc", true)
	c1, c2 := dialRPC(t, env.Server.Addr(), env.UID, env.GID), dialRPC(t, env.Server.Addr(), env.UID, env.GID)
	_, rootFH := c1.mount(capCfg.ExportPath)
	_, fh := c1.create(rootFH, "conc.txt", 0o644, 0, [8]byte{})

	if st, _, _, _ := c1.write(fh, 0, []byte("first-part"), nfsserver.Unstable); st != nfs.NFSStatusOk {
		t.Fatalf("c1 write 1: %v", st)
	}
	pauseSync.Store(true)
	commitDone := make(chan nfs.NFSStatus, 1)
	go func() { st, _ := c1.commit(fh, 0, 10); commitDone <- st }()
	<-syncEntered
	if st, _, _, _ := c2.write(fh, 10, []byte("-second-part"), nfsserver.Unstable); st != nfs.NFSStatusOk {
		t.Fatalf("c2 concurrent write 2 during paused COMMIT: %v", st)
	}
	releaseSync <- struct{}{}
	if st := <-commitDone; st != nfs.NFSStatusOk {
		t.Fatalf("c1 COMMIT 1: %v", st)
	}
	if vb, vc, _ := nfsserver.PendingUnstableStats(env.Server, "view-conc"); vb != 12 || vc != 1 {
		t.Fatalf("expected second write retained in pending state (12B, 1 chunk), got bytes=%d chunks=%d", vb, vc)
	}
	failSync.Store(true)
	if st, _ := c2.commit(fh, 0, 22); st != nfs.NFSStatusIO {
		t.Fatalf("expected NFSStatusIO on failed COMMIT, got %v", st)
	}
	if vb, vc, _ := nfsserver.PendingUnstableStats(env.Server, "view-conc"); vb != 12 || vc != 1 {
		t.Fatalf("failed COMMIT discarded pending write: bytes=%d chunks=%d", vb, vc)
	}
	failSync.Store(false)
	if st, _ := c2.commit(fh, 0, 22); st != nfs.NFSStatusOk {
		t.Fatalf("c2 COMMIT retry: %v", st)
	}
	if got, err := env.Core.ReadFile(layeredfs.QualifiedPath{RootID: nfsserver.ProjectRoot, Rel: "conc.txt"}); err != nil || string(got) != "first-part-second-part" {
		t.Fatalf("after second COMMIT got %q err=%v", got, err)
	}

	_, _ = env.Core.Truncate(layeredfs.QualifiedPath{RootID: nfsserver.ProjectRoot, Rel: "conc.txt"}, 0)
	_, _, _, _ = c1.write(fh, 0, []byte("UUUUUUUUUUUUUUUU"), nfsserver.Unstable)
	pauseSync.Store(true)
	stableDone := make(chan nfs.NFSStatus, 1)
	go func() { st, _, _, _ := c1.write(fh, 4, []byte("SSSSSSSS"), nfsserver.FileSync); stableDone <- st }()
	<-syncEntered
	if st, _, _, _ := c2.write(fh, 0, []byte("XXXX"), nfsserver.Unstable); st != nfs.NFSStatusOk {
		t.Fatalf("c2 overlapping UNSTABLE write during WriteStable: %v", st)
	}
	releaseSync <- struct{}{}
	if st := <-stableDone; st != nfs.NFSStatusOk {
		t.Fatalf("c1 WriteStable: %v", st)
	}
	if st, _ := c2.commit(fh, 0, 16); st != nfs.NFSStatusOk {
		t.Fatalf("c2 final COMMIT: %v", st)
	}
	if got, err := env.Core.ReadFile(layeredfs.QualifiedPath{RootID: nfsserver.ProjectRoot, Rel: "conc.txt"}); err != nil || string(got) != "XXXXSSSSSSSSUUUU" {
		t.Fatalf("overlap result got %q, want %q (err=%v)", got, "XXXXSSSSSSSSUUUU", err)
	}
}

func TestUnstableWriteCompactionAndBoundedPendingBytes(t *testing.T) {
	var failSync atomic.Bool
	env := newTestEnv(t, nil)
	srv, err := nfsserver.New(env.Core, nfsserver.Config{
		ListenAddr: "127.0.0.1:0", MaxUnstableBytesPerView: 1024, MaxUnstableChunksPerView: 4, MaxTotalUnstableBytes: 1536,
		ExpectedUID: env.UID, ExpectedGID: env.GID,
		FaultInjector: &nfsserver.FaultInjector{
			OnSync: func(_ nfsserver.ViewID, _ layeredfs.QualifiedPath) error {
				if failSync.Load() {
					return syscall.EIO
				}
				return nil
			},
		},
	})
	if err != nil {
		t.Fatalf("nfsserver.New: %v", err)
	}
	t.Cleanup(func() { _ = srv.Close() })
	env.Server = srv
	cap1 := env.createViewAndExport(t, "view-cap1", true)
	cli := dialRPC(t, srv.Addr(), env.UID, env.GID)
	_, root1 := cli.mount(cap1.ExportPath)
	_, ovFH := cli.create(root1, "overwrite.bin", 0o644, 0, [8]byte{})

	for i := range 40 {
		payload := bytes.Repeat([]byte{byte('A' + (i % 26))}, 256)
		if st, _, _, _ := cli.write(ovFH, 0, payload, nfsserver.Unstable); st != nfs.NFSStatusOk {
			t.Fatalf("overwrite %d: %v", i, st)
		}
		if vb, vc, tb := nfsserver.PendingUnstableStats(srv, "view-cap1"); vb != 256 || vc != 1 || tb != 256 {
			t.Fatalf("iter %d: expected compacted 256B/1chunk, got vb=%d vc=%d tb=%d", i, vb, vc, tb)
		}
	}
	if st, _ := cli.commit(ovFH, 0, 256); st != nfs.NFSStatusOk {
		t.Fatalf("COMMIT overwrite.bin: %v", st)
	}

	var fhs [][]byte
	for i := range 10 {
		_, fh := cli.create(root1, fmt.Sprintf("f-%d.bin", i), 0o644, 0, [8]byte{})
		fhs = append(fhs, fh)
		data := bytes.Repeat([]byte{byte('0' + i)}, 300)
		if st, _, _, _ := cli.write(fh, 0, data, nfsserver.Unstable); st != nfs.NFSStatusOk {
			t.Fatalf("write f-%d.bin: %v", i, st)
		}
		if vb, vc, tb := nfsserver.PendingUnstableStats(srv, "view-cap1"); vb > 1024 || vc > 4 || tb > 1536 {
			t.Fatalf("bounds exceeded at file %d: vb=%d vc=%d tb=%d", i, vb, vc, tb)
		}
	}
	failSync.Store(true)
	_, extraFH := cli.create(root1, "reject.bin", 0o644, 0, [8]byte{})
	if st, _, _, _ := cli.write(extraFH, 0, bytes.Repeat([]byte("X"), 900), nfsserver.Unstable); st != nfs.NFSStatusIO {
		t.Fatalf("expected NFSStatusIO when backpressure flush fails, got %v", st)
	}
	failSync.Store(false)
	for i, fh := range fhs {
		if st, _ := cli.commit(fh, 0, 300); st != nfs.NFSStatusOk {
			t.Fatalf("COMMIT f-%d.bin: %v", i, st)
		}
		if st, got, _ := cli.read(fh, 0, 400); st != nfs.NFSStatusOk || !bytes.Equal(got, bytes.Repeat([]byte{byte('0'+i)}, 300)) {
			t.Fatalf("READ f-%d.bin mismatch: st=%v len=%d", i, st, len(got))
		}
	}
}
