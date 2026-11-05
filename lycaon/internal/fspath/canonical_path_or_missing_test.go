package fspath

import (
	"os"
	"path/filepath"
	"testing"
)

func TestCanonicalPathOrMissingRefusesAnythingButAMissingName(t *testing.T) {
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	file := filepath.Join(root, "file")
	if err := os.WriteFile(file, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(root, "gone"), filepath.Join(root, "dangling")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(root, "loop"), filepath.Join(root, "loop")); err != nil {
		t.Fatal(err)
	}
	locked := filepath.Join(root, "locked")
	if err := os.Mkdir(locked, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(locked, 0); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(locked, 0o700) })

	for name, want := range map[string]string{
		filepath.Join(root, "missing", "sock"): filepath.Join(root, "missing", "sock"),
		filepath.Join(root, "dangling"):        filepath.Join(root, "gone"),
		file:                                   file,
	} {
		if got, ok := CanonicalPathOrMissing(name); !ok || got != want {
			t.Errorf("%s = %q, %v; want %q", name, got, ok, want)
		}
	}
	refused := []string{filepath.Join(file, "sock"), filepath.Join(root, "loop", "sock")}
	if os.Geteuid() != 0 {
		refused = append(refused, filepath.Join(locked, "sock"))
	}
	for _, name := range refused {
		if got, ok := CanonicalPathOrMissing(name); ok {
			t.Errorf("%s resolved to %q, want refused", name, got)
		}
	}
}
