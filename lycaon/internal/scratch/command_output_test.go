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
