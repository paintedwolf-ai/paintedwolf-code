//go:build darwin || linux

package platform_test

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"testing"

	"github.com/lycaon/lycaon/internal/layeredfs"
	"github.com/lycaon/lycaon/internal/layeredfs/privategit"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/testvolume"
)

// Through this host's mount of a source that folds names, every spelling of a privately changed,
// deleted or created name gives the private result.
func TestMountedViewOfAFoldingSourceSeesOneNameUnderEverySpelling(t *testing.T) {
	volume, ran := testvolume.New(t, testvolume.Folding)
	if ran {
		return
	}
	trunk := volume.Dir(t, "source")
	if !layeredfs.ProbeNameFolding(trunk).Case {
		t.Skip("this volume does not fold names")
	}
	for name, content := range map[string]string{"README.md": "trunk\n", "Gone.txt": "gone\n"} {
		testutil.FailErr(t, "seed "+name, os.WriteFile(filepath.Join(trunk, name), []byte(content), 0o644))
	}
	mgr, err := privategit.NewManager(privategit.ManagerConfig{BackingDir: filepath.Join(t.TempDir(), "pgit")})
	testutil.FailErr(t, "privategit.NewManager", err)
	view, _ := mountedHostView(t, trunk, mgr)
	work := view.PrimaryWorkPath

	testutil.FailErr(t, "edit", os.WriteFile(filepath.Join(work, "README.md"), []byte("edited\n"), 0o644))
	got, err := os.ReadFile(filepath.Join(work, "readme.md"))
	testutil.FailErr(t, "read another spelling", err)
	if string(got) != "edited\n" {
		t.Fatalf("another spelling read %q, want the edit", got)
	}

	testutil.FailErr(t, "delete", os.Remove(filepath.Join(work, "Gone.txt")))
	if _, err := os.Stat(filepath.Join(work, "GONE.TXT")); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("a deleted file came back under another spelling: %v", err)
	}

	testutil.FailErr(t, "create", os.WriteFile(filepath.Join(work, "New.txt"), []byte("new\n"), 0o644))
	if _, err := os.Stat(filepath.Join(work, "new.TXT")); err != nil {
		t.Fatalf("a new file is missing under another spelling: %v", err)
	}

	entries, err := os.ReadDir(work)
	testutil.FailErr(t, "list", err)
	var names []string
	for _, entry := range entries {
		names = append(names, entry.Name())
	}
	for _, want := range []string{"New.txt", "README.md"} {
		if !slices.Contains(names, want) {
			t.Fatalf("listing %v lacks %s", names, want)
		}
	}
	if slices.Contains(names, "Gone.txt") || slices.Contains(names, "readme.md") {
		t.Fatalf("listing %v shows a deleted name or a second spelling", names)
	}
}
