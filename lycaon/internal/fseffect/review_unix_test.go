//go:build darwin || linux

package fseffect_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/fseffect"
	"github.com/lycaon/lycaon/internal/testutil"
)

func TestReviewRejectsReplacedParentDirectory(t *testing.T) {
	root := t.TempDir()
	folder, moved := filepath.Join(root, "folder"), filepath.Join(root, "moved")
	testutil.FailErr(t, "create directory", os.Mkdir(folder, 0o755))
	testutil.FailErr(t, "seed target", os.WriteFile(filepath.Join(folder, "AGENTS.md"), []byte("original"), 0o644))
	_, err := fseffect.Replace(fseffect.ReplaceRequest{
		Location: fseffect.Location{Root: root, Rel: "folder/AGENTS.md"}, Source: strings.NewReader("proposal"), Mode: 0o644,
		ReviewStaged: func(fseffect.Target, fseffect.Result) error {
			if err := os.Rename(folder, moved); err != nil {
				return err
			}
			if err := os.Mkdir(folder, 0o755); err != nil {
				return err
			}
			return os.WriteFile(filepath.Join(folder, "AGENTS.md"), []byte("original"), 0o644)
		},
	})
	if err == nil {
		t.Fatal("reviewed path moved but replacement committed")
	}
	for _, dir := range []string{folder, moved} {
		current, readErr := os.ReadFile(filepath.Join(dir, "AGENTS.md"))
		testutil.FailErr(t, "read surviving target", readErr)
		if string(current) != "original" {
			t.Fatalf("changed instructions in %s: %q", dir, current)
		}
	}
}
