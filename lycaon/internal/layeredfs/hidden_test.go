package layeredfs

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"syscall"
	"testing"

	"github.com/lycaon/lycaon/internal/testutil"
)

// Engine storage inside a root resolves as absent, is never listed, and cannot be created or reached.
func TestHiddenEngineStorageIsAbsentAndCannotBeCreated(t *testing.T) {
	t.Parallel()
	tmp := t.TempDir()
	trunk := filepath.Join(tmp, "home")
	for rel, content := range map[string]string{"notes.txt": "notes\n", ".config/paintedwolf/store.db": "state\n", ".config/other.conf": "other\n"} {
		testutil.FailErr(t, "mkdir", os.MkdirAll(filepath.Join(trunk, filepath.Dir(rel)), 0o755))
		testutil.FailErr(t, "seed "+rel, os.WriteFile(filepath.Join(trunk, rel), []byte(content), 0o644))
	}
	backing := filepath.Join(tmp, "backing")
	testutil.FailErr(t, "mkdir backing", os.MkdirAll(backing, 0o700))
	core, err := New(Config{
		ViewID: "view-hidden", BackingDir: backing,
		Roots: []RootSpec{{ID: "primary", Label: "primary", TrunkPath: trunk, IsPrimary: true, Hidden: []string{".config/paintedwolf"}}},
	})
	testutil.FailErr(t, "New Core", err)
	t.Cleanup(func() { _ = core.ReleaseView() })
	qp := func(rel string) QualifiedPath { return QualifiedPath{RootID: "primary", Rel: rel} }

	for _, rel := range []string{".config/paintedwolf", ".config/paintedwolf/store.db"} {
		if _, err := core.Stat(qp(rel)); !errors.Is(err, fs.ErrNotExist) {
			t.Fatalf("stat %s = %v, want absent", rel, err)
		}
		if _, err := core.ReadFile(qp(rel)); err == nil {
			t.Fatalf("read %s succeeded", rel)
		}
	}
	entries, err := core.ReadDir(qp(".config"))
	testutil.FailErr(t, "list .config", err)
	var names []string
	for _, e := range entries {
		names = append(names, e.Name)
	}
	if !slices.Equal(names, []string{"other.conf"}) {
		t.Fatalf(".config lists %v", names)
	}

	if _, err := core.WriteFile(qp(".config/paintedwolf"), []byte("x"), 0o644); !errors.Is(err, syscall.EACCES) {
		t.Fatalf("create the hidden name = %v, want EACCES", err)
	}
	if _, err := core.Mkdir(qp(".config/paintedwolf"), 0o755); !errors.Is(err, syscall.EACCES) {
		t.Fatalf("mkdir the hidden name = %v, want EACCES", err)
	}
	if err := core.Rename(qp("notes.txt"), qp(".config/paintedwolf")); !errors.Is(err, syscall.EACCES) {
		t.Fatalf("rename onto the hidden name = %v, want EACCES", err)
	}
	if _, err := core.WriteFile(qp(".config/paintedwolf/new.db"), []byte("x"), 0o644); err == nil {
		t.Fatal("created a file inside hidden storage")
	}
}

// A placement folder made after the view started stays out of it, wherever it appears.
func TestPlacementFolderMadeLaterIsHiddenByName(t *testing.T) {
	t.Parallel()
	tmp := t.TempDir()
	trunk := filepath.Join(tmp, "volume")
	backing := filepath.Join(tmp, "backing")
	for _, dir := range []string{filepath.Join(trunk, "disk"), backing} {
		testutil.FailErr(t, "mkdir", os.MkdirAll(dir, 0o755))
	}
	const placement = ".paintedwolf-placement-0-0123456789abcdef.noindex"
	core, err := New(Config{
		ViewID: "view-late-placement", BackingDir: backing,
		Roots: []RootSpec{{ID: "primary", Label: "primary", TrunkPath: trunk, IsPrimary: true, HiddenNames: []string{placement}}},
	})
	testutil.FailErr(t, "New Core", err)
	t.Cleanup(func() { _ = core.ReleaseView() })

	late := filepath.Join(trunk, "disk", placement, "worker-branches")
	testutil.FailErr(t, "make the placement folder", os.MkdirAll(late, 0o700))
	if _, err := core.Stat(QualifiedPath{RootID: "primary", Rel: "disk/" + placement}); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("a later placement folder resolved: %v", err)
	}
	entries, err := core.ReadDir(QualifiedPath{RootID: "primary", Rel: "disk"})
	testutil.FailErr(t, "list disk", err)
	if len(entries) != 0 {
		t.Fatalf("disk lists %+v", entries)
	}
}
