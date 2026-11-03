package nfsserver_test

import (
	"bytes"
	"encoding/binary"
	"io"
	"net"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"github.com/lycaon/lycaon/internal/layeredfs"
	"github.com/lycaon/lycaon/internal/layeredfs/nfsserver"
	nfs "github.com/willscott/go-nfs"
)

type testEnv struct {
	Core     *layeredfs.Core
	Server   *nfsserver.Server
	BaseDir  string
	ProjDir  string
	WorkDir  string
	StateDir string
	UID      uint32
	GID      uint32
}

func newTestEnv(t *testing.T, fault *nfsserver.FaultInjector) *testEnv {
	t.Helper()
	tmp := t.TempDir()
	projDir := filepath.Join(tmp, "project")
	workDir := filepath.Join(tmp, "worktree")
	stateDir := filepath.Join(tmp, "state")
	for _, d := range []string{projDir, workDir, stateDir} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatalf("mkdir %s: %v", d, err)
		}
	}
	if err := os.WriteFile(filepath.Join(projDir, "README.md"), []byte("hello world\n"), 0o644); err != nil {
		t.Fatalf("seed README: %v", err)
	}
	core, err := layeredfs.New(layeredfs.Config{
		ViewID:     "default",
		BackingDir: filepath.Join(stateDir, "default"),
		Roots: []layeredfs.RootSpec{
			{ID: nfsserver.ProjectRoot, Label: "project", TrunkPath: projDir, IsPrimary: true},
			{ID: nfsserver.WorktreeRoot, Label: "worktree", TrunkPath: workDir},
		},
	})
	if err != nil {
		t.Fatalf("layeredfs.New: %v", err)
	}
	t.Cleanup(func() { _ = core.ReleaseView() })

	uid := uint32(os.Geteuid())
	gid := uint32(os.Getegid())
	srv, err := nfsserver.New(core, nfsserver.Config{
		ListenAddr: "127.0.0.1:0", ExpectedUID: uid, ExpectedGID: gid,
		HasExpectedUID: true, HasExpectedGID: true, FaultInjector: fault,
	})
	if err != nil {
		t.Fatalf("nfsserver.New: %v", err)
	}
	t.Cleanup(func() { _ = srv.Close() })

	return &testEnv{
		Core: core, Server: srv, BaseDir: tmp, ProjDir: projDir,
		WorkDir: workDir, StateDir: stateDir, UID: uid, GID: gid,
	}
}

func (e *testEnv) createViewAndExport(t *testing.T, viewID nfsserver.ViewID, writable bool) nfsserver.ExportCapability {
	t.Helper()
	viewCore, err := layeredfs.New(layeredfs.Config{
		ViewID:     viewID,
		BackingDir: filepath.Join(e.StateDir, viewID),
		Roots: []layeredfs.RootSpec{
			{ID: nfsserver.ProjectRoot, Label: "project", TrunkPath: e.ProjDir, IsPrimary: true},
			{ID: nfsserver.WorktreeRoot, Label: "worktree", TrunkPath: e.WorkDir},
		},
	})
	if err != nil {
		t.Fatalf("CreateView(%s): %v", viewID, err)
	}
	t.Cleanup(func() { _ = viewCore.ReleaseView() })
	e.Core = viewCore
	nfsserver.BindViewCore(e.Server, viewID, viewCore, !writable)
	capCfg, err := e.Server.RegisterExport(viewID, nfsserver.ProjectRoot, "")
	if err != nil {
		t.Fatalf("RegisterExport(%s): %v", viewID, err)
	}
	return capCfg
}

type rpcClient struct {
	t      *testing.T
	conn   net.Conn
	uid    uint32
	gid    uint32
	flavor nfs.AuthFlavor
	xidSeq atomic.Uint32
}

func dialRPC(t *testing.T, addr net.Addr, uid, gid uint32) *rpcClient {
	t.Helper()
	conn, err := net.DialTimeout("tcp", addr.String(), 5*time.Second)
	if err != nil {
		t.Fatalf("dial %s: %v", addr, err)
	}
	t.Cleanup(func() { _ = conn.Close() })
	return &rpcClient{t: t, conn: conn, uid: uid, gid: gid, flavor: nfs.AuthFlavorUnix}
}

