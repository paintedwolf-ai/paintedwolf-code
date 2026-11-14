package layeredfs

import (
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"

	"github.com/lycaon/lycaon/internal/testutil"
)

func newCountingCore(t *testing.T, cfg Config) (*Core, string) {
	t.Helper()
	tmp := t.TempDir()
	trunk := filepath.Join(tmp, "trunk")
	backing := filepath.Join(tmp, "backing")
	testutil.FailErr(t, "mkdir trunk", os.MkdirAll(trunk, 0o755))
	testutil.FailErr(t, "mkdir backing", os.MkdirAll(backing, 0o700))
	cfg.ViewID, cfg.BackingDir = "view-counts", backing
	cfg.Roots = []RootSpec{{ID: "primary", Label: "primary", TrunkPath: trunk, IsPrimary: true}}
	core, err := New(cfg)
	testutil.FailErr(t, "New Core", err)
	t.Cleanup(func() { _ = core.ReleaseView() })
	return core, trunk
}

// One lookup visits each directory once, however deep the path, and presenting metadata reads no
// extended attributes. The hook fires for every lower component the provider touches.
func TestLookupTouchesEachComponentOnce(t *testing.T) {
	var touched, xattrReads atomic.Int64
	core, trunk := newCountingCore(t, Config{
		LookupRaceHook: func(QualifiedPath, string) { touched.Add(1) },
		FaultHooks: &FaultHooks{LowerXattrHook: func(QualifiedPath) error {
			xattrReads.Add(1)
			return nil
		}},
	})
	testutil.FailErr(t, "mkdir", os.MkdirAll(filepath.Join(trunk, "a/b/c/d/e/f/g/h/i"), 0o755))
	testutil.FailErr(t, "seed", os.WriteFile(filepath.Join(trunk, "a/b/c/d/e/f/g/h/i/file"), []byte("x"), 0o644))
	q := QualifiedPath{RootID: "primary", Rel: "a/b/c/d/e/f/g/h/i/file"}
	const depth = 10

	attr, err := core.LstatAttrs(q)
	testutil.FailErr(t, "LstatAttrs", err)
	if attr.Kind != EntryRegular || attr.Size != 1 {
		t.Fatalf("attr = %+v", attr)
	}
	// One stat per component, one descent per directory above the file.
	if got := touched.Load(); got != 2*depth-1 {
		t.Fatalf("components touched = %d, want %d", got, 2*depth-1)
	}
	if got := xattrReads.Load(); got != 0 {
		t.Fatalf("xattr reads for metadata = %d, want 0", got)
	}

	touched.Store(0)
	_, err = core.Lstat(q)
	testutil.FailErr(t, "Lstat", err)
	if got := xattrReads.Load(); got != 1 {
		t.Fatalf("xattr reads for a full lstat = %d, want 1 (the file, not the directories)", got)
	}
}

// A walk restarts correctly when a symlink sends resolution back above where it stood.
func TestLookupWalkFollowsSymlinkBackUp(t *testing.T) {
	core, trunk := newCountingCore(t, Config{})
	testutil.FailErr(t, "mkdir", os.MkdirAll(filepath.Join(trunk, "x/deep/er"), 0o755))
	testutil.FailErr(t, "mkdir", os.MkdirAll(filepath.Join(trunk, "y"), 0o755))
	testutil.FailErr(t, "seed", os.WriteFile(filepath.Join(trunk, "top"), []byte("t"), 0o644))
	testutil.FailErr(t, "seed", os.WriteFile(filepath.Join(trunk, "y/file"), []byte("yy"), 0o644))
	testutil.FailErr(t, "link", os.Symlink("../../../y", filepath.Join(trunk, "x/deep/er/up")))
	testutil.FailErr(t, "link", os.Symlink("../../../top", filepath.Join(trunk, "x/deep/er/totop")))

	attr, err := core.Stat(QualifiedPath{RootID: "primary", Rel: "x/deep/er/up/file"})
	testutil.FailErr(t, "stat through symlink", err)
	if attr.Size != 2 {
		t.Fatalf("size = %d, want 2", attr.Size)
	}
	attr, err = core.Stat(QualifiedPath{RootID: "primary", Rel: "x/deep/er/totop"})
	testutil.FailErr(t, "stat to root file", err)
	if attr.Size != 1 {
		t.Fatalf("size = %d, want 1", attr.Size)
	}
}
