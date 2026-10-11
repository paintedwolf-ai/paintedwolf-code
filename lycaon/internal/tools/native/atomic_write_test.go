package native

import (
	"errors"
	"github.com/lycaon/lycaon/internal/toolrejection"
	"os"
	"path/filepath"
	"testing"

	"github.com/lycaon/lycaon/internal/fseffect"
	"github.com/lycaon/lycaon/internal/textfile"
	"github.com/lycaon/lycaon/internal/tools"
)

// A planted staging symlink cannot redirect the write.
func TestApplyAgentFileIgnoresPlantedTempSymlink(t *testing.T) {
	project := t.TempDir()
	outside := filepath.Join(t.TempDir(), "outside.txt")
	if err := os.WriteFile(outside, []byte("original"), 0o644); err != nil {
		t.Fatalf("seed outside file: %v", err)
	}

	target := filepath.Join(project, "notes.md")
	if err := os.Symlink(outside, target+".tmp"); err != nil {
		t.Fatalf("plant symlink: %v", err)
	}

	if err := applyTestFile(t, target, []byte("in-project content\n"), nil, ""); err != nil {
		t.Fatalf("applyAgentFile: %v", err)
	}

	escaped, err := os.ReadFile(outside)
	if err != nil {
		t.Fatalf("read outside file: %v", err)
	}
	if string(escaped) != "original" {
		t.Fatalf("write escaped the project: outside file = %q", escaped)
	}
	written, err := os.ReadFile(target)
	if err != nil {
		t.Fatalf("read target: %v", err)
	}
	if string(written) != "in-project content\n" {
		t.Fatalf("target content = %q", written)
	}
}

// Replacement preserves the existing file mode.
func TestApplyAgentFileKeepsExistingMode(t *testing.T) {
	target := filepath.Join(t.TempDir(), "run.sh")
	if err := os.WriteFile(target, []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatalf("seed script: %v", err)
	}
	before := []byte("#!/bin/sh\n")
	if err := applyTestFile(t, target, []byte("#!/bin/sh\necho hi\n"), before, textfile.SHA256(before)); err != nil {
		t.Fatalf("applyAgentFile: %v", err)
	}
	info, err := os.Stat(target)
	if err != nil {
		t.Fatalf("stat: %v", err)
	}
	if info.Mode().Perm() != 0o755 {
		t.Fatalf("mode = %v want 0755", info.Mode().Perm())
	}
}

func TestApplyAgentFileRejectsChangedExistingBase(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	target := filepath.Join(dir, "notes.txt")
	original := []byte("opened bytes\n")
	changed := []byte("newer bytes\n")
	if err := os.WriteFile(target, changed, 0o644); err != nil {
		t.Fatalf("seed changed target: %v", err)
	}
	err := applyTestFile(t, target, []byte("agent replacement\n"), original, textfile.SHA256(original))
	assertTextWriteConflict(t, err)
	got, readErr := os.ReadFile(target)
	if readErr != nil {
		t.Fatalf("read conflicted target: %v", readErr)
	}
	if string(got) != string(changed) {
		t.Fatalf("conflict overwrote target: got %q want %q", got, changed)
	}
	assertNoReplacementTemps(t, dir)
}

func TestApplyAgentFileRejectsPathCreatedAfterResolution(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	target := filepath.Join(dir, "new.txt")
	created := []byte("created by another writer\n")
	if err := os.WriteFile(target, created, 0o644); err != nil {
		t.Fatalf("create raced target: %v", err)
	}
	err := applyTestFile(t, target, []byte("agent replacement\n"), nil, "")
	assertTextWriteConflict(t, err)
	got, readErr := os.ReadFile(target)
	if readErr != nil {
		t.Fatalf("read raced target: %v", readErr)
	}
	if string(got) != string(created) {
		t.Fatalf("conflict overwrote target: got %q want %q", got, created)
	}
	assertNoReplacementTemps(t, dir)
}

func applyTestFile(t *testing.T, target string, after, before []byte, baseSHA256 string) error {
	t.Helper()
	return applyAgentFile(t.Context(), tools.ToolContext{}, testMutationTarget(target), after, before, baseSHA256)
}

func testMutationTarget(path string) mutationTarget {
	return mutationTarget{
		Abs:      path,
		Location: fseffect.Location{Root: filepath.Dir(path), Rel: filepath.Base(path)},
	}
}

func assertTextWriteConflict(t *testing.T, err error) {
	t.Helper()
	var reject *toolrejection.ToolReject
	if !errors.As(err, &reject) || reject.Code != "TEXT_WRITE_CONFLICT" {
		t.Fatalf("error = %v, want TEXT_WRITE_CONFLICT", err)
	}
}

func assertNoReplacementTemps(t *testing.T, dir string) {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("read temp directory: %v", err)
	}
	if len(entries) != 1 {
		t.Fatalf("replacement temp leaked: entries=%v", entries)
	}
}

// A symlinked destination child cannot redirect extraction.
func TestPathWithinDirRejectsSymlinkedSubdir(t *testing.T) {
	dest := t.TempDir()
	outside := t.TempDir()
	if err := os.Symlink(outside, filepath.Join(dest, "sub")); err != nil {
		t.Fatalf("plant symlink: %v", err)
	}
	if pathWithinDir(dest, filepath.Join(dest, "sub", "evil.plist")) {
		t.Fatal("entry resolving outside dest must be rejected")
	}
	if !pathWithinDir(dest, filepath.Join(dest, "ok", "file.txt")) {
		t.Fatal("ordinary nested entry must be allowed")
	}
}
