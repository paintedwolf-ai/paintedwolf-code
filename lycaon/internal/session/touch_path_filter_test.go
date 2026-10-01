package session

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/lycaon/lycaon/internal/testutil"
)

func TestFilterTouchPathsForProjectDropsMissingPrefix(t *testing.T) {
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, "internal", "auth"), 0o755); err != nil {
		testutil.FailErr(t, "create directory", err)
	}
	got := FilterTouchPathsForProject(dir, []string{"internal/**", "lycaon/**", "lycaon-den/**"})
	if len(got) != 1 || got[0] != "internal/**" {
		t.Fatalf("got %v", got)
	}
}

func TestFilterTouchPathsForProjectKeepsRepoWide(t *testing.T) {
	dir := t.TempDir()
	got := FilterTouchPathsForProject(dir, []string{"**"})
	if len(got) != 1 || got[0] != "**" {
		t.Fatalf("got %v", got)
	}
}

func TestTouchPathAppliesToProjectGlob(t *testing.T) {
	dir := t.TempDir()
	py := filepath.Join(dir, "main.py")
	if err := os.WriteFile(py, []byte("x"), 0o644); err != nil {
		testutil.FailErr(t, "write file", err)
	}
	if !TouchPathAppliesToProject(dir, "**/*.py") {
		t.Fatal("expected **/*.py to match")
	}
	if TouchPathAppliesToProject(dir, "lycaon/**") {
		t.Fatal("expected lycaon/** to miss")
	}
}
