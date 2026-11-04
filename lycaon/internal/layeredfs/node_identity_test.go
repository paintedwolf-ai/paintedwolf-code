//go:build darwin || linux

package layeredfs

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"syscall"
	"testing"
	"time"

	"github.com/lycaon/lycaon/internal/testutil"
)

func newIdentityCore(t *testing.T, seed map[string]string) (*Core, string) {
	t.Helper()
	trunk := t.TempDir()
	for name, content := range seed {
		testutil.FailErr(t, "seed "+name, os.WriteFile(filepath.Join(trunk, name), []byte(content), 0o644))
	}
	c, err := New(Config{
		ViewID: "view-identity", BackingDir: t.TempDir(),
		Roots: []RootSpec{{ID: "r", Label: "r", TrunkPath: trunk, IsPrimary: true}},
	})
	testutil.FailErr(t, "new core", err)
	t.Cleanup(func() { _ = c.ReleaseView() })
	return c, trunk
}

func rp(rel string) QualifiedPath { return QualifiedPath{RootID: "r", Rel: rel} }

func TestSpecialNodesLiveInTheViewAndAreAbsentFromChanges(t *testing.T) {
	c, _ := newIdentityCore(t, map[string]string{"over.txt": "trunk"})

	fifo, err := c.Mknod(rp("p"), fs.ModeNamedPipe|0o640)
	testutil.FailErr(t, "mknod fifo", err)
	if fifo.Kind != EntryFIFO || fifo.Mode&fs.ModeNamedPipe == 0 || fifo.Mode.Perm() != 0o640 {
		t.Fatalf("fifo attr: %+v", fifo)
	}
	sock, err := c.Mknod(rp("s"), fs.ModeSocket|0o600)
	testutil.FailErr(t, "mknod socket", err)
	if sock.Kind != EntrySocket {
		t.Fatalf("socket attr: %+v", sock)
	}
	if _, err := c.Mknod(rp("p"), fs.ModeNamedPipe|0o640); !errors.Is(err, os.ErrExist) {
		t.Fatalf("mknod over existing: %v", err)
	}
	if _, err := c.Mknod(rp("d"), fs.ModeDevice|0o600); !errors.Is(err, syscall.EPERM) {
		t.Fatalf("device node: %v", err)
	}
	if _, err := c.Mknod(rp("r"), 0o600); !errors.Is(err, syscall.EINVAL) {
		t.Fatalf("regular through mknod: %v", err)
	}
	if _, _, err := c.Open(rp("p"), OpenReadOnly, 0); !errors.Is(err, ErrUnsupportedOperation) {
		t.Fatalf("open fifo: %v", err)
	}
	if _, _, err := c.Open(rp("p"), OpenReadWrite, 0); !errors.Is(err, ErrUnsupportedOperation) {
		t.Fatalf("open fifo for write: %v", err)
	}
	entries, err := c.ReadDir(rp("."))
	testutil.FailErr(t, "readdir", err)
	kinds := map[string]EntryKind{}
	for _, e := range entries {
		kinds[e.Name] = e.Kind
	}
	if kinds["p"] != EntryFIFO || kinds["s"] != EntrySocket {
		t.Fatalf("listing: %v", kinds)
	}
	logical, err := c.EnumerateChanges(true)
	testutil.FailErr(t, "changes", err)
	if len(logical) != 0 {
		t.Fatalf("special nodes in logical changes: %+v", logical)
	}

	// A special node over a trunk file: the trunk file's deletion is the only publishable fact.
	testutil.FailErr(t, "unlink over", c.Unlink(rp("over.txt"), false))
	_, err = c.Mknod(rp("over.txt"), fs.ModeSocket|0o600)
	testutil.FailErr(t, "mknod over deleted", err)
	logical, err = c.EnumerateChanges(true)
	testutil.FailErr(t, "changes after replacement", err)
	if len(logical) != 1 || !logical[0].Deleted || logical[0].Kind != EntryAbsent || logical[0].Base.Kind != EntryRegular {
		t.Fatalf("replacement change: %+v", logical)
	}

	testutil.FailErr(t, "rename socket", c.Rename(rp("s"), rp("moved")))
	if moved, err := c.Lstat(rp("moved")); err != nil || moved.Kind != EntrySocket {
		t.Fatalf("renamed socket: %+v err=%v", moved, err)
	}
	testutil.FailErr(t, "unlink socket", c.Unlink(rp("moved"), false))
	if _, err := c.Lstat(rp("moved")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("after unlink: %v", err)
	}
}

