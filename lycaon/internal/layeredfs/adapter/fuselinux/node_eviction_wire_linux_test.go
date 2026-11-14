//go:build linux

package fuselinux

import (
	"encoding/binary"
	"syscall"
	"testing"

	"github.com/lycaon/lycaon/internal/layeredfs"
)

func (w *wireServer) nodeCount() int {
	w.t.Helper()
	n, _, _ := w.srv.NodeTableCountsForTest()
	return n
}

// forget sends FUSE_FORGET, which has no reply. A following replied request proves the reader
// has processed it, because the reader handles requests in order.
func (w *wireServer) forget(nodeID, count uint64) {
	w.t.Helper()
	w.sendWith(w.nextUnique(), fuseOpForget, nodeID, u64(count))
	w.wantErrno("barrier after forget", w.call(fuseOpGetlk, fuseRootID, lockBody(99, 0, 0, syscall.F_RDLCK)), 0)
}

func (w *wireServer) batchForget(pairs ...uint64) {
	w.t.Helper()
	body := make([]byte, 8)
	binary.LittleEndian.PutUint32(body[0:4], uint32(len(pairs)/2))
	for _, v := range pairs {
		body = binary.LittleEndian.AppendUint64(body, v)
	}
	w.sendWith(w.nextUnique(), fuseOpBatchForget, 0, body)
	w.wantErrno("barrier after batch forget", w.call(fuseOpGetlk, fuseRootID, lockBody(99, 0, 0, syscall.F_RDLCK)), 0)
}

func entryNode(w *wireServer, what string, r wireReply) uint64 {
	w.t.Helper()
	w.wantErrno(what, r, 0)
	return binary.LittleEndian.Uint64(r.payload[0:8])
}

func entryIno(r wireReply) uint64 { return binary.LittleEndian.Uint64(r.payload[40:48]) }

// TestEveryEntryReplyCountsOneKernelReference is table-driven over each reply carrying an entry:
// after one such reply, one forget evicts, and one less keeps the node.
func TestEveryEntryReplyCountsOneKernelReference(t *testing.T) {
	cases := []struct {
		name  string
		reply func(w *wireServer) wireReply
	}{
		{"lookup", func(w *wireServer) wireReply {
			w.seed("subject", "x")
			return w.call(fuseOpLookup, fuseRootID, cName("subject"))
		}},
		{"create", func(w *wireServer) wireReply {
			return w.call(fuseOpCreate, fuseRootID, append(make([]byte, 16), cName("subject")...))
		}},
		{"mkdir", func(w *wireServer) wireReply {
			return w.call(fuseOpMkdir, fuseRootID, append(make([]byte, 8), cName("subject")...))
		}},
		{"mknod", func(w *wireServer) wireReply {
			return w.call(fuseOpMknod, fuseRootID, append(make([]byte, 16), cName("subject")...))
		}},
		{"symlink", func(w *wireServer) wireReply {
			return w.call(fuseOpSymlink, fuseRootID, cName("subject", "target"))
		}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			w := newWireServer(t, ServerConfig{})
			base := w.nodeCount()
			reply := c.reply(w)
			node := entryNode(w, c.name, reply)
			if c.name == "create" {
				// CREATE also opens a descriptor, which holds the node until released.
				fh := binary.LittleEndian.Uint64(reply.payload[128:136])
				w.wantErrno("release", w.call(fuseOpRelease, node, append(u64(fh), make([]byte, 16)...)), 0)
			}
			if got := w.nodeCount(); got != base+1 {
				t.Fatalf("nodes after %s = %d, want %d", c.name, got, base+1)
			}
			again := entryNode(w, "second lookup", w.call(fuseOpLookup, fuseRootID, cName("subject")))
			if again != node {
				t.Fatalf("second lookup returned node %d, want %d", again, node)
			}
			w.forget(node, 1)
			if got := w.nodeCount(); got != base+1 {
				t.Fatalf("one of two references dropped, nodes = %d, want %d", got, base+1)
			}
			w.forget(node, 1)
			if got := w.nodeCount(); got != base {
				t.Fatalf("all references dropped, nodes = %d, want %d", got, base)
			}
			w.wantErrno("evicted node", w.call(fuseOpGetattr, node, make([]byte, 16)), syscall.ENOENT)
		})
	}
}

