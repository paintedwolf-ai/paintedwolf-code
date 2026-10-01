package integration

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/lycaon/lycaon/internal/scan"
	"github.com/lycaon/lycaon/internal/scan/obligation"
	"github.com/lycaon/lycaon/internal/testutil"
)

func TestPathScopedTargetPreservesExactPaths(t *testing.T) {
	dir := t.TempDir()
	mustWrite(t, filepath.Join(dir, "src", "pkg", "a.go"), "package pkg\n")
	mustWrite(t, filepath.Join(dir, "src", "pkg", "b.go"), "package pkg\n")
	got := obligation.PathScopedTarget(dir, []string{"src/pkg/a.go", "src/pkg/b.go"})
	if len(got) != 2 || got[0] != "src/pkg/a.go" || got[1] != "src/pkg/b.go" {
		t.Fatalf("exact targets = %#v", got)
	}
}

func TestPathScopedTargetDoesNotPromoteScatteredPaths(t *testing.T) {
	dir := t.TempDir()
	mustWrite(t, filepath.Join(dir, "src", "a.go"), "package src\n")
	mustWrite(t, filepath.Join(dir, "lib", "b.go"), "package lib\n")
	got := obligation.PathScopedTarget(dir, []string{"src/a.go", "lib/b.go"})
	if len(got) != 2 || got[0] != "lib/b.go" || got[1] != "src/a.go" {
		t.Fatalf("scattered exact targets = %#v", got)
	}
}

func TestPathScopedTargetDoesNotProbeDeletedPaths(t *testing.T) {
	dir := t.TempDir()
	got := obligation.PathScopedTarget(dir, []string{"removed.go"})
	if len(got) != 1 || got[0] != "removed.go" {
		t.Fatalf("deleted target normalization = %#v", got)
	}
}

func TestEffectiveScanTargetsPreservesCompleteScope(t *testing.T) {
	root := "/tmp/project"
	targets := []string{"/tmp/project/src", "/tmp/project/lib", "/tmp/project/src"}
	got := scan.EffectiveScanTargets(root, targets)
	if len(got) != 2 || got[0] != targets[0] || got[1] != targets[1] {
		t.Fatalf("got %#v", got)
	}
	if got := scan.EffectiveScanTargets(root, nil); len(got) != 1 || got[0] != root {
		t.Fatalf("root fallback = %#v", got)
	}
	if got := scan.EffectiveScanTargets(root, []string{"src/a"}); len(got) != 1 || got[0] != "/tmp/project/src/a" {
		t.Fatalf("relative target = %#v", got)
	}
}

func mustWrite(t *testing.T, path, body string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		testutil.FailErr(t, "create directory", err)
	}
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		testutil.FailErr(t, "write file", err)
	}
}
