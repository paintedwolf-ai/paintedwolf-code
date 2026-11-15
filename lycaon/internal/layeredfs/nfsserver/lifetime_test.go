package nfsserver_test

import (
	"bytes"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"

	"github.com/lycaon/lycaon/internal/layeredfs"
	"github.com/lycaon/lycaon/internal/layeredfs/nfsserver"
	nfs "github.com/willscott/go-nfs"
)

const (
	procNFS = uint32(100003)
	verNFS  = uint32(3)
)

type limitedEnv struct {
	core  *layeredfs.Core
	srv   *nfsserver.Server
	trunk string
	cli   *rpcClient
	root  []byte
	cap   nfsserver.ExportCapability
}

func newLimitedEnv(t *testing.T, cfg nfsserver.Config, hooks *layeredfs.FaultHooks, seed func(trunk string)) *limitedEnv {
	t.Helper()
	tmp := t.TempDir()
	trunk := filepath.Join(tmp, "trunk")
	if err := os.MkdirAll(trunk, 0o755); err != nil {
		t.Fatalf("mkdir trunk: %v", err)
	}
	if seed != nil {
		seed(trunk)
	}
	core, err := layeredfs.New(layeredfs.Config{
		ViewID: "v", BackingDir: filepath.Join(tmp, "backing"), FaultHooks: hooks,
		Roots: []layeredfs.RootSpec{{ID: nfsserver.ProjectRoot, Label: "project", TrunkPath: trunk, IsPrimary: true}},
	})
	if err != nil {
		t.Fatalf("layeredfs.New: %v", err)
	}
	t.Cleanup(func() { _ = core.ReleaseView() })
	uid, gid := uint32(os.Geteuid()), uint32(os.Getegid())
	cfg.ListenAddr, cfg.ExpectedUID, cfg.ExpectedGID = "127.0.0.1:0", uid, gid
	srv, err := nfsserver.New(core, cfg)
	if err != nil {
		t.Fatalf("nfsserver.New: %v", err)
	}
	t.Cleanup(func() { _ = srv.Close() })
	capCfg, err := srv.RegisterExport("v", nfsserver.ProjectRoot, "")
	if err != nil {
		t.Fatalf("RegisterExport: %v", err)
	}
	cli := dialRPC(t, srv.Addr(), uid, gid)
	st, root := cli.mount(capCfg.ExportPath)
	if st != nfs.MountStatusOk {
		t.Fatalf("mount: %v", st)
	}
	return &limitedEnv{core: core, srv: srv, trunk: trunk, cli: cli, root: root, cap: capCfg}
}

func seedFiles(t *testing.T, n int) func(string) {
	return func(trunk string) {
		for i := range n {
			if err := os.WriteFile(filepath.Join(trunk, fmt.Sprintf("f%04d", i)), []byte(fmt.Sprintf("content-%d", i)), 0o644); err != nil {
				t.Fatalf("seed: %v", err)
			}
		}
	}
}

func (c *rpcClient) status(proc nfs.NFSProcedure, args []byte) nfs.NFSStatus {
	c.t.Helper()
	code, r := c.call(procNFS, verNFS, uint32(proc), args)
	if code != nfs.ResponseCodeSuccess {
		c.t.Fatalf("%v accept code: %v", proc, code)
	}
	return nfs.NFSStatus(getU32(c.t, r))
}

func (c *rpcClient) rename(fromDir []byte, from string, toDir []byte, to string) nfs.NFSStatus {
	c.t.Helper()
	var a bytes.Buffer
	putBytes(&a, fromDir)
	putStr(&a, from)
	putBytes(&a, toDir)
	putStr(&a, to)
	return c.status(nfs.NFSProcedureRename, a.Bytes())
}

func (c *rpcClient) remove(dir []byte, name string) nfs.NFSStatus {
	c.t.Helper()
	var a bytes.Buffer
	putBytes(&a, dir)
	putStr(&a, name)
	return c.status(nfs.NFSProcedureRemove, a.Bytes())
}

