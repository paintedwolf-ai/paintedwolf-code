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

func TestOwnPathNamesTheCurrentMarkOnly(t *testing.T) {
	current.mu.Lock()
	saved := current.dir
	current.dir = ""
	current.mu.Unlock()
	t.Cleanup(func() { current.mu.Lock(); current.dir = saved; current.mu.Unlock() })
	if own := OwnPath(); own != "" {
		t.Fatalf("OwnPath = %q with no mark enabled", own)
	}
	current.mu.Lock()
	current.dir = "/marks/7-x"
	current.mu.Unlock()
	if own := OwnPath(); own != filepath.Join("/marks/7-x", ownName) {
		t.Fatalf("OwnPath = %q", own)
	}
}
