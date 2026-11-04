package fspath

import (
	"os"
	"path/filepath"
	"testing"
)

func TestCanonicalizeExistingMatchesTheGeneralResolver(t *testing.T) {
	base, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	real := filepath.Join(base, "real", "nested")
	if err := os.MkdirAll(real, 0o700); err != nil {
		t.Fatal(err)
	}
	file := filepath.Join(real, "file")
	if err := os.WriteFile(file, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	for link, target := range map[string]string{"to-dir": real, "to-file": file, "dangling": filepath.Join(base, "absent"), "relative": "real"} {
		if err := os.Symlink(target, filepath.Join(base, link)); err != nil {
			t.Fatal(err)
		}
	}
	for _, path := range []string{
		"/", base, real, file,
		filepath.Join(base, "to-dir"),
		filepath.Join(base, "to-dir", "file"),
		filepath.Join(base, "to-file"),
		filepath.Join(base, "relative", "nested"),
		filepath.Join(base, "dangling"),
		filepath.Join(base, "absent"),
		filepath.Join(file, "under-a-file"),
	} {
		want, wantErr := filepath.EvalSymlinks(path)
		got, ok := canonicalizeExisting(path)
		if ok != (wantErr == nil) || (ok && got != want) {
			t.Errorf("%s: got %q ok=%v, resolver %q err=%v", path, got, ok, want, wantErr)
		}
	}
}

func TestLinkFreePathCostsAtMostOneAllocationPerComponent(t *testing.T) {
	base, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	deep := filepath.Join(base, "a", "b", "c", "d")
	if err := os.MkdirAll(deep, 0o700); err != nil {
		t.Fatal(err)
	}
	components := 0
	for _, c := range deep {
		if c == filepath.Separator {
			components++
		}
	}
	allocations := testing.AllocsPerRun(50, func() {
		if !linkFreePath(deep) {
			t.Fatal("expected a link-free path")
		}
	})
	if allocations > float64(components) {
		t.Fatalf("allocations = %v for %d components", allocations, components)
	}
}