func (c *rpcClient) mkdir(dir []byte, name string) (nfs.NFSStatus, []byte) {
	c.t.Helper()
	var a bytes.Buffer
	putBytes(&a, dir)
	putStr(&a, name)
	encodeSattrMode(&a, true, 0o755)
	code, r := c.call(procNFS, verNFS, uint32(nfs.NFSProcedureMkDir), a.Bytes())
	if code != nfs.ResponseCodeSuccess {
		c.t.Fatalf("MKDIR accept code: %v", code)
	}
	st := nfs.NFSStatus(getU32(c.t, r))
	if st != nfs.NFSStatusOk {
		return st, nil
	}
	getU32(c.t, r)
	return st, getBytes(c.t, r)
}

// setattr sends SETATTR with the given optional mode and size.
func (c *rpcClient) setattr(fh []byte, mode *uint32, size *uint64) nfs.NFSStatus {
	c.t.Helper()
	var a bytes.Buffer
	putBytes(&a, fh)
	if mode != nil {
		putU32(&a, 1)
		putU32(&a, *mode)
	} else {
		putU32(&a, 0)
	}
	putU32(&a, 0)
	putU32(&a, 0)
	if size != nil {
		putU32(&a, 1)
		putU64(&a, *size)
	} else {
		putU32(&a, 0)
	}
	putU32(&a, 0)
	putU32(&a, 0)
	putU32(&a, 0)
	return c.status(nfs.NFSProcedureSetAttr, a.Bytes())
}

type dirListing struct {
	names  []string
	ids    map[string]uint64
	verf   [8]byte
	cookie uint64
	eof    bool
}

// readdir pages READDIR and returns status plus entries.
func (c *rpcClient) readdir(dir []byte, cookie uint64, verf [8]byte, count uint32) (nfs.NFSStatus, dirListing) {
	c.t.Helper()
	var a bytes.Buffer
	putBytes(&a, dir)
	putU64(&a, cookie)
	_, _ = a.Write(verf[:])
	putU32(&a, count)
	code, r := c.call(procNFS, verNFS, uint32(nfs.NFSProcedureReadDir), a.Bytes())
	if code != nfs.ResponseCodeSuccess {
		c.t.Fatalf("READDIR accept code: %v", code)
	}
	st := nfs.NFSStatus(getU32(c.t, r))
	out := dirListing{ids: map[string]uint64{}}
	if getU32(c.t, r) == 1 {
		discardFattr(c.t, r)
	}
	if st != nfs.NFSStatusOk {
		return st, out
	}
	_, _ = io.ReadFull(r, out.verf[:])
	for getU32(c.t, r) == 1 {
		id := getU64(c.t, r)
		name := string(getBytes(c.t, r))
		out.cookie = getU64(c.t, r)
		out.names = append(out.names, name)
		out.ids[name] = id
	}
	out.eof = getU32(c.t, r) == 1
	return st, out
}

func discardFattr(t *testing.T, r *bytes.Reader) {
	t.Helper()
	if _, err := r.Seek(84, io.SeekCurrent); err != nil {
		t.Fatalf("skip fattr3: %v", err)
	}
}

