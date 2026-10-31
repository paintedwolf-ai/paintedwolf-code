package main

import (
	"os"
	"path/filepath"
	"testing"
)

func TestResolveModulePathsRejectsEscape(t *testing.T) {
	moduleRoot := filepath.Join("..", "..")
	paths, err := resolveModulePaths(moduleRoot)
	if err != nil {
		t.Fatalf("resolveModulePaths: %v", err)
	}
	outside := filepath.Join(paths.root, "..", "..", "etc", "passwd")
	if err := assertPathUnderRoot(outside, paths.root); err == nil {
		t.Fatalf("expected escape rejection for %q", outside)
	}
}

func TestResolveModulePathsAllowlist(t *testing.T) {
	moduleRoot := filepath.Join("..", "..")
	paths, err := resolveModulePaths(moduleRoot)
	if err != nil {
		t.Fatalf("resolveModulePaths: %v", err)
	}
	for _, path := range []string{paths.languageDir} {
		if _, err := os.Stat(path); err != nil {
			t.Fatalf("expected tracked path %q: %v", path, err)
		}
	}
	if _, err := resolveModulePaths(t.TempDir()); err == nil {
		t.Fatal("expected missing module root directory rejection")
	}
	if err := assertRegularFile(filepath.Join(t.TempDir(), "missing")); err == nil {
		t.Fatal("expected missing regular file rejection")
	}
	if err := assertRegularFile(t.TempDir()); err == nil {
		t.Fatal("expected directory regular-file rejection")
	}
}
