package pkgmanifest_test

import (
	"bytes"
	"errors"
	"os"
	"reflect"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/pkgmanifest"
	"github.com/lycaon/lycaon/internal/testutil"
)

type failedManifestReader struct{ err error }

func (r failedManifestReader) Read([]byte) (int, error) { return 0, r.err }

func TestReadRetainsTheSelectedManifestDescriptor(t *testing.T) {
	path, _ := fixture(t, nil)
	expected, err := pkgmanifest.Load(path)
	testutil.FailErr(t, "read expected manifest", err)
	file, err := os.Open(path)
	testutil.FailErr(t, "retain manifest descriptor", err)
	defer file.Close()
	testutil.FailErr(t, "unlink manifest path", os.Remove(path))
	testutil.FailErr(t, "replace manifest path", os.WriteFile(path, []byte("{}"), 0o644))
	actual, err := pkgmanifest.Read(file)
	testutil.FailErr(t, "decode retained manifest", err)
	if !reflect.DeepEqual(actual, expected) {
		t.Fatalf("retained manifest = %+v, want %+v", actual, expected)
	}
	if _, err := pkgmanifest.Load(path); err == nil {
		t.Fatal("path decoder accepted replacement manifest")
	}
	if _, err := file.Stat(); err != nil {
		t.Fatalf("descriptor closed by decoder: %v", err)
	}
}

func TestReadUsesTheBoundedStrictManifestContract(t *testing.T) {
	path, _ := fixture(t, nil)
	raw, err := os.ReadFile(path)
	testutil.FailErr(t, "read fixture", err)
	unknown := bytes.Replace(raw, []byte(`"package"`), []byte(`"unknown"`), 1)
	for _, data := range [][]byte{append(append([]byte{}, raw...), []byte("{}")...), unknown,
		[]byte(`{"format_version":7}`), bytes.Repeat([]byte(" "), (4<<20)+1)} {
		if _, err := pkgmanifest.Read(bytes.NewReader(data)); err == nil {
			t.Fatal("accepted invalid or oversized manifest")
		}
	}
	if _, err := pkgmanifest.Read(nil); err == nil {
		t.Fatal("accepted missing reader")
	}
	failure := errors.New("fixture read failed")
	if _, err := pkgmanifest.Read(failedManifestReader{failure}); !errors.Is(err, failure) {
		t.Fatalf("reader error = %v, want original error", err)
	}
	if _, err := pkgmanifest.Read(strings.NewReader("{}")); err == nil {
		t.Fatal("accepted invalid shape")
	}
}