// TestHandleSurvivesLeavingMemory is the FORGET question for NFS: the client never says it dropped
// a handle, so a handle the table let go must still answer for the same object.
func TestHandleSurvivesLeavingMemory(t *testing.T) {
	env := newLimitedEnv(t, nfsserver.Config{MaxHandles: 8}, nil, seedFiles(t, 40))
	cli := env.cli

	st, first, _ := cli.lookup(env.root, "f0000")
	if st != nfs.NFSStatusOk {
		t.Fatalf("lookup: %v", st)
	}
	st, written, _ := cli.lookup(env.root, "f0001")
	if st != nfs.NFSStatusOk {
		t.Fatalf("lookup: %v", st)
	}
	if st, _, _, _ := cli.write(written, 0, []byte("private"), nfsserver.FileSync); st != nfs.NFSStatusOk {
		t.Fatalf("write: %v", st)
	}
	st, sub := cli.mkdir(env.root, "subdir")
	if st != nfs.NFSStatusOk {
		t.Fatalf("mkdir: %v", st)
	}
	for i := range 40 {
		if st, _, _ := cli.lookup(env.root, fmt.Sprintf("f%04d", i)); st != nfs.NFSStatusOk {
			t.Fatalf("walk: %v", st)
		}
	}
	// Count: at most the limit of non-directory records plus root and subdir.
	if got := env.srv.HandleCount(); got > 8+2 {
		t.Fatalf("handle table holds %d records, limit is 8 plus 2 directories", got)
	}
	for i := range 30 {
		cli.lookup(env.root, fmt.Sprintf("f%04d", 39-i))
	}
	if st, data, _ := cli.read(first, 0, 64); st != nfs.NFSStatusOk || string(data) != "content-0" {
		t.Fatalf("evicted lower handle: st=%v data=%q", st, data)
	}
	if st, data, _ := cli.read(written, 0, 64); st != nfs.NFSStatusOk || string(data) != "private-1" {
		t.Fatalf("evicted private handle: st=%v data=%q", st, data)
	}
	if st, _ := cli.getAttr(sub); st != nfs.NFSStatusOk {
		t.Fatalf("directory handle: %v", st)
	}
	// Same identity gives the same handle bytes, evicted or not.
	if st, again, _ := cli.lookup(env.root, "f0000"); st != nfs.NFSStatusOk || !bytes.Equal(again, first) {
		t.Fatalf("handle changed across eviction: st=%v", st)
	}

	// A renamed-then-evicted object is found at its new name; a removed one is stale.
	if st := cli.rename(env.root, "f0001", env.root, "renamed"); st != nfs.NFSStatusOk {
		t.Fatalf("rename: %v", st)
	}
	for i := range 30 {
		cli.lookup(env.root, fmt.Sprintf("f%04d", i+5))
	}
	if st, data, _ := cli.read(written, 0, 64); st != nfs.NFSStatusOk || string(data) != "private-1" {
		t.Fatalf("renamed evicted handle: st=%v data=%q", st, data)
	}
	if err := env.core.Unlink(layeredfs.QualifiedPath{RootID: nfsserver.ProjectRoot, Rel: "f0000"}, false); err != nil {
		t.Fatalf("unlink: %v", err)
	}
	for i := range 30 {
		cli.lookup(env.root, fmt.Sprintf("f%04d", i+5))
	}
	if st, _ := cli.getAttr(first); st != nfs.NFSStatusStale {
		t.Fatalf("removed object's handle: %v", st)
	}
}

// TestRemovingFilesCostsNoPerFileState covers rm -rf: no copy-up to delete, bounded held objects,
// and a table full of removed objects cannot wedge eviction under its lock.
func TestRemovingFilesCostsNoPerFileState(t *testing.T) {
	env := newLimitedEnv(t, nfsserver.Config{MaxHandles: 8}, nil, seedFiles(t, 200))
	cli := env.cli
	for i := range 200 {
		name := fmt.Sprintf("f%04d", i)
		if st, _, _ := cli.lookup(env.root, name); st != nfs.NFSStatusOk {
			t.Fatalf("lookup %s: %v", name, st)
		}
		if st := cli.remove(env.root, name); st != nfs.NFSStatusOk {
			t.Fatalf("remove %s: %v", name, st)
		}
	}
	if got := env.core.Stats().PhysicalCopyUps; got != 0 {
		t.Fatalf("removing untouched files copied up %d of them", got)
	}
	if got := env.srv.HandleCount(); got > 8+64+1 {
		t.Fatalf("handle table holds %d records after removing every file", got)
	}
	// The table keeps accepting new objects.
	for i := range 50 {
		if st, _ := cli.create(env.root, fmt.Sprintf("n%d", i), 0o644, 0, [8]byte{}); st != nfs.NFSStatusOk {
			t.Fatalf("create after removals: %v", st)
		}
	}
}

