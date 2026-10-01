package sourceblob

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"testing"

	"github.com/lycaon/lycaon/internal/testutil"
)

func TestCopyRootFileRetainsExactlyTheCopiedBytes(t *testing.T) {
	store := New(t.TempDir())
	root, err := os.OpenRoot(t.TempDir())
	testutil.FailErr(t, "open source", err)
	defer func() { _ = root.Close() }()
	body := bytes.Repeat([]byte("one transfer\n"), 10000)
	testutil.FailErr(t, "write source", root.WriteFile("file", body, 0o600))
	var copied bytes.Buffer
	capture, err := store.CopyRootFile(t.Context(), root, "file", &copied)
	testutil.FailErr(t, "capture and copy", err)
	var retained bytes.Buffer
	testutil.FailErr(t, "verify retained object", store.CopySHA(t.Context(), capture.SHA256, &retained))
	if !bytes.Equal(copied.Bytes(), body) || !bytes.Equal(retained.Bytes(), body) {
		t.Fatal("copied and retained bytes differ")
	}
}

func TestCaptureRepairsAnExistingDamagedObject(t *testing.T) {
	store := New(t.TempDir())
	body := []byte("complete recovery")
	sha := ContentSHA(body)
	rel, _, _, err := store.Put(sha, body)
	testutil.FailErr(t, "seed object", err)
	damaged, err := os.ReadFile(filepath.Join(store.Root(), rel))
	testutil.FailErr(t, "read encoded object", err)
	damaged[len(damaged)-1] ^= 0xff
	testutil.FailErr(t, "damage existing object without changing its size", os.WriteFile(filepath.Join(store.Root(), rel), damaged, 0o600))
	path := filepath.Join(t.TempDir(), "file")
	testutil.FailErr(t, "seed source", os.WriteFile(path, body, 0o600))
	capture, err := store.PutFile(t.Context(), path)
	testutil.FailErr(t, "capture complete bytes", err)
	if capture.SHA256 != sha {
		t.Fatal("capture changed content identity")
	}
	testutil.FailErr(t, "verify repaired recovery", store.CopySHA(t.Context(), sha, io.Discard))
	_, err = store.PutFile(t.Context(), path)
	testutil.FailErr(t, "recapture immutable object", err)
}

type captureFailureWriter struct{ err error }

func (w captureFailureWriter) Write([]byte) (int, error) { return 0, w.err }

func TestCopyFailureDoesNotPublishRecovery(t *testing.T) {
	store := New(t.TempDir())
	root, err := os.OpenRoot(t.TempDir())
	testutil.FailErr(t, "open source", err)
	defer func() { _ = root.Close() }()
	body := []byte("retained only on success")
	testutil.FailErr(t, "seed source", root.WriteFile("file", body, 0o600))
	failure := errors.New("destination full")
	_, err = store.CopyRootFile(t.Context(), root, "file", captureFailureWriter{failure})
	if !errors.Is(err, failure) {
		t.Fatalf("destination error = %v", err)
	}
	assertCaptureUnpublished(t, store)
}

type cancelingWriter struct{ cancel context.CancelFunc }

func (w cancelingWriter) Write(p []byte) (int, error) {
	w.cancel()
	return len(p), nil
}

func TestCopyCancellationDoesNotPublishRecovery(t *testing.T) {
	store := New(t.TempDir())
	root, err := os.OpenRoot(t.TempDir())
	testutil.FailErr(t, "open source", err)
	defer func() { _ = root.Close() }()
	testutil.FailErr(t, "seed source", root.WriteFile("file", make([]byte, 1<<20), 0o600))
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	_, err = store.CopyRootFile(ctx, root, "file", cancelingWriter{cancel})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("cancellation = %v", err)
	}
	assertCaptureUnpublished(t, store)
}

func assertCaptureUnpublished(t *testing.T, store *Store) {
	t.Helper()
	err := filepath.WalkDir(store.Root(), func(path string, entry os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if !entry.IsDir() {
			t.Errorf("failed capture retained a file: %s", path)
		}
		return nil
	})
	testutil.FailErr(t, "inspect failed capture storage", err)
}