func TestBatchForgetEvictsEachNodeAndIDsAreNeverReused(t *testing.T) {
	w := newWireServer(t, ServerConfig{})
	w.seed("a", "a")
	w.seed("b", "b")
	w.seed("c", "c")
	a := w.lookup("a")
	b := w.lookup("b")
	c := w.lookup("c")
	w.lookup("c")
	base := w.nodeCount()
	w.batchForget(a, 1, b, 1, c, 1, 1<<40, 7, fuseRootID, 1000)
	if got := w.nodeCount(); got != base-2 {
		t.Fatalf("nodes after batch = %d, want %d (c keeps one reference, root and unknown IDs ignored)", got, base-2)
	}
	if got := w.call(fuseOpGetattr, fuseRootID, make([]byte, 16)); got.errno != 0 {
		t.Fatalf("root was evicted: %d", got.errno)
	}

	reborn := w.lookup("a")
	if reborn <= c {
		t.Fatalf("evicted path got node %d, not above every earlier ID (%d)", reborn, c)
	}
}

func TestStableInodeSurvivesEvictionAndCopyUp(t *testing.T) {
	w := newWireServer(t, ServerConfig{})
	w.seed("f", "lower")
	lookup := func() (uint64, uint64) {
		r := w.call(fuseOpLookup, fuseRootID, cName("f"))
		return entryNode(w, "lookup", r), entryIno(r)
	}
	node, ino := lookup()
	w.forget(node, 1)
	node2, ino2 := lookup()
	if node2 == node || ino2 != ino {
		t.Fatalf("across eviction: node %d->%d (must change), ino %d->%d (must not)", node, node2, ino, ino2)
	}

	fh := w.open(node2, syscall.O_RDWR)
	w.wantErrno("release", w.call(fuseOpRelease, node2, append(u64(fh), make([]byte, 16)...)), 0)
	w.forget(node2, 1)
	node3, ino3 := lookup()
	if ino3 != ino {
		t.Fatalf("copy-up then eviction changed ino %d->%d", ino, ino3)
	}
	w.forget(node3, 1)
	if _, _, private := w.srv.NodeTableCountsForTest(); private != 0 {
		t.Fatalf("private map holds %d evicted nodes", private)
	}
}

func TestEvictionWaitsForOpenHandlesAndCoversHardLinkPaths(t *testing.T) {
	w := newWireServer(t, ServerConfig{})
	w.seed("a", "content")
	base := w.nodeCount()
	node := w.lookup("a")
	fh := w.open(node, syscall.O_RDWR)

	// FORGET may precede the descriptor's asynchronous release.
	w.forget(node, 1)
	if got := w.nodeCount(); got != base+1 {
		t.Fatalf("node with an open descriptor evicted: nodes = %d", got)
	}
	w.wantErrno("getattr while held", w.call(fuseOpGetattr, node, append(make([]byte, 8), u64(fh)...)), 0)
	w.wantErrno("release", w.call(fuseOpRelease, node, append(u64(fh), make([]byte, 16)...)), 0)
	if got := w.nodeCount(); got != base {
		t.Fatalf("release did not settle the unreferenced node: nodes = %d", got)
	}

	node = w.lookup("a")
	fh = w.open(node, syscall.O_RDWR)
	w.wantErrno("release", w.call(fuseOpRelease, node, append(u64(fh), make([]byte, 16)...)), 0)
	link := entryNode(w, "link", w.call(fuseOpLink, fuseRootID, append(u64(node), cName("b")...)))
	if link != node {
		t.Fatalf("hard link got node %d, want shared node %d", link, node)
	}
	if got := w.nodeCount(); got != base+1 {
		t.Fatalf("hard link added a node: nodes = %d", got)
	}
	w.forget(node, 1)
	if id, ok := w.srv.NodeIDForTest("b"); !ok || id != node {
		t.Fatalf("path set lost with a reference remaining: id=%d ok=%v", id, ok)
	}
	w.forget(node, 1)
	for _, rel := range []string{"a", "b"} {
		if _, ok := w.srv.NodeIDForTest(rel); ok {
			t.Fatalf("path %s survives eviction", rel)
		}
	}
	first := entryIno(w.call(fuseOpLookup, fuseRootID, cName("a")))
	second := entryIno(w.call(fuseOpLookup, fuseRootID, cName("b")))
	if first != second {
		t.Fatalf("hard links differ after eviction: ino %d and %d", first, second)
	}
}

