package configlayout_test

import (
	"path/filepath"
	"runtime"
	"testing"

	"github.com/lycaon/lycaon/internal/configlayout"
)

func TestFindModuleRootFromModuleDir(t *testing.T) {
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("runtime.Caller failed")
	}
	root := filepath.Clean(filepath.Join(filepath.Dir(file), "..", ".."))
	t.Chdir(root)
	got := configlayout.FindModuleRoot()
	if !filepath.IsAbs(got) {
		t.Fatalf("module root = %q, want absolute path", got)
	}
	if got != root {
		t.Fatalf("module root = %q, want %q", got, root)
	}
}

func TestFindModuleRootFromNestedDir(t *testing.T) {
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("runtime.Caller failed")
	}
	root := filepath.Clean(filepath.Join(filepath.Dir(file), "..", ".."))
	if got := configlayout.FindModuleRootFrom(filepath.Dir(file)); got != root {
		t.Fatalf("FindModuleRootFrom(internal/configlayout) = %q, want %q", got, root)
	}
}

func TestIsModuleRoot(t *testing.T) {
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("runtime.Caller failed")
	}
	root := filepath.Clean(filepath.Join(filepath.Dir(file), "..", ".."))
	if !configlayout.IsModuleRoot(root) {
		t.Fatalf("IsModuleRoot(%q) = false, want true", root)
	}
	if configlayout.IsModuleRoot("") {
		t.Fatal(`IsModuleRoot("") = true, want false`)
	}
	if configlayout.IsModuleRoot(t.TempDir()) {
		t.Fatal("IsModuleRoot(temp) = true, want false")
	}
	// A matching basename alone does not identify a module root.
	ghost := filepath.Join(t.TempDir(), "lycaon")
	if configlayout.IsModuleRoot(ghost) {
		t.Fatalf("IsModuleRoot(%q) = true, want false", ghost)
	}
}

// Repository-root launches resolve the adjacent module directory.
func TestFindModuleRootFromRepositoryRootIsAbsolute(t *testing.T) {
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("runtime.Caller failed")
	}
	moduleDir := filepath.Clean(filepath.Join(filepath.Dir(file), "..", ".."))
	t.Chdir(filepath.Dir(moduleDir))

	root := configlayout.FindModuleRoot()
	if !filepath.IsAbs(root) {
		t.Fatalf("FindModuleRoot = %q, want an absolute path", root)
	}
	if root != moduleDir {
		t.Fatalf("FindModuleRoot = %q, want %q", root, moduleDir)
	}
}

// The bundled-only marker denotes an absent disk tree.
func TestBundledOnlyRootIsNotAModuleRoot(t *testing.T) {
	t.Chdir(t.TempDir())
	if root := configlayout.FindModuleRoot(); root != configlayout.BundledOnlyRoot {
		t.Fatalf("FindModuleRoot = %q, want the bundled marker %q", root, configlayout.BundledOnlyRoot)
	}
	if configlayout.IsModuleRoot(configlayout.BundledOnlyRoot) {
		t.Fatal("the bundled marker must not read as a module root")
	}
}