func TestLowerSpecialFilesAreNotListed(t *testing.T) {
	c, trunk := newIdentityCore(t, map[string]string{"plain": "x"})
	testutil.FailErr(t, "mkfifo", syscall.Mkfifo(filepath.Join(trunk, "lowerpipe"), 0o600))
	entries, err := c.ReadDir(rp("."))
	testutil.FailErr(t, "readdir", err)
	if len(entries) != 1 || entries[0].Name != "plain" {
		t.Fatalf("lower special listed: %+v", entries)
	}
	if _, err := c.Lstat(rp("lowerpipe")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("lower special visible: %v", err)
	}
	_, err = c.Mknod(rp("lowerpipe"), fs.ModeNamedPipe|0o600)
	testutil.FailErr(t, "private node over hidden lower special", err)
}

func TestSetAttrNodeReachesObjectsWithNoPathAndNothingElse(t *testing.T) {
	c, _ := newIdentityCore(t, nil)
	_, err := c.WriteFile(rp("a"), []byte("content-a"), 0o644)
	testutil.FailErr(t, "write a", err)
	hid, attr, err := c.Open(rp("a"), OpenReadWrite, 0)
	testutil.FailErr(t, "open a", err)
	testutil.FailErr(t, "unlink a", c.Unlink(rp("a"), false))

	size := int64(3)
	mode := fs.FileMode(0o600)
	got, err := c.SetAttrNode(attr.NodeID, attr.Generation, SetAttrRequest{Size: &size, Mode: &mode})
	testutil.FailErr(t, "setattr unlinked", err)
	if got.Size != 3 || got.Mode.Perm() != 0o600 {
		t.Fatalf("attr: %+v", got)
	}
	st, err := c.StatHandle(hid)
	testutil.FailErr(t, "stat", err)
	if st.Size != 3 || st.Mode.Perm() != 0o600 {
		t.Fatalf("descriptor sees: %+v", st)
	}
	buf := make([]byte, 16)
	n, err := c.ReadHandle(hid, buf, 0)
	if string(buf[:n]) != "con" || (err != nil && n == 0) {
		t.Fatalf("bytes after truncate: %q err=%v", buf[:n], err)
	}
	if _, err := c.SetAttrNode(attr.NodeID, attr.Generation+1, SetAttrRequest{Mode: &mode}); !errors.Is(err, ErrStaleHandle) {
		t.Fatalf("wrong generation: %v", err)
	}
	uid := uint32(0)
	if _, err := c.SetAttrNode(attr.NodeID, attr.Generation, SetAttrRequest{UID: &uid}); !errors.Is(err, syscall.EPERM) {
		t.Fatalf("chown: %v", err)
	}
	testutil.FailErr(t, "close", c.CloseHandle(hid))
	if _, err := c.SetAttrNode(attr.NodeID, attr.Generation, SetAttrRequest{Mode: &mode}); !errors.Is(err, ErrStaleHandle) {
		t.Fatalf("after last close: %v", err)
	}

	// A replaced object keeps its own bytes; the replacement at its old name is untouched.
	_, err = c.WriteFile(rp("old"), []byte("old-content"), 0o644)
	testutil.FailErr(t, "write old", err)
	_, err = c.WriteFile(rp("new"), []byte("new"), 0o644)
	testutil.FailErr(t, "write new", err)
	oldH, oldAttr, err := c.Open(rp("old"), OpenReadWrite, 0)
	testutil.FailErr(t, "open old", err)
	testutil.FailErr(t, "rename over", c.Rename(rp("new"), rp("old")))
	one := int64(1)
	_, err = c.SetAttrNode(oldAttr.NodeID, oldAttr.Generation, SetAttrRequest{Size: &one})
	testutil.FailErr(t, "truncate replaced", err)
	content, err := c.ReadFile(rp("old"))
	testutil.FailErr(t, "read replacement", err)
	if string(content) != "new" {
		t.Fatalf("replacement changed: %q", content)
	}
	testutil.FailErr(t, "close old", c.CloseHandle(oldH))
}