func TestListingCreatesNoNodesAndStreamStateSettlesOnRelease(t *testing.T) {
	w := newWireServer(t, ServerConfig{})
	for _, name := range []string{"a", "b", "c"} {
		w.seed(name, "x")
	}
	base := w.nodeCount()
	r := w.call(fuseOpOpendir, fuseRootID, make([]byte, 8))
	w.wantErrno("opendir", r, 0)
	fh := binary.LittleEndian.Uint64(r.payload[0:8])
	body := make([]byte, 40)
	binary.LittleEndian.PutUint64(body[0:8], fh)
	binary.LittleEndian.PutUint32(body[16:20], 4096)
	listing := w.call(fuseOpReaddir, fuseRootID, body)
	w.wantErrno("readdir", listing, 0)
	if got := w.nodeCount(); got != base {
		t.Fatalf("listing created %d nodes", got-base)
	}
	// The inode a listing reports is the one a later lookup reports.
	listed := map[string]uint64{}
	for p := listing.payload; len(p) > 0; {
		nameLen := int(binary.LittleEndian.Uint32(p[16:20]))
		listed[string(p[24:24+nameLen])] = binary.LittleEndian.Uint64(p[0:8])
		p = p[(24+nameLen+7)&^7:]
	}
	if got, want := listed["a"], entryIno(w.call(fuseOpLookup, fuseRootID, cName("a"))); got != want {
		t.Fatalf("d_ino %d differs from st_ino %d", got, want)
	}
	w.srv.nodes.mu.RLock()
	snapshots := len(w.srv.nodes.dirSnapshots)
	w.srv.nodes.mu.RUnlock()
	if snapshots != 1 {
		t.Fatalf("snapshots while open = %d", snapshots)
	}
	w.wantErrno("releasedir", w.call(fuseOpReleasedir, fuseRootID, append(u64(fh), make([]byte, 16)...)), 0)
	w.srv.nodes.mu.RLock()
	snapshots = len(w.srv.nodes.dirSnapshots)
	w.srv.nodes.mu.RUnlock()
	if snapshots != 0 {
		t.Fatalf("snapshots after release = %d", snapshots)
	}
}

func TestDirtyPathsSettleAfterASuccessfulBarrierFlush(t *testing.T) {
	w := newWireServer(t, ServerConfig{})
	w.wantErrno("mkdir", w.call(fuseOpMkdir, fuseRootID, append(make([]byte, 8), cName("d")...)), 0)
	if len(w.srv.nodes.dirtyCandidatePaths()) == 0 {
		t.Fatal("mutation recorded no dirty path")
	}
	injected := error(syscall.EIO)
	w.syncErr.Store(&injected)
	if _, err := w.srv.BeginBarrierDrain(t.Context(), "wire"); err == nil {
		t.Fatal("expected failing journal flush to fail the drain")
	}
	if len(w.srv.nodes.dirtyCandidatePaths()) == 0 {
		t.Fatal("a failed flush must keep dirty paths")
	}
	w.syncErr.Store(nil)
	release, err := w.srv.BeginBarrierDrain(t.Context(), "wire")
	if err != nil {
		t.Fatalf("BeginBarrierDrain: %v", err)
	}
	defer release()
	if got := w.srv.nodes.dirtyCandidatePaths(); len(got) != 0 {
		t.Fatalf("dirty paths after a successful flush: %v", got)
	}
	if _, err := w.core.Lstat(layeredfs.QualifiedPath{RootID: "primary", Rel: "d"}); err != nil {
		t.Fatalf("private directory: %v", err)
	}
}
