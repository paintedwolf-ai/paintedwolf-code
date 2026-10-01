//go:build darwin || linux

package fseffect_test

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sync"
	"testing"

	"github.com/lycaon/lycaon/internal/fseffect"
	"github.com/lycaon/lycaon/internal/testutil"
)

func readFromRoot(t *testing.T, root *fseffect.ReadRoot, rel string) (string, error) {
	t.Helper()
	f, err := root.Open(rel)
	if err != nil {
		return "", err
	}
	defer func() { _ = f.Close() }()
	b, readErr := io.ReadAll(f)
	testutil.FailErr(t, "read opened target", readErr)
	return string(b), nil
}

// One held root serves many concurrent reads, and closing it leaves the files
// it opened readable.
func TestReadRootServesConcurrentReads(t *testing.T) {
	dir := t.TempDir()
	for i := range 32 {
		name := filepath.Join(dir, "pkg", fmt.Sprintf("f%02d.txt", i))
		testutil.FailErr(t, "mkdir", os.MkdirAll(filepath.Dir(name), 0o755))
		testutil.FailErr(t, "seed", os.WriteFile(name, []byte(fmt.Sprint(i)), 0o600))
	}
	root, err := fseffect.OpenReadRoot(dir)
	testutil.FailErr(t, "open read root", err)
	var wg sync.WaitGroup
	for i := range 32 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			got, err := readFromRoot(t, root, fmt.Sprintf("pkg/f%02d.txt", i))
			if err != nil || got != fmt.Sprint(i) {
				t.Errorf("read f%02d = %q, %v", i, got, err)
			}
		}()
	}
	wg.Wait()
	kept, err := root.Open("pkg/f00.txt")
	testutil.FailErr(t, "open before close", err)
	testutil.FailErr(t, "close root", root.Close())
	b, err := io.ReadAll(kept)
	testutil.FailErr(t, "read after root close", err)
	if string(b) != "0" {
		t.Fatalf("file opened before close read %q", b)
	}
	_ = kept.Close()
}

// Reads through a held root refuse the same escapes OpenRead refuses.
func TestReadRootRefusesLinksLeavingTheRoot(t *testing.T) {
	dir := t.TempDir()
	outsideDir := t.TempDir()
	outside := filepath.Join(outsideDir, "secret.txt")
	testutil.FailErr(t, "seed outside", os.WriteFile(outside, []byte("secret\n"), 0o600))
	testutil.FailErr(t, "absolute escape", os.Symlink(outside, filepath.Join(dir, "abs-escape")))
	testutil.FailErr(t, "directory escape", os.Symlink(outsideDir, filepath.Join(dir, "dir-escape")))
	root, err := fseffect.OpenReadRoot(dir)
	testutil.FailErr(t, "open read root", err)
	defer func() { _ = root.Close() }()
	for _, rel := range []string{"abs-escape", "dir-escape/secret.txt", "../" + filepath.Base(outsideDir) + "/secret.txt"} {
		if got, err := readFromRoot(t, root, rel); err == nil {
			t.Fatalf("read %q escaped the root and returned %q", rel, got)
		}
	}
}

// The held descriptor is the admitted directory: retargeting its path after
// the root opened cannot redirect later reads.
func TestReadRootKeepsTheAdmittedDirectory(t *testing.T) {
	parent := t.TempDir()
	dir := filepath.Join(parent, "project")
	testutil.FailErr(t, "mkdir project", os.Mkdir(dir, 0o755))
	testutil.FailErr(t, "seed project file", os.WriteFile(filepath.Join(dir, "a.txt"), []byte("admitted"), 0o600))
	outsideDir := t.TempDir()
	testutil.FailErr(t, "seed outside file", os.WriteFile(filepath.Join(outsideDir, "a.txt"), []byte("outside"), 0o600))
	root, err := fseffect.OpenReadRoot(dir)
	testutil.FailErr(t, "open read root", err)
	defer func() { _ = root.Close() }()

	testutil.FailErr(t, "move project aside", os.Rename(dir, filepath.Join(parent, "moved")))
	testutil.FailErr(t, "retarget project path", os.Symlink(outsideDir, dir))
	got, err := readFromRoot(t, root, "a.txt")
	testutil.FailErr(t, "read after retarget", err)
	if got != "admitted" {
		t.Fatalf("held root read %q after its path was retargeted", got)
	}
}
