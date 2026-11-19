//go:build darwin || linux

package fseffect_test

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/fseffect"
	"github.com/lycaon/lycaon/internal/fspath"
	"github.com/lycaon/lycaon/internal/testutil"
)

func TestReviewRejectsReplacedParentDirectory(t *testing.T) {
	root := t.TempDir()
	folder, moved := filepath.Join(root, "folder"), filepath.Join(root, "moved")
	testutil.FailErr(t, "create directory", os.Mkdir(folder, 0o755))
	testutil.FailErr(t, "seed target", os.WriteFile(filepath.Join(folder, "AGENTS.md"), []byte("original"), 0o644))
	result, err := fseffect.Replace(fseffect.ReplaceRequest{
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
	if err == nil || result.Publication != fseffect.PublicationNotCommitted {
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

func TestReplacementRemainsBoundToObservedParent(t *testing.T) {
	root := t.TempDir()
	folder, moved := filepath.Join(root, "folder"), filepath.Join(root, "moved")
	testutil.FailErr(t, "create observed parent", os.Mkdir(folder, 0o700))
	testutil.FailErr(t, "seed original target", os.WriteFile(filepath.Join(folder, "file"), []byte("original"), 0o600))
	identity, err := fspath.EntryIdentity(folder)
	testutil.FailErr(t, "get parent identity", err)
	req := fseffect.ReplaceRequest{
		Location: fseffect.Location{Root: root, Rel: "folder/file"}, ExpectedParentIdentity: identity,
		Source: strings.NewReader("edited"),
	}

	staged, _, err := fseffect.StageReplace(req)
	testutil.FailErr(t, "stage replacement", err)
	defer staged.Close()
	testutil.FailErr(t, "move observed parent", os.Rename(folder, moved))
	testutil.FailErr(t, "create replacement parent", os.Mkdir(folder, 0o700))
	testutil.FailErr(t, "seed replacement target", os.WriteFile(filepath.Join(folder, "file"), []byte("original"), 0o600))

	result, err := staged.Commit()
	if !errors.Is(err, fseffect.ErrPredicateChanged) || result.Publication != fseffect.PublicationNotCommitted {
		t.Fatalf("commit after parent replacement = %+v, %v", result, err)
	}
	req.Source = strings.NewReader("edited again")
	if staged, _, err := fseffect.StageReplace(req); !errors.Is(err, fseffect.ErrPredicateChanged) {
		if staged != nil {
			staged.Close()
		}
		t.Fatalf("stage after parent replacement = %v", err)
	}
	for _, dir := range []string{folder, moved} {
		content, err := os.ReadFile(filepath.Join(dir, "file"))
		testutil.FailErr(t, "read surviving target", err)
		if string(content) != "original" {
			t.Fatalf("changed target in %s: %q", dir, content)
		}
	}
}
