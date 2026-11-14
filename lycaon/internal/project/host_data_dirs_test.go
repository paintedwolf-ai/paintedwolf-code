package project

import (
	"os"
	"path/filepath"
	"slices"
	"testing"

	"github.com/lycaon/lycaon/internal/testutil"
)

func TestHostDataDirsListsProjectTreesOnly(t *testing.T) {
	dataDir := t.TempDir()
	dirs, err := HostDataDirs(dataDir)
	testutil.FailErr(t, "list before any project", err)
	if len(dirs) != 0 {
		t.Fatalf("dirs before any project = %v", dirs)
	}
	first, err := EnsureHostDataDir(dataDir, "a")
	testutil.FailErr(t, "ensure a", err)
	second, err := EnsureHostDataDir(dataDir, "b")
	testutil.FailErr(t, "ensure b", err)
	testutil.FailErr(t, "stray file", os.WriteFile(filepath.Join(filepath.Dir(first), "stray"), nil, 0o600))
	dirs, err = HostDataDirs(dataDir)
	testutil.FailErr(t, "list", err)
	slices.Sort(dirs)
	if !slices.Equal(dirs, []string{first, second}) {
		t.Fatalf("dirs = %v", dirs)
	}
}