func TestCopyUpKeepsStableIdentityAndIdentityResolves(t *testing.T) {
	c, _ := newIdentityCore(t, map[string]string{"f": "x"})
	before, err := c.Lstat(rp("f"))
	testutil.FailErr(t, "lstat", err)
	entries, err := c.ReadDir(rp("."))
	testutil.FailErr(t, "readdir", err)
	if entries[0].StableID() != before.StableID() {
		t.Fatalf("listing identity %d differs from attr identity %d", entries[0].StableID(), before.StableID())
	}
	if name, ok, err := c.FindChildByIdentity(rp("."), before.StableID()); err != nil || !ok || name != "f" {
		t.Fatalf("find child: %q %v %v", name, ok, err)
	}
	if _, _, ok := c.ResolveIdentity(before.StableID()); ok {
		t.Fatal("an untouched lower object has no private identity")
	}
	_, err = c.WriteFile(rp("f"), []byte("yy"), 0o644)
	testutil.FailErr(t, "write", err)
	after, err := c.Lstat(rp("f"))
	testutil.FailErr(t, "lstat private", err)
	if after.Layer != LayerPrivate || after.StableID() != before.StableID() {
		t.Fatalf("identity moved across copy-up: before=%d after=%d", before.StableID(), after.StableID())
	}
	testutil.FailErr(t, "rename", c.Rename(rp("f"), rp("g")))
	qp, attr, ok := c.ResolveIdentity(before.StableID())
	if !ok || qp.Rel != "g" || attr.Layer != LayerPrivate {
		t.Fatalf("identity after rename: %v %+v %v", qp, attr, ok)
	}
	testutil.FailErr(t, "unlink", c.Unlink(rp("g"), false))
	if _, _, ok := c.ResolveIdentity(before.StableID()); ok {
		t.Fatal("identity of a removed object still resolves")
	}
}

func TestRemovedObjectsLeaveNoNodeState(t *testing.T) {
	c, _ := newIdentityCore(t, nil)
	for i := 0; i < 50; i++ {
		_, err := c.WriteFile(rp("f"), []byte("x"), 0o644)
		testutil.FailErr(t, "write", err)
		testutil.FailErr(t, "unlink", c.Unlink(rp("f"), false))
	}
	c.mu.RLock()
	nodes := len(c.state.nodesByID)
	c.mu.RUnlock()
	if nodes != 0 {
		t.Fatalf("%d nodes retained after removing every object", nodes)
	}
}

func TestTransientNameIsLiveButNotPublished(t *testing.T) {
	c, _ := newIdentityCore(t, nil)
	_, err := c.WriteFile(rp(".nfs.00000001.0001"), []byte("x"), 0o644)
	testutil.FailErr(t, "write", err)
	c.MarkTransientName(rp(".nfs.00000001.0001"))
	if _, err := c.Lstat(rp(".nfs.00000001.0001")); err != nil {
		t.Fatalf("transient path must stay live: %v", err)
	}
	changes, err := c.EnumerateChanges(true)
	testutil.FailErr(t, "changes", err)
	if len(changes) != 0 {
		t.Fatalf("transient path published: %+v", changes)
	}
	testutil.FailErr(t, "rename away", c.Rename(rp(".nfs.00000001.0001"), rp("kept")))
	changes, err = c.EnumerateChanges(true)
	testutil.FailErr(t, "changes after rename", err)
	found := false
	for _, ch := range changes {
		found = found || ch.Path.Rel == "kept"
	}
	if !found {
		t.Fatalf("a real name must publish again: %+v", changes)
	}
}

func TestRootListingsAreRepeatable(t *testing.T) {
	c, _ := newIdentityCore(t, map[string]string{"a": "1", "b": "2"})
	for pass := range 3 {
		entries, err := c.ReadDir(rp("."))
		testutil.FailErr(t, "readdir", err)
		if len(entries) != 2 {
			t.Fatalf("pass %d listed %d entries: %+v", pass, len(entries), entries)
		}
	}
}

func TestLowerDirectoryMtimeAdvancesWithPrivateChanges(t *testing.T) {
	c, trunk := newIdentityCore(t, nil)
	sub := filepath.Join(trunk, "sub")
	testutil.FailErr(t, "mkdir", os.Mkdir(sub, 0o755))
	old := time.Unix(1_000_000_000, 0)
	testutil.FailErr(t, "chtimes", os.Chtimes(sub, old, old))
	before, err := c.Lstat(rp("sub"))
	testutil.FailErr(t, "lstat", err)
	if !before.ModTime.Equal(old) {
		t.Fatalf("lower mtime: %v", before.ModTime)
	}
	_, err = c.WriteFile(rp("sub/new"), []byte("x"), 0o644)
	testutil.FailErr(t, "create in lower dir", err)
	after, err := c.Lstat(rp("sub"))
	testutil.FailErr(t, "lstat after", err)
	if !after.ModTime.After(old) {
		t.Fatalf("directory mtime did not advance: %v", after.ModTime)
	}
	if after.StableID() != before.StableID() {
		t.Fatal("directory identity changed")
	}
}
