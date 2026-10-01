package main

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/testutil"
)

func TestLogsSiblingBesideFindsNeighbor(t *testing.T) {
	dir := t.TempDir()
	engine := filepath.Join(dir, "pw")
	sibling := filepath.Join(dir, logsSiblingName)
	if runtime.GOOS == "windows" {
		engine += ".exe"
		sibling += ".exe"
	}
	testutil.FailErr(t, "write engine stub", os.WriteFile(engine, []byte("x"), 0o755))
	testutil.FailErr(t, "write logs stub", os.WriteFile(sibling, []byte("x"), 0o755))

	got, err := logsSiblingBeside(engine)
	testutil.FailErr(t, "resolve sibling", err)
	want, err := filepath.EvalSymlinks(sibling)
	testutil.FailErr(t, "eval sibling", err)
	if filepath.Clean(got) != filepath.Clean(want) {
		t.Fatalf("got %q, want %q", got, want)
	}
}

func TestLogsSiblingBesideMissing(t *testing.T) {
	dir := t.TempDir()
	engine := filepath.Join(dir, "pw")
	testutil.FailErr(t, "write engine stub", os.WriteFile(engine, []byte("x"), 0o755))

	_, err := logsSiblingBeside(engine)
	if err == nil {
		t.Fatal("expected missing sibling error")
	}
	if !strings.Contains(err.Error(), logsSiblingName) {
		t.Fatalf("error should name the sibling, got %v", err)
	}
}

func TestLogsSiblingInsideMacOSHostBundle(t *testing.T) {
	if runtime.GOOS != "darwin" {
		return
	}
	contents := filepath.Join(t.TempDir(), "Painted Wolf Code.app", "Contents")
	engine := filepath.Join(contents, "Helpers", "Painted Wolf Code engine.app", "Contents", "MacOS", "pw")
	logs := filepath.Join(contents, "MacOS", "pw-logs")
	for _, path := range []string{engine, logs} {
		testutil.FailErr(t, "create bundle directory", os.MkdirAll(filepath.Dir(path), 0o755))
		testutil.FailErr(t, "write executable", os.WriteFile(path, []byte("x"), 0o755))
	}
	link := filepath.Join(t.TempDir(), "pw")
	testutil.FailErr(t, "link engine", os.Symlink(engine, link))
	got, err := logsSiblingBeside(link)
	testutil.FailErr(t, "resolve bundled logs", err)
	want, err := filepath.EvalSymlinks(logs)
	testutil.FailErr(t, "resolve expected logs", err)
	if got != want {
		t.Fatalf("logs path = %q, want %q", got, want)
	}
}
