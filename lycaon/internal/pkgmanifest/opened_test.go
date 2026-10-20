package pkgmanifest_test

import (
	"errors"
	"io"
	"os"
	"testing"

	"github.com/lycaon/lycaon/internal/pkgmanifest"
	"github.com/lycaon/lycaon/internal/testutil"
)

func TestVerifyOpenedRetainsTheSelectedInodeAndCallerOffset(t *testing.T) {
	manifestPath, path := fixture(t, nil)
	manifest, err := pkgmanifest.Load(manifestPath)
	testutil.FailErr(t, "load manifest", err)
	expected, ok := manifest.Lookup(path)
	if !ok {
		t.Fatal("fixture executable absent")
	}
	file, err := os.Open(path)
	testutil.FailErr(t, "retain executable descriptor", err)
	defer file.Close()
	_, err = file.Seek(5, io.SeekStart)
	testutil.FailErr(t, "set caller offset", err)
	testutil.FailErr(t, "unlink selected pathname", os.Remove(path))
	testutil.FailErr(t, "replace pathname", os.WriteFile(path, []byte("replacement"), 0o755))
	testutil.FailErr(t, "verify retained image", pkgmanifest.VerifyOpened(file, expected))
	position, err := file.Seek(0, io.SeekCurrent)
	testutil.FailErr(t, "read caller offset", err)
	if position != 5 {
		t.Fatalf("offset = %d, want 5", position)
	}
	if manifest.Verify(path) == nil {
		t.Fatal("path verifier accepted replacement image")
	}
	if _, err := file.Stat(); err != nil {
		t.Fatalf("descriptor closed by verifier: %v", err)
	}
}

func TestVerifyOpenedRejectsInvalidExpectationsAndUnavailableFiles(t *testing.T) {
	manifestPath, path := fixture(t, nil)
	manifest, err := pkgmanifest.Load(manifestPath)
	testutil.FailErr(t, "load manifest", err)
	expected, _ := manifest.Lookup(path)
	file, err := os.Open(path)
	testutil.FailErr(t, "retain executable", err)
	defer file.Close()
	for _, field := range []string{"path", "digest", "size"} {
		t.Run(field, func(t *testing.T) {
			invalid := expected
			switch field {
			case "path":
				invalid.Path = "relative"
			case "digest":
				invalid.SHA256 = "bad"
			case "size":
				invalid.Size = -1
			}
			if pkgmanifest.VerifyOpened(file, invalid) == nil {
				t.Fatal("accepted invalid expectation")
			}
		})
	}
	if pkgmanifest.VerifyOpened(nil, expected) == nil {
		t.Fatal("accepted missing descriptor")
	}
	directory, err := os.Open(t.TempDir())
	testutil.FailErr(t, "open directory", err)
	defer directory.Close()
	if pkgmanifest.VerifyOpened(directory, expected) == nil {
		t.Fatal("accepted directory image")
	}
	closed, err := os.Open(path)
	testutil.FailErr(t, "open closing fixture", err)
	testutil.FailErr(t, "close fixture", closed.Close())
	if err := pkgmanifest.VerifyOpened(closed, expected); !errors.Is(err, os.ErrClosed) {
		t.Fatalf("closed descriptor = %v, want os.ErrClosed", err)
	}
}
