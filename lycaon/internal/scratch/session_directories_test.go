package scratch_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/enginepaths"
	"github.com/lycaon/lycaon/internal/fspath"
	"github.com/lycaon/lycaon/internal/scratch"
	"github.com/lycaon/lycaon/internal/testutil"
)

func TestSessionOutputHasIndependentStorageAndOneCleanupLifetime(t *testing.T) {
	state := t.TempDir()
	folders := scratch.New(state)
	dirs, err := folders.Ensure("chat")
	testutil.FailErr(t, "prepare session capabilities", err)
	if dirs.Output != enginepaths.SessionOutputUnder(fspath.CanonicalPath(state), "chat") || dirs.Output == dirs.Scratch || strings.HasPrefix(dirs.Output, dirs.Scratch+string(filepath.Separator)) {
		t.Fatalf("directories=%+v", dirs)
	}
	info, err := os.Stat(dirs.Output)
	testutil.FailErr(t, "output exists before publication", err)
	if !info.IsDir() || info.Mode().Perm() != 0700 {
		t.Fatalf("output directory mode=%v", info.Mode())
	}
	testutil.FailErr(t, "publish output", scratch.PublishCommandOutput(dirs.Output, "program", "stdout", strings.NewReader("saved\n")))
	bytes, err := os.ReadFile(filepath.Join(dirs.Output, "program.stdout"))
	testutil.FailErr(t, "read published copy", err)
	if string(bytes) != "saved\n" {
		t.Fatalf("copy=%q", bytes)
	}
	present, size, err := folders.Inventory(t.Context())
	testutil.FailErr(t, "inventory both areas", err)
	if !present || size != 6 {
		t.Fatalf("inventory=%v/%d", present, size)
	}
	holds, releases := 0, 0
	kept, err := folders.Reclaim(t.Context(), func(id string) (func(), bool) {
		holds++
		if id != "chat" {
			t.Fatalf("wrong session %q", id)
		}
		return func() { releases++ }, true
	})
	testutil.FailErr(t, "reclaim both areas", err)
	if len(kept) != 0 || holds != 1 || releases != 1 {
		t.Fatalf("cleanup kept=%v holds=%d releases=%d", kept, holds, releases)
	}
	for _, dir := range []string{dirs.Scratch, dirs.Output} {
		if _, err := os.Stat(dir); !os.IsNotExist(err) {
			t.Fatalf("session folder survived: %q %v", dir, err)
		}
	}
}

func TestOutputPreparationRefusesLinkedFolderOrParent(t *testing.T) {
	for _, leaf := range []bool{false, true} {
		t.Run(map[bool]string{false: "parent", true: "session"}[leaf], func(t *testing.T) {
			state, outside := t.TempDir(), t.TempDir()
			parent := enginepaths.SessionOutputRootUnder(state)
			link := parent
			if leaf {
				testutil.FailErr(t, "output parent", os.Mkdir(parent, 0700))
				link = filepath.Join(parent, "chat")
			}
			testutil.FailErr(t, "plant output link", os.Symlink(outside, link))
			if _, err := scratch.New(state).Ensure("chat"); err == nil {
				t.Fatal("output capability followed a link")
			}
			entries, err := os.ReadDir(outside)
			testutil.FailErr(t, "outside storage", err)
			if len(entries) != 0 {
				t.Fatalf("wrote through planted link: %v", entries)
			}
		})
	}
}
