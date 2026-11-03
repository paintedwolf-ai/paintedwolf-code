package scratch_test

import (
	"errors"
	"os"
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
	entries, err := os.ReadDir(root)
	if err != nil || len(entries) != 0 {
		t.Fatalf("invalid destination created entries: %v, %v", entries, err)
	}
}
