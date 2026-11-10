//go:build darwin || linux

package fseffect_test

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
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

// Retargeting or removing a previously warmed ancestor alias while retaining
// the old tree must never allow subsequent reads or mutations of an uncached
// child through the original alias spelling to reach the stale old tree.
func TestRetargetedOrRemovedAncestorAliasDoesNotReachOldTree(t *testing.T) {
	parent := t.TempDir()
	oldTree := filepath.Join(parent, "old")
	newTree := filepath.Join(parent, "new")
	alias := filepath.Join(parent, "alias")

	for path, content := range map[string]string{
		filepath.Join(oldTree, "warm", "seed.txt"):             "old-warm",
		filepath.Join(oldTree, "uncached", "target.txt"):       "old-uncached",
		filepath.Join(oldTree, "uncached-removed", "keep.txt"): "old-keep",
		filepath.Join(newTree, "uncached", "target.txt"):       "new-uncached",
	} {
		testutil.FailErr(t, "mkdir fixture", os.MkdirAll(filepath.Dir(path), 0o755))
		testutil.FailErr(t, "seed fixture", os.WriteFile(path, []byte(content), 0o600))
	}
	testutil.FailErr(t, "create ancestor alias", os.Symlink(oldTree, alias))

	// Warm both the ancestor alias root and a child under the alias.
	warmRoot, err := fseffect.OpenReadRoot(alias)
	testutil.FailErr(t, "warm ancestor alias root", err)
	gotWarm, err := readFromRoot(t, warmRoot, "warm/seed.txt")
	testutil.FailErr(t, "read warm seed", err)
	if gotWarm != "old-warm" {
		t.Fatalf("warm read = %q, want old-warm", gotWarm)
	}
	testutil.FailErr(t, "close warm root", warmRoot.Close())

	warmChildFile, err := fseffect.OpenRead(fseffect.Location{Root: filepath.Join(alias, "warm"), Rel: "seed.txt"})
	testutil.FailErr(t, "warm child through alias", err)
	_ = warmChildFile.Close()

	// Retarget alias -> newTree while retaining oldTree intact, then access a
	// previously uncached child through the original alias spelling.
	testutil.FailErr(t, "remove old alias", os.Remove(alias))
	testutil.FailErr(t, "retarget alias to new tree", os.Symlink(newTree, alias))

	uncachedViaAlias := filepath.Join(alias, "uncached")
	retargetedRoot, err := fseffect.OpenReadRoot(uncachedViaAlias)
	testutil.FailErr(t, "open uncached child after retarget", err)
	gotRetargeted, err := readFromRoot(t, retargetedRoot, "target.txt")
	testutil.FailErr(t, "read uncached child after retarget", err)
	testutil.FailErr(t, "close retargeted root", retargetedRoot.Close())
	if gotRetargeted != "new-uncached" {
		t.Fatalf("read through retargeted alias = %q, want new-uncached", gotRetargeted)
	}

	_, err = fseffect.Replace(fseffect.ReplaceRequest{
		Location: fseffect.Location{Root: uncachedViaAlias, Rel: "written.txt"},
		Source:   strings.NewReader("new-mutation"),
		Mode:     0o600,
	})
	testutil.FailErr(t, "replace through retargeted alias", err)
	if _, statErr := os.Stat(filepath.Join(oldTree, "uncached", "written.txt")); !os.IsNotExist(statErr) {
		t.Fatalf("mutation through retargeted alias reached old tree: %v", statErr)
	}
	gotNewWritten, err := os.ReadFile(filepath.Join(newTree, "uncached", "written.txt"))
	testutil.FailErr(t, "read new tree written file", err)
	if string(gotNewWritten) != "new-mutation" {
		t.Fatalf("new tree written bytes = %q, want new-mutation", gotNewWritten)
	}

	// Remove the alias completely while retaining oldTree intact, then access
	// another previously uncached child through the original alias spelling.
	testutil.FailErr(t, "remove alias completely", os.Remove(alias))
	removedViaAlias := filepath.Join(alias, "uncached-removed")
	if r, openErr := fseffect.OpenReadRoot(removedViaAlias); openErr == nil {
		_ = r.Close()
		t.Fatal("OpenReadRoot succeeded through removed ancestor alias")
	}
	if f, openErr := fseffect.OpenRead(fseffect.Location{Root: removedViaAlias, Rel: "keep.txt"}); openErr == nil {
		_ = f.Close()
		t.Fatal("OpenRead succeeded through removed ancestor alias")
	}
	if _, replaceErr := fseffect.Replace(fseffect.ReplaceRequest{
		Location: fseffect.Location{Root: removedViaAlias, Rel: "keep.txt"},
		Source:   strings.NewReader("clobbered"),
		Mode:     0o600,
	}); replaceErr == nil {
		t.Fatal("Replace succeeded through removed ancestor alias")
	}
	gotOldKeep, err := os.ReadFile(filepath.Join(oldTree, "uncached-removed", "keep.txt"))
	testutil.FailErr(t, "read retained old tree file", err)
	if string(gotOldKeep) != "old-keep" {
		t.Fatalf("retained old tree file = %q, want old-keep", gotOldKeep)
	}
}
