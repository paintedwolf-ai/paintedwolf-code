package modelstore

import (
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/testutil"
)

type payloadReaderFunc func([]byte) (int, error)

func (f payloadReaderFunc) Read(p []byte) (int, error) { return f(p) }

func TestSealedPayloadClosesBeforePublication(t *testing.T) {
	file, err := os.CreateTemp(t.TempDir(), "payload")
	testutil.FailErr(t, "create staged payload", err)
	want := testArtifact(t, []byte("weights"))
	testutil.FailErr(t, "seal staged payload", sealStagedPayload(file, strings.NewReader("weights"), want))
	if _, err := file.Stat(); !errors.Is(err, os.ErrClosed) {
		t.Fatalf("sealed descriptor should be closed: %v", err)
	}
	info, err := os.Stat(file.Name())
	testutil.FailErr(t, "inspect sealed payload", err)
	if info.Mode().Perm() != 0o400 {
		t.Fatalf("sealed permissions: %v", info.Mode())
	}
}

func TestPayloadPreservesReadAndCloseFailures(t *testing.T) {
	file, err := os.CreateTemp(t.TempDir(), "payload")
	testutil.FailErr(t, "create staged payload", err)
	readErr := errors.New("payload source failed")
	reader := payloadReaderFunc(func([]byte) (int, error) {
		testutil.FailErr(t, "close interrupted payload", file.Close())
		return 0, readErr
	})
	err = sealStagedPayload(file, reader, testArtifact(t, []byte("weights")))
	if !errors.Is(err, readErr) || !errors.Is(err, os.ErrClosed) {
		t.Fatalf("missing read or close cause: %v", err)
	}
}

func TestStagePreservesSourceAndCleanupFailures(t *testing.T) {
	store := newStore(t)
	want := testArtifact(t, []byte("weights"))
	reader := payloadReaderFunc(func([]byte) (int, error) {
		dir := filepath.Join(store.root, "artifacts", want.Digest[:2])
		entries, err := os.ReadDir(dir)
		testutil.FailErr(t, "find staging file", err)
		if len(entries) != 1 {
			t.Fatalf("staging entries: %d", len(entries))
		}
		path := filepath.Join(dir, entries[0].Name())
		testutil.FailErr(t, "replace staging entry", os.Remove(path))
		testutil.FailErr(t, "create obstructing directory", os.Mkdir(path, 0o700))
		testutil.FailErr(t, "populate obstructing directory", os.Mkdir(filepath.Join(path, "child"), 0o700))
		return 0, io.ErrUnexpectedEOF
	})
	err := store.Stage(reader, want)
	var pathErr *os.PathError
	if !errors.Is(err, io.ErrUnexpectedEOF) || !errors.As(err, &pathErr) || pathErr.Op != "remove" {
		t.Fatalf("missing source or cleanup cause: %v", err)
	}
	if store.Verified(want.Digest) {
		t.Fatal("failed stage published an artifact")
	}
}

func TestManifestEqualityIncludesPayloadAndIdentity(t *testing.T) {
	first := Artifact{Name: "first", Digest: "one", Format: "config", Bytes: 1}
	second := Artifact{Name: "second", Digest: "two", Format: "tokenizer", Bytes: 2}
	base := Manifest{Model: "model", Revision: "revision", Device: "cpu", Artifact: first, Files: []Artifact{first, second}}
	changes := map[string]func(*Manifest){
		"model":    func(m *Manifest) { m.Model = "other" },
		"revision": func(m *Manifest) { m.Revision = "other" },
		"device":   func(m *Manifest) { m.Device = "other" },
		"payload":  func(m *Manifest) { m.Artifact.Bytes++ },
		"files":    func(m *Manifest) { m.Files = []Artifact{second, first} },
	}
	if !sameManifest(base, base) {
		t.Fatal("identical manifest differs")
	}
	for name, change := range changes {
		t.Run(name, func(t *testing.T) {
			changed := base
			change(&changed)
			if sameManifest(base, changed) {
				t.Fatal("changed manifest matched")
			}
		})
	}
	base.Files = nil
	empty := base
	empty.Files = []Artifact{}
	if !sameManifest(base, empty) {
		t.Fatal("omitted supplementary files differ")
	}
}
