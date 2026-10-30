//go:build darwin || linux

package fseffect

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/testutil"
)

func TestDirectoryPathFallbackRequiresCurrentContainedIdentity(t *testing.T) {
	root := t.TempDir()
	testutil.FailErr(t, "create parent", os.MkdirAll(filepath.Join(root, "parent", "child"), 0700))
	reader, err := OpenReadRoot(root)
	testutil.FailErr(t, "open root", err)
	defer reader.Close()
	file, err := reader.OpenExact("parent/child")
	testutil.FailErr(t, "hold child and cache parent", err)
	defer file.Close()
	actual, err := reader.directoryPathFromName(file)
	testutil.FailErr(t, "validate unchanged child", err)
	if actual != file.Name() {
		t.Fatalf("native address=%q want %q", actual, file.Name())
	}
	testutil.FailErr(t, "move cached parent outside", os.Rename(filepath.Join(root, "parent"), filepath.Join(t.TempDir(), "outside")))
	testutil.FailErr(t, "replace parent", os.MkdirAll(filepath.Join(root, "parent", "child"), 0700))
	if _, err := reader.directoryPathFromName(file); !errors.Is(err, ErrSymlink) {
		t.Fatalf("fallback reused moved cached parent: %v", err)
	}
	testutil.FailErr(t, "close held child", file.Close())
	if _, err := reader.directoryPathFromName(file); !errors.Is(err, os.ErrClosed) {
		t.Fatalf("closed fallback=%v", err)
	}
	external, err := os.Open(t.TempDir())
	testutil.FailErr(t, "open external directory", err)
	defer external.Close()
	if _, err := reader.directoryPathFromName(external); !errors.Is(err, ErrSymlink) {
		t.Fatalf("outside fallback=%v", err)
	}
}

func TestDirectoryPathNativeSpellingMismatchUsesContainedIdentity(t *testing.T) {
	root := t.TempDir()
	testutil.FailErr(t, "create child", os.Mkdir(filepath.Join(root, "child"), 0700))
	reader, err := OpenReadRoot(root)
	testutil.FailErr(t, "open root", err)
	defer reader.Close()
	file, err := reader.OpenExact("child")
	testutil.FailErr(t, "open child", err)
	defer file.Close()
	// An alternate native spelling is an observed address, never string authority.
	native := filepath.Join(t.TempDir(), "alternate-native-spelling")
	actual, err := reader.directoryPathFromNative(file, native)
	testutil.FailErr(t, "validate equivalent held identity", err)
	if actual != file.Name() {
		t.Fatalf("admitted address=%q", actual)
	}
	moved := filepath.Join(t.TempDir(), "outside")
	testutil.FailErr(t, "move child outside", os.Rename(filepath.Join(root, "child"), moved))
	testutil.FailErr(t, "replace old spelling with outward link", os.Symlink(moved, filepath.Join(root, "child")))
	if _, err := reader.directoryPathFromNative(file, native); !errors.Is(err, ErrSymlink) {
		t.Fatalf("mismatch followed outward replacement: %v", err)
	}
}

func TestDirectoryPathFallbackRefusesMovedAndReplacedRoot(t *testing.T) {
	root := filepath.Join(t.TempDir(), "admitted")
	testutil.FailErr(t, "create child", os.MkdirAll(filepath.Join(root, "child"), 0700))
	reader, err := OpenReadRoot(root)
	testutil.FailErr(t, "open root", err)
	defer reader.Close()
	file, err := reader.OpenExact("child")
	testutil.FailErr(t, "open child", err)
	defer file.Close()
	moved := filepath.Join(t.TempDir(), "moved-root")
	testutil.FailErr(t, "move admitted root outside", os.Rename(root, moved))
	testutil.FailErr(t, "replace configured root", os.MkdirAll(filepath.Join(root, "child"), 0700))
	before := reader.DescriptorOpens()
	if _, err := reader.DirectoryPath(file); !errors.Is(err, ErrSymlink) {
		t.Fatalf("replaced root accepted stale identity: %v", err)
	}
	if reader.DescriptorOpens() != before {
		t.Fatal("root replacement opened stale descendants")
	}
	testutil.FailErr(t, "move retired root beneath replacement", os.Rename(moved, filepath.Join(root, "retired")))
	if _, err := reader.DirectoryPath(file); !errors.Is(err, ErrSymlink) {
		t.Fatalf("native prefix accepted replaced root: %v", err)
	}
}

func TestDirectoryIdentityFallbackHasConstantScratchDescriptorsAtDepth240(t *testing.T) {
	root := t.TempDir()
	rel := strings.TrimSuffix(strings.Repeat("d/", 240), "/")
	testutil.FailErr(t, "create deep chain", os.MkdirAll(filepath.Join(root, rel), 0700))
	reader, err := OpenReadRoot(root)
	testutil.FailErr(t, "open root", err)
	defer reader.Close()
	file, err := reader.OpenExact(rel)
	testutil.FailErr(t, "open deep directory", err)
	defer file.Close()
	before := reader.DescriptorOpens()
	actual, err := reader.directoryPathFromName(file)
	testutil.FailErr(t, "validate deep identity", err)
	if actual != file.Name() || reader.DescriptorOpens()-before != 241 {
		t.Fatalf("identity=%q opens=%d", actual, reader.DescriptorOpens()-before)
	}
	peak, held := reader.DirectoryIdentityDescriptors()
	if peak != 2 || held != 0 {
		t.Fatalf("scratch peak=%d held=%d", peak, held)
	}
	testutil.FailErr(t, "move first ancestor", os.Rename(filepath.Join(root, "d"), filepath.Join(t.TempDir(), "outside")))
	if _, err := reader.directoryPathFromName(file); err == nil {
		t.Fatal("missing canonical chain accepted")
	}
	_, held = reader.DirectoryIdentityDescriptors()
	if held != 0 {
		t.Fatalf("failed identity retained %d scratch descriptors", held)
	}
}

func TestConcurrentDirectoryIdentityValidationReportsRootAggregateWork(t *testing.T) {
	root := t.TempDir()
	rel := strings.TrimSuffix(strings.Repeat("d/", 32), "/")
	testutil.FailErr(t, "create directory chain", os.MkdirAll(filepath.Join(root, rel), 0700))
	reader, err := OpenReadRoot(root)
	testutil.FailErr(t, "open root", err)
	defer reader.Close()
	file, err := reader.OpenExact(rel)
	testutil.FailErr(t, "hold directory", err)
	defer file.Close()
	before := reader.DescriptorOpens()
	const workers = 8
	ready := make(chan struct{})
	outcomes := make(chan error, workers)
	for range workers {
		go func() { <-ready; _, err := reader.directoryPathFromName(file); outcomes <- err }()
	}
	close(ready)
	for range workers {
		testutil.FailErr(t, "validate concurrent identity", <-outcomes)
	}
	peak, held := reader.DirectoryIdentityDescriptors()
	if peak < 2 || peak > 2*workers || held != 0 || reader.DescriptorOpens()-before != workers*33 {
		t.Fatalf("aggregate peak=%d held=%d opens=%d", peak, held, reader.DescriptorOpens()-before)
	}
}
