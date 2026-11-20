package sandboxmark

import (
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/lycaon/lycaon/internal/testutil"
)

func TestRemoveDeletesOnlyTheMarkAndAnEmptiedParent(t *testing.T) {
	parent := filepath.Join(t.TempDir(), "sandbox-marks")
	first, second := filepath.Join(parent, "1-a"), filepath.Join(parent, "2-b")
	for _, dir := range []string{first, second} {
		testutil.FailErr(t, "create mark", os.MkdirAll(dir, 0o700))
		for _, name := range []string{ownName, foreignName} {
			testutil.FailErr(t, "write marker", os.WriteFile(filepath.Join(dir, name), nil, 0o400))
		}
	}
	Remove("relative/mark")
	Remove(first)
	if _, err := os.Stat(first); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("the removed mark remains: %v", err)
	}
	if _, err := os.Stat(filepath.Join(second, ownName)); err != nil {
		t.Fatalf("removing one mark touched another engine's: %v", err)
	}
	Remove(second)
	if _, err := os.Stat(parent); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("the emptied parent remains: %v", err)
	}
}
