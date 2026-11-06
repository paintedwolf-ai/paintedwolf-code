package fspath

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/lycaon/lycaon/internal/testutil"
)

func TestCanonicalizeExistingMatchesTheGeneralResolver(t *testing.T) {
	base, err := filepath.EvalSymlinks(t.TempDir())
	testutil.FailErr(t, "filepath.EvalSymlinks failed", err)
	real := filepath.Join(base, "real", "nested")
	if err := os.MkdirAll(real, 0o700); err != nil {
		testutil.FailErr(t, "create directory", err)
	}
	file := filepath.Join(real, "file")
	if err := os.WriteFile(file, nil, 0o600); err != nil {
		testutil.FailErr(t, "write file", err)
	}
	for link, target := range map[string]string{"to-dir": real, "to-file": file, "dangling": filepath.Join(base, "absent"), "relative": "real"} {
		if err := os.Symlink(target, filepath.Join(base, link)); err != nil {
			testutil.FailErr(t, "os.Symlink failed", err)
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

func TestLinkFreePathAllocatesNothing(t *testing.T) {
	base, err := filepath.EvalSymlinks(t.TempDir())
	testutil.FailErr(t, "filepath.EvalSymlinks failed", err)
	deep := filepath.Join(base, "a", "b", "c", "d")
	if err := os.MkdirAll(deep, 0o700); err != nil {
		testutil.FailErr(t, "create directory", err)
	}
	allocations := testing.AllocsPerRun(50, func() {
		if !linkFreePath(deep) {
			t.Fatal("expected a link-free path")
		}
	})
	if allocations != 0 {
		t.Fatalf("allocations = %v", allocations)
	}
}
