package nfsserver

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"github.com/lycaon/lycaon/internal/layeredfs"
	"github.com/lycaon/lycaon/internal/testutil"
)

// A walk larger than the handle table rebuilds records as it goes. Rebuilding must not list the
// directory once per file, which turns a walk quadratic in directory size.
func TestRebuildingEvictedHandlesListsADirectoryOnce(t *testing.T) {
	tmp := t.TempDir()
	trunk, backing := filepath.Join(tmp, "trunk"), filepath.Join(tmp, "backing")
	testutil.FailErr(t, "mkdir", os.MkdirAll(filepath.Join(trunk, "dir"), 0o755))
	testutil.FailErr(t, "mkdir", os.MkdirAll(backing, 0o700))
	const files = 50
	for i := range files {
		testutil.FailErr(t, "seed", os.WriteFile(filepath.Join(trunk, "dir", fmt.Sprintf("f%02d", i)), []byte("x"), 0o644))
	}
	core, err := layeredfs.New(layeredfs.Config{
		ViewID: "v", BackingDir: backing,
		Roots: []layeredfs.RootSpec{{ID: "primary", Label: "primary", TrunkPath: trunk, IsPrimary: true}},
	})
	testutil.FailErr(t, "core", err)
	t.Cleanup(func() { _ = core.ReleaseView() })

	table := newHandleTable([32]byte{1}, 4, newSidecarTable(), func(ViewID) *layeredfs.Core { return core })
	table.RegisterView("v")
	q := func(rel string) layeredfs.QualifiedPath { return layeredfs.QualifiedPath{RootID: "primary", Rel: rel} }
	dirMeta, err := core.Lstat(q("dir"))
	testutil.FailErr(t, "stat dir", err)
	table.Ensure("v", q("dir"), dirMeta, 0, false)

	wires := make([][HandleSize]byte, files)
	for i := range files {
		meta, err := core.LstatAttrs(q(fmt.Sprintf("dir/f%02d", i)))
		testutil.FailErr(t, "stat file", err)
		wires[i] = table.Ensure("v", q(fmt.Sprintf("dir/f%02d", i)), meta, dirMeta.StableID(), false)
	}

	before := core.Stats().LowerDirReads
	for pass := range 2 {
		for i, wire := range wires {
			rec, err := table.Resolve(wire[:], 0)
			testutil.FailErr(t, "resolve", err)
			if want := fmt.Sprintf("dir/f%02d", i); rec.qpath.Rel != want {
				t.Fatalf("pass %d: resolved %q, want %q", pass, rec.qpath.Rel, want)
			}
		}
	}
	if got := core.Stats().LowerDirReads - before; got != 1 {
		t.Fatalf("directory listed %d times across two walks, want 1", got)
	}

	// A remembered name is only a candidate: the object gone from it is stale, not a different file.
	testutil.FailErr(t, "remove", os.Remove(filepath.Join(trunk, "dir", "f00")))
	for i := range 8 { // evict f00 again
		if _, err := table.Resolve(wires[10+i][:], 0); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := table.Resolve(wires[0][:], 0); err == nil {
		t.Fatal("resolved a handle whose object was removed from the trunk")
	}
}