func (c *rpcClient) call(prog, vers, proc uint32, args []byte) (nfs.ResponseCode, *bytes.Reader) {
	c.t.Helper()
	xid := c.xidSeq.Add(1)
	var msg bytes.Buffer
	putU32(&msg, xid)
	putU32(&msg, 0) // CALL
	putU32(&msg, 2) // RPC v2
	putU32(&msg, prog)
	putU32(&msg, vers)
	putU32(&msg, proc)

	putU32(&msg, uint32(c.flavor))
	if c.flavor == nfs.AuthFlavorUnix {
		var cred bytes.Buffer
		putU32(&cred, 1)
		putStr(&cred, "localhost")
		putU32(&cred, c.uid)
		putU32(&cred, c.gid)
		putU32(&cred, 1)
		putU32(&cred, c.gid)
		putBytes(&msg, cred.Bytes())
	} else {
		putBytes(&msg, nil)
	}
	// Verifier: AUTH_NULL
	putU32(&msg, 0)
	putU32(&msg, 0)
	_, _ = msg.Write(args)

	var frame bytes.Buffer
	putU32(&frame, uint32(msg.Len())|0x80000000)
	_, _ = frame.Write(msg.Bytes())
	if _, err := c.conn.Write(frame.Bytes()); err != nil {
		c.t.Fatalf("write RPC frame: %v", err)
	}

	var hdr [4]byte
	if _, err := io.ReadFull(c.conn, hdr[:]); err != nil {
		c.t.Fatalf("read RPC reply header: %v", err)
	}
	respLen := binary.BigEndian.Uint32(hdr[:]) & 0x7fffffff
	respBuf := make([]byte, respLen)
	if _, err := io.ReadFull(c.conn, respBuf); err != nil {
		c.t.Fatalf("read RPC reply body: %v", err)
	}
	r := bytes.NewReader(respBuf)
	gotXID := getU32(c.t, r)
	if gotXID != xid {
		c.t.Fatalf("XID mismatch: got %d, want %d", gotXID, xid)
	}
	if replyType := getU32(c.t, r); replyType != 1 {
		c.t.Fatalf("expected REPLY(1), got %d", replyType)
	}
	replyStat := getU32(c.t, r)
	if replyStat != 0 {
		return nfs.ResponseCodeAuthError, r
	}
	_ = getU32(c.t, r) // verf flavor
	_ = getBytes(c.t, r)
	acceptStat := nfs.ResponseCode(getU32(c.t, r))
	return acceptStat, r
}

func (c *rpcClient) mount(exportPath string) (nfs.MountStatus, []byte) {
	c.t.Helper()
	var args bytes.Buffer
	putStr(&args, exportPath)
	code, r := c.call(100005, 3, uint32(nfs.MountProcMount), args.Bytes())
	if code != nfs.ResponseCodeSuccess {
		c.t.Fatalf("MOUNT accept code: %v", code)
	}
	st := nfs.MountStatus(getU32(c.t, r))
	if st != nfs.MountStatusOk {
		return st, nil
	}
	fh := getBytes(c.t, r)
	return st, fh
}

func (c *rpcClient) lookup(dirFH []byte, name string) (nfs.NFSStatus, []byte, nfs.FileAttribute) {
	c.t.Helper()
	var args bytes.Buffer
	putBytes(&args, dirFH)
	putStr(&args, name)
	_, r := c.call(100003, 3, uint32(nfs.NFSProcedureLookup), args.Bytes())
	st := nfs.NFSStatus(getU32(c.t, r))
	if st != nfs.NFSStatusOk {
		return st, nil, nfs.FileAttribute{}
	}
	fh := getBytes(c.t, r)
	attr := getPostOpAttr(c.t, r)
	return st, fh, attr
}

func (c *rpcClient) getAttr(fh []byte) (nfs.NFSStatus, nfs.FileAttribute) {
	c.t.Helper()
	var args bytes.Buffer
	putBytes(&args, fh)
	_, r := c.call(100003, 3, uint32(nfs.NFSProcedureGetAttr), args.Bytes())
	st := nfs.NFSStatus(getU32(c.t, r))
	if st != nfs.NFSStatusOk {
		return st, nfs.FileAttribute{}
	}
	return st, getFattr3(c.t, r)
}