// TestSetattrOnRemovedObjectReachesThatObject is F8 over the protocol: SETATTR on a removed
// object changes that object, not whatever later takes its name.
func TestSetattrOnRemovedObjectReachesThatObject(t *testing.T) {
	env := newLimitedEnv(t, nfsserver.Config{}, nil, seedFiles(t, 1))
	cli := env.cli
	st, fh := cli.create(env.root, "gone", 0o644, 0, [8]byte{})
	if st != nfs.NFSStatusOk {
		t.Fatalf("create: %v", st)
	}
	if st, _, _, _ := cli.write(fh, 0, []byte("0123456789"), nfsserver.FileSync); st != nfs.NFSStatusOk {
		t.Fatalf("write: %v", st)
	}
	if st := cli.remove(env.root, "gone"); st != nfs.NFSStatusOk {
		t.Fatalf("remove: %v", st)
	}
	st, replacement := cli.create(env.root, "gone", 0o644, 0, [8]byte{})
	if st != nfs.NFSStatusOk {
		t.Fatalf("recreate: %v", st)
	}
	mode, size := uint32(0o600), uint64(4)
	if st := cli.setattr(fh, &mode, &size); st != nfs.NFSStatusOk {
		t.Fatalf("setattr removed: %v", st)
	}
	if st, attr := cli.getAttr(fh); st != nfs.NFSStatusOk || attr.Filesize != 4 || attr.FileMode&0o777 != 0o600 {
		t.Fatalf("removed object attrs: st=%v size=%d mode=%o", st, attr.Filesize, attr.FileMode)
	}
	if st, attr := cli.getAttr(replacement); st != nfs.NFSStatusOk || attr.Filesize != 0 || attr.FileMode&0o777 != 0o644 {
		t.Fatalf("replacement touched: st=%v size=%d mode=%o", st, attr.Filesize, attr.FileMode)
	}

	// An untouched lower object has no private identity to change.
	st, lower, _ := cli.lookup(env.root, "f0000")
	if st != nfs.NFSStatusOk {
		t.Fatalf("lookup lower: %v", st)
	}
	if st := cli.remove(env.root, "f0000"); st != nfs.NFSStatusOk {
		t.Fatalf("remove lower: %v", st)
	}
	if st, data, _ := cli.read(lower, 0, 64); st != nfs.NFSStatusOk || string(data) != "content-0" {
		t.Fatalf("held lower object: st=%v data=%q", st, data)
	}
	if st := cli.setattr(lower, &mode, nil); st != nfs.NFSStatusStale {
		t.Fatalf("setattr on removed lower object: %v", st)
	}
}

