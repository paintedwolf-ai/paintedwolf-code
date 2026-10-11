package projectsource

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/lycaon/lycaon/internal/testutil"
)

func TestSourceFingerprintNeedsNoParentReadAccess(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("POSIX directory permissions")
	}
	parent := t.TempDir()
	testutil.FailErr(t, "seed directory", os.Mkdir(filepath.Join(parent, "folder"), 0o750))
	testutil.FailErr(t, "seed file", os.WriteFile(filepath.Join(parent, "file"), []byte("café 🐺"), 0o640))
	testutil.FailErr(t, "seed nested file", os.WriteFile(filepath.Join(parent, "folder", "child"), []byte("nested"), 0o640))
	testutil.FailErr(t, "seed dangling link", os.Symlink("missing", filepath.Join(parent, "link")))
	want := map[string]string{}
	for _, name := range []string{"file", "folder", "link"} {
		value, err := sourceTreeFingerprint(t.Context(), filepath.Join(parent, name))
		testutil.FailErr(t, "fingerprint accessible entry", err)
		want[name] = value
	}
	t.Cleanup(func() { testutil.FailErr(t, "restore parent permissions", os.Chmod(parent, 0o750)) })
	testutil.FailErr(t, "make parent searchable only", os.Chmod(parent, 0o110))
	if dir, err := os.Open(parent); err == nil {
		_ = dir.Close()
		t.Skip("process can bypass parent read permissions")
	}
	for name, expected := range want {
		got, err := sourceTreeFingerprint(t.Context(), filepath.Join(parent, name))
		testutil.FailErr(t, "fingerprint with unreadable parent", err)
		if got != expected {
			t.Fatalf("%s fingerprint = %s, want %s", name, got, expected)
		}
	}
}

func TestSourceFingerprintSeparatesFileBoundaries(t *testing.T) {
	one, two := t.TempDir(), t.TempDir()
	testutil.FailErr(t, "match first root mode", os.Chmod(one, 0o700))
	testutil.FailErr(t, "match second root mode", os.Chmod(two, 0o700))
	// File contents cannot encode another entry in the tree digest.
	testutil.FailErr(t, "seed one file", os.WriteFile(filepath.Join(one, "a"), []byte(fmt.Sprintf("firstb\x00%d\x00second", 0o600)), 0o600))
	testutil.FailErr(t, "seed first file", os.WriteFile(filepath.Join(two, "a"), []byte("first"), 0o600))
	testutil.FailErr(t, "seed second file", os.WriteFile(filepath.Join(two, "b"), []byte("second"), 0o600))
	a, err := sourceTreeFingerprint(t.Context(), one)
	testutil.FailErr(t, "hash one file", err)
	b, err := sourceTreeFingerprint(t.Context(), two)
	testutil.FailErr(t, "hash two files", err)
	if a == b {
		t.Fatal("different trees share a structural fingerprint")
	}
}

func TestSourceFingerprintRecordsFramedContentHash(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("recorded POSIX mode")
	}
	path := filepath.Join(t.TempDir(), "renamed café 🐺.md")
	testutil.FailErr(t, "seed file", os.WriteFile(path, []byte("# External lifecycle\n\nExternal change must survive until resolved.\n"), 0o644))
	testutil.FailErr(t, "set recorded mode", os.Chmod(path, 0o644))
	got, err := sourceTreeFingerprint(t.Context(), path)
	testutil.FailErr(t, "fingerprint recorded content", err)
	const want = "e81f3dbb078ad96111ec96b0e07315d7268272961eb6f5611ae633f2dc41401b"
	if got != want {
		t.Fatalf("fingerprint = %s, want recorded %s", got, want)
	}
}

func TestSourceFingerprintRejectsReplacedEntryBeforeReading(t *testing.T) {
	dir := t.TempDir()
	path, replacement := filepath.Join(dir, "original"), filepath.Join(dir, "replacement")
	testutil.FailErr(t, "seed original", os.WriteFile(path, []byte("original"), 0o640))
	testutil.FailErr(t, "seed replacement", os.WriteFile(replacement, []byte("must not be read"), 0o640))
	info, err := os.Lstat(path)
	testutil.FailErr(t, "stat original", err)
	var output bytes.Buffer
	err = fingerprintSourceEntry(t.Context(), &output, ".", info,
		func() (*os.File, error) { return os.Open(replacement) },
		func() (string, error) { t.Fatal("regular file read as symlink"); return "", nil }, newSourceWorkProgress(t.Context(), "verifying"))
	if !errors.Is(err, ErrSourceMutationDiverged) {
		t.Fatalf("replacement error = %v", err)
	}
	if bytes.Contains(output.Bytes(), []byte("must not be read")) {
		t.Fatal("fingerprint read replacement content")
	}
}