func (c *rpcClient) create(dirFH []byte, name string, mode uint32, how uint32, verf [8]byte) (nfs.NFSStatus, []byte) {
	c.t.Helper()
	var args bytes.Buffer
	putBytes(&args, dirFH)
	putStr(&args, name)
	putU32(&args, how)
	if how == 2 {
		_, _ = args.Write(verf[:])
	} else {
		encodeSattrMode(&args, true, mode)
	}
	_, r := c.call(100003, 3, uint32(nfs.NFSProcedureCreate), args.Bytes())
	st := nfs.NFSStatus(getU32(c.t, r))
	if st != nfs.NFSStatusOk {
		return st, nil
	}
	hasFH := getU32(c.t, r)
	var fh []byte
	if hasFH == 1 {
		fh = getBytes(c.t, r)
	}
	return st, fh
}

func (c *rpcClient) write(fh []byte, offset uint64, data []byte, stab nfsserver.WriteStability) (nfs.NFSStatus, uint32, nfsserver.WriteStability, [8]byte) {
	c.t.Helper()
	var args bytes.Buffer
	putBytes(&args, fh)
	putU64(&args, offset)
	putU32(&args, uint32(len(data)))
	putU32(&args, uint32(stab))
	putBytes(&args, data)
	_, r := c.call(100003, 3, uint32(nfs.NFSProcedureWrite), args.Bytes())
	st := nfs.NFSStatus(getU32(c.t, r))
	var verf [8]byte
	if st != nfs.NFSStatusOk {
		return st, 0, 0, verf
	}
	skipWcc(c.t, r)
	n := getU32(c.t, r)
	committed := nfsserver.WriteStability(getU32(c.t, r))
	_, _ = io.ReadFull(r, verf[:])
	return st, n, committed, verf
}

func (c *rpcClient) commit(fh []byte, offset uint64, count uint32) (nfs.NFSStatus, [8]byte) {
	c.t.Helper()
	var args bytes.Buffer
	putBytes(&args, fh)
	putU64(&args, offset)
	putU32(&args, count)
	_, r := c.call(100003, 3, uint32(nfs.NFSProcedureCommit), args.Bytes())
	st := nfs.NFSStatus(getU32(c.t, r))
	var verf [8]byte
	if st != nfs.NFSStatusOk {
		return st, verf
	}
	skipWcc(c.t, r)
	_, _ = io.ReadFull(r, verf[:])
	return st, verf
}

func (c *rpcClient) read(fh []byte, offset uint64, count uint32) (nfs.NFSStatus, []byte, bool) {
	c.t.Helper()
	var args bytes.Buffer
	putBytes(&args, fh)
	putU64(&args, offset)
	putU32(&args, count)
	_, r := c.call(100003, 3, uint32(nfs.NFSProcedureRead), args.Bytes())
	st := nfs.NFSStatus(getU32(c.t, r))
	if st != nfs.NFSStatusOk {
		return st, nil, false
	}
	_ = getPostOpAttr(c.t, r)
	_ = getU32(c.t, r) // count
	eof := getU32(c.t, r) == 1
	data := getBytes(c.t, r)
	return st, data, eof
}

func putU32(w *bytes.Buffer, v uint32) {
	var b [4]byte
	binary.BigEndian.PutUint32(b[:], v)
	_, _ = w.Write(b[:])
}

func putU64(w *bytes.Buffer, v uint64) {
	var b [8]byte
	binary.BigEndian.PutUint64(b[:], v)
	_, _ = w.Write(b[:])
}

func putBytes(w *bytes.Buffer, data []byte) {
	putU32(w, uint32(len(data)))
	_, _ = w.Write(data)
	if pad := (4 - (len(data) & 3)) & 3; pad > 0 {
		var z [4]byte
		_, _ = w.Write(z[:pad])
	}
}

func putStr(w *bytes.Buffer, s string) { putBytes(w, []byte(s)) }

func encodeSattrMode(w *bytes.Buffer, hasMode bool, mode uint32) {
	if hasMode {
		putU32(w, 1)
		putU32(w, mode)
	} else {
		putU32(w, 0)
	}
	for range 5 {
		putU32(w, 0)
	}
}
