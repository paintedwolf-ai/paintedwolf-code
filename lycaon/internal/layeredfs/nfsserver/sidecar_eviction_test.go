package nfsserver

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"github.com/lycaon/lycaon/internal/layeredfs"
	"github.com/lycaon/lycaon/internal/testutil"
)

// A client that caches attributes keeps using a ._ file's handle after the server's table has
// dropped it, so a presented sidecar must resolve again from its handle alone, for a trunk file and
// for a file the view made.
func TestPresentedSidecarResolvesAfterLeavingTheHandleTable(t *testing.T) {
	tmp := t.TempDir()
	trunk, backing := filepath.Join(tmp, "trunk"), filepath.Join(tmp, "backing")
	testutil.FailErr(t, "mkdir", os.MkdirAll(trunk, 0o755))
	testutil.FailErr(t, "mkdir", os.MkdirAll(backing, 0o700))
	testutil.FailErr(t, "seed", os.WriteFile(filepath.Join(trunk, "lower.txt"), []byte("lower"), 0o644))
	core, err := layeredfs.New(layeredfs.Config{
		ViewID: "v", BackingDir: backing,
		Roots: []layeredfs.RootSpec{{ID: "primary", Label: "primary", TrunkPath: trunk, IsPrimary: true}},
	})
	testutil.FailErr(t, "core", err)
	t.Cleanup(func() { _ = core.ReleaseView() })
	q := func(rel string) layeredfs.QualifiedPath { return layeredfs.QualifiedPath{RootID: "primary", Rel: rel} }
	_, err = core.WriteFile(q("f"), []byte("new\n"), 0o644)
	testutil.FailErr(t, "create", err)
	testutil.FailErr(t, "xattr f", core.SetXattr(q("f"), "user.a", []byte("v"), true))
	testutil.FailErr(t, "xattr lower", core.SetXattr(q("lower.txt"), "user.low", []byte("v"), true))

	sidecars := newSidecarTable()
	table := newHandleTable([32]byte{1}, 4, sidecars, func(uint64, ViewID) *layeredfs.Core { return core })
	vHash, err := table.RegisterView("v")
	testutil.FailErr(t, "register", err)
	root, err := core.Lstat(q("."))
	testutil.FailErr(t, "stat root", err)
	_, ok := table.Ensure(vHash, "v", q("."), root, 0, true)
	if !ok {
		t.Fatal("root handle refused")
	}
	wires := map[string][HandleSize]byte{}
	for _, target := range []string{"f", "lower.txt"} {
		sidecar := sidecarPathOf(q(target))
		meta, _, presented := sidecars.synth(core, sidecar)
		if !presented {
			t.Fatalf("%s has no presented sidecar", target)
		}
		wire, ok := table.Ensure(vHash, "v", sidecar, meta, root.StableID(), false)
		if !ok {
			t.Fatalf("%s handle refused", sidecar.Rel)
		}
		wires[target] = wire
	}
	for i := range 16 {
		name := fmt.Sprintf("n%02d", i)
		meta, err := core.WriteFile(q(name), []byte("x"), 0o644)
		testutil.FailErr(t, "fill", err)
		table.Ensure(vHash, "v", q(name), meta, root.StableID(), false)
	}
	for target, wire := range wires {
		rec, err := table.Resolve(wire[:], 0)
		if err != nil {
			t.Fatalf("._%s after eviction: %v", target, err)
		}
		if want := "._" + target; rec.qpath.Rel != want {
			t.Fatalf("resolved %q, want %q", rec.qpath.Rel, want)
		}
	}
}