func TestPendingBytesFollowTheirObject(t *testing.T) {
	env := newLimitedEnv(t, nfsserver.Config{}, nil, nil)
	cli := env.cli
	createWith := func(dir []byte, name, content string, stability nfsserver.WriteStability) []byte {
		t.Helper()
		st, fh := cli.create(dir, name, 0o644, 0, [8]byte{})
		if st != nfs.NFSStatusOk {
			t.Fatalf("create %s: %v", name, st)
		}
		if st, _, _, _ := cli.write(fh, 0, []byte(content), stability); st != nfs.NFSStatusOk {
			t.Fatalf("write %s: %v", name, st)
		}
		return fh
	}
	read := func(rel string) string {
		t.Helper()
		data, err := env.core.ReadFile(layeredfs.QualifiedPath{RootID: nfsserver.ProjectRoot, Rel: rel})
		if err != nil {
			t.Fatalf("read %s: %v", rel, err)
		}
		return string(data)
	}

	// A replaced object's unflushed bytes must never land on its replacement.
	createWith(env.root, "dst", "stale-bytes-from-old-object", nfsserver.Unstable)
	createWith(env.root, "src", "new", nfsserver.FileSync)
	if st := cli.rename(env.root, "src", env.root, "dst"); st != nfs.NFSStatusOk {
		t.Fatalf("rename over: %v", st)
	}
	if err := env.srv.FlushForCheckpoint(t.Context(), "v"); err != nil {
		t.Fatalf("flush: %v", err)
	}
	if got := read("dst"); got != "new" {
		t.Fatalf("replacement corrupted by old object's bytes: %q", got)
	}

	// A file below a renamed directory flushes to where it now lives.
	st, dir := cli.mkdir(env.root, "d1")
	if st != nfs.NFSStatusOk {
		t.Fatalf("mkdir: %v", st)
	}
	createWith(dir, "inner", "pending", nfsserver.Unstable)
	if st := cli.rename(env.root, "d1", env.root, "d2"); st != nfs.NFSStatusOk {
		t.Fatalf("rename dir: %v", st)
	}
	if err := env.srv.FlushForCheckpoint(t.Context(), "v"); err != nil {
		t.Fatalf("flush after dir rename: %v", err)
	}
	if got := read("d2/inner"); got != "pending" {
		t.Fatalf("pending bytes under renamed dir: %q", got)
	}

	// Truncate clips pending bytes rather than discarding them.
	fh := createWith(env.root, "trunc", "0123456789", nfsserver.Unstable)
	size := uint64(4)
	if st := cli.setattr(fh, nil, &size); st != nfs.NFSStatusOk {
		t.Fatalf("truncate: %v", st)
	}
	if st, _ := cli.commit(fh, 0, 0); st != nfs.NFSStatusOk {
		t.Fatalf("commit: %v", st)
	}
	if got := read("trunc"); got != "0123" {
		t.Fatalf("after truncate: %q", got)
	}
	grown := uint64(6)
	createWith(env.root, "grow", "abcd", nfsserver.Unstable)
	st, growFH, _ := cli.lookup(env.root, "grow")
	if st != nfs.NFSStatusOk {
		t.Fatalf("lookup grow: %v", st)
	}
	if st := cli.setattr(growFH, nil, &grown); st != nfs.NFSStatusOk {
		t.Fatalf("grow: %v", st)
	}
	if got := read("grow"); got != "abcd\x00\x00" {
		t.Fatalf("after grow: %q", got)
	}
}

// TestCommitCostDoesNotScaleWithPrivateContent counts file hashes: COMMIT must not rehash and
// republish every private file, or a thousand-file install is quadratic and times out the client.
func TestCommitCostDoesNotScaleWithPrivateContent(t *testing.T) {
	var hashes atomic.Int64
	hooks := &layeredfs.FaultHooks{WorkingFileHashReader: func(_ layeredfs.QualifiedPath, r io.Reader) (io.Reader, error) {
		hashes.Add(1)
		return r, nil
	}}
	env := newLimitedEnv(t, nfsserver.Config{}, hooks, nil)
	for i := range 20 {
		st, fh := env.cli.create(env.root, fmt.Sprintf("n%d", i), 0o644, 0, [8]byte{})
		if st != nfs.NFSStatusOk {
			t.Fatalf("create: %v", st)
		}
		if st, _, _, _ := env.cli.write(fh, 0, []byte("x"), nfsserver.Unstable); st != nfs.NFSStatusOk {
			t.Fatalf("write: %v", st)
		}
		if st, _ := env.cli.commit(fh, 0, 1); st != nfs.NFSStatusOk {
			t.Fatalf("commit: %v", st)
		}
	}
	if got := hashes.Load(); got != 0 {
		t.Fatalf("COMMIT hashed private files %d times", got)
	}
	if err := env.srv.FlushForCheckpoint(t.Context(), "v"); err != nil {
		t.Fatalf("flush: %v", err)
	}
	if got := hashes.Load(); got != 20 {
		t.Fatalf("barrier flush hashed %d files, want 20", got)
	}
}
