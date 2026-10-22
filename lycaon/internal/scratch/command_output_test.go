package scratch_test

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/fseffect"
	"github.com/lycaon/lycaon/internal/scratch"
)

func TestCommandOutputRejectsInvalidDestinationsWithoutCreatingFiles(t *testing.T) {
	root := t.TempDir()
	for _, id := range []string{"", ".", "..", "../escape", `..\escape`} {
		if err := scratch.PublishCommandOutput(root, id, "stdout", strings.NewReader("bytes")); !errors.Is(err, fseffect.ErrInvalidPath) {
			t.Fatalf("id %q: %v", id, err)
		}
	}
	if err := scratch.PublishCommandOutput(root, "program", "../../escape", strings.NewReader("bytes")); !errors.Is(err, fseffect.ErrInvalidPath) {
		t.Fatalf("invalid stream: %v", err)
	}
	if _, err := os.Stat(filepath.Join(root, "output")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("invalid destination created output directory: %v", err)
	}
}

func TestCommandOutputDirectoryPreparedBeforePublication(t *testing.T) {
	root := t.TempDir()
	dir, err := scratch.PrepareCommandOutputDirectory(root)
	if err != nil {
		t.Fatal(err)
	}
	entries, err := os.ReadDir(dir)
	if err != nil || len(entries) != 0 {
		t.Fatalf("unpublished output directory: %v, %v", entries, err)
	}
	if err := scratch.PublishCommandOutput(root, "program", "stdout", strings.NewReader("saved\n")); err != nil {
		t.Fatal(err)
	}
	if _, err := scratch.PrepareCommandOutputDirectory(root); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(filepath.Join(dir, "program.stdout"))
	if err != nil || string(data) != "saved\n" {
		t.Fatalf("prepared directory lost retained output: %q, %v", data, err)
	}
}

func TestCommandOutputPreparationRefusesDirectoryAlias(t *testing.T) {
	root, outside := t.TempDir(), t.TempDir()
	if err := os.Symlink(outside, filepath.Join(root, "output")); err != nil {
		t.Fatal(err)
	}
	if _, err := scratch.PrepareCommandOutputDirectory(root); !errors.Is(err, fseffect.ErrSymlink) {
		t.Fatalf("output alias accepted: %v", err)
	}
	entries, err := os.ReadDir(outside)
	if err != nil || len(entries) != 0 {
		t.Fatalf("outside directory changed: %v, %v", entries, err)
	}
}
