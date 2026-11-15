//go:build linux

package fuselinux

import (
	"encoding/binary"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"
)

func validity(seconds, nanos []byte) time.Duration {
	return time.Duration(binary.LittleEndian.Uint64(seconds))*time.Second + time.Duration(binary.LittleEndian.Uint32(nanos))
}

func entryValidity(r wireReply) (entry, attr time.Duration) {
	return validity(r.payload[16:24], r.payload[32:36]), validity(r.payload[24:32], r.payload[36:40])
}

func attrValidity(r wireReply) time.Duration { return validity(r.payload[0:8], r.payload[8:12]) }

func (w *wireServer) seedDir(rel string) {
	w.t.Helper()
	if err := os.MkdirAll(filepath.Join(w.trunk, rel), 0o755); err != nil {
		w.t.Fatalf("seed %s: %v", rel, err)
	}
}

// readdirplusAll lists a directory through READDIRPLUS in replies of size bytes and returns each
// entry's node with the number of requests the listing took.
func (w *wireServer) readdirplusAll(dir uint64, size uint32) (map[string]uint64, int) {
	w.t.Helper()
	r := w.call(fuseOpOpendir, dir, make([]byte, 8))
	w.wantErrno("opendir", r, 0)
	fh := binary.LittleEndian.Uint64(r.payload[0:8])
	defer w.wantErrno("releasedir", w.call(fuseOpReleasedir, dir, append(u64(fh), make([]byte, 16)...)), 0)
	nodes := map[string]uint64{}
	requests := 0
	for offset := uint64(0); ; {
		body := make([]byte, 40)
		binary.LittleEndian.PutUint64(body[0:8], fh)
		binary.LittleEndian.PutUint64(body[8:16], offset)
		binary.LittleEndian.PutUint32(body[16:20], size)
		requests++
		listing := w.call(fuseOpReaddirplus, dir, body)
		w.wantErrno("readdirplus", listing, 0)
		if len(listing.payload) == 0 {
			return nodes, requests
		}
		for p := listing.payload; len(p) > 0; {
			dirent := p[128:]
			nameLen := int(binary.LittleEndian.Uint32(dirent[16:20]))
			nodes[string(dirent[24:24+nameLen])] = binary.LittleEndian.Uint64(p[0:8])
			offset = binary.LittleEndian.Uint64(dirent[8:16])
			p = p[128+((24+nameLen+7)&^7):]
		}
	}
}

func TestRepliesCarryTheCacheValidityAndGitMetadataNone(t *testing.T) {
	w := newWireServer(t, ServerConfig{CacheTTL: DefaultCacheTTL})
	w.seed("a.txt", "a")
	w.seedDir(".git")
	file := w.call(fuseOpLookup, fuseRootID, cName("a.txt"))
	if entry, attr := entryValidity(file); entry != DefaultCacheTTL || attr != DefaultCacheTTL {
		t.Fatalf("lookup validity = %v, %v, want %v", entry, attr, DefaultCacheTTL)
	}
	if got := attrValidity(w.call(fuseOpGetattr, entryNode(w, "lookup a.txt", file), make([]byte, 16))); got != DefaultCacheTTL {
		t.Fatalf("getattr validity = %v, want %v", got, DefaultCacheTTL)
	}
	git := w.call(fuseOpLookup, fuseRootID, cName(".git"))
	if entry, attr := entryValidity(git); entry != 0 || attr != 0 {
		t.Fatalf("Git metadata lookup validity = %v, %v, want none", entry, attr)
	}
	if got := attrValidity(w.call(fuseOpGetattr, entryNode(w, "lookup .git", git), make([]byte, 16))); got != 0 {
		t.Fatalf("Git metadata getattr validity = %v, want none", got)
	}
}

// A listing with a stat per file costs one request per reply batch: every entry arrives with its
// node and attributes, so the kernel has nothing left to look up.
func TestReaddirplusCostsRequestsPerBatchNotPerFile(t *testing.T) {
	w := newWireServer(t, ServerConfig{CacheTTL: DefaultCacheTTL})
	const files = 400
	for i := range files {
		w.seed(fmt.Sprintf("file-%03d", i), "x")
	}
	const replySize = 16 * 1024
	nodes, requests := w.readdirplusAll(fuseRootID, replySize)
	for i := range files {
		if nodes[fmt.Sprintf("file-%03d", i)] == 0 {
			t.Fatalf("file-%03d arrived without a node", i)
		}
	}
	// Each record is the entry, its attributes and a padded name; the last request returns nothing.
	perReply := replySize / (128 + 32)
	if limit := files/perReply + 2; requests > limit {
		t.Fatalf("listing %d files took %d requests, want at most %d", files, requests, limit)
	}
	if got, want := w.nodeCount(), files+1; got != want {
		t.Fatalf("node count = %d, want %d", got, want)
	}
}

// After a walk past the budget the server asks the kernel to drop its oldest dentries, and once the
// kernel forgets them the table is back within the budget.
func TestNodeBudgetReturnsTheTableToItsBoundAfterForgets(t *testing.T) {
	w := newWireServer(t, ServerConfig{CacheTTL: DefaultCacheTTL})
	const budget = 32
	w.srv.nodes.mu.Lock()
	w.srv.nodes.budget = budget
	w.srv.nodes.mu.Unlock()
	var mu sync.Mutex
	var asked []entryRef
	w.srv.invalidateEntry = func(parent uint64, name string) {
		mu.Lock()
		asked = append(asked, entryRef{parent: parent, name: name})
		mu.Unlock()
	}
	const files = 200
	for i := range files {
		w.seed(fmt.Sprintf("walk-%03d", i), "x")
	}
	looked := map[string]uint64{}
	for i := range files {
		name := fmt.Sprintf("walk-%03d", i)
		looked[name] = w.lookup(name)
	}
	if got := w.nodeCount(); got != files+1 {
		t.Fatalf("node count after walk = %d", got)
	}
	// The kernel drops each dentry it is asked to and forgets the node it held once.
	forgotten := map[string]bool{}
	for w.nodeCount() > budget+1 {
		w.await("eviction request", func() bool {
			mu.Lock()
			defer mu.Unlock()
			return len(asked) > 0
		})
		mu.Lock()
		batch := asked
		asked = nil
		mu.Unlock()
		for _, ref := range batch {
			if ref.parent != fuseRootID {
				t.Fatalf("eviction named parent %d", ref.parent)
			}
			if !forgotten[ref.name] {
				forgotten[ref.name] = true
				w.forget(looked[ref.name], 1)
			}
		}
		w.srv.wakeEvictor()
	}
	if got := w.nodeCount(); got > budget+1 {
		t.Fatalf("node count after forgets = %d, want at most %d", got, budget+1)
	}
	// The oldest lookups go first.
	for i := range files - budget {
		if !forgotten[fmt.Sprintf("walk-%03d", i)] {
			t.Fatalf("walk-%03d was kept while newer entries were dropped", i)
		}
	}
}
