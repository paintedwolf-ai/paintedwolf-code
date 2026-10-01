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
	"github.com/lycaon/lycaon/internal/zstdcodec"
)

func TestCopySHAVerifiesContent(t *testing.T) {
	store := New(t.TempDir())
	body := []byte("complete content\n")
	sha := ContentSHA(body)
	rel, _, _, err := store.Put(sha, body)
	testutil.FailErr(t, "put object", err)
	var out bytes.Buffer
	testutil.FailErr(t, "stream object", store.CopySHA(t.Context(), sha, &out))
	if !bytes.Equal(out.Bytes(), body) {
		t.Fatal("stream changed content")
	}
	compressed, err := zstdcodec.Compress(bytes.NewReader([]byte("different content")))
	testutil.FailErr(t, "compress corrupt object", err)
	testutil.FailErr(t, "replace corrupt object", os.WriteFile(filepath.Join(store.Root(), rel), compressed, 0o600))
	if err := store.CopySHA(t.Context(), sha, io.Discard); err == nil {
		t.Fatal("corrupt object accepted")
	}
}

func TestCanceledBlobCaptureDoesNotPublish(t *testing.T) {
	store := New(t.TempDir())
	path := filepath.Join(t.TempDir(), "large")
	testutil.FailErr(t, "write source", os.WriteFile(path, make([]byte, 1<<20), 0o600))
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if _, err := store.PutFile(ctx, path); !errors.Is(err, context.Canceled) {
		t.Fatalf("capture error=%v", err)
	}
	assertCaptureUnpublished(t, store)
}

func TestRootCaptureKeepsHeldDirectoryWhenItsPathIsReplaced(t *testing.T) {
	store := New(t.TempDir())
	parent := t.TempDir()
	selected := filepath.Join(parent, "selected")
	testutil.FailErr(t, "create selected directory", os.Mkdir(selected, 0o700))
	testutil.FailErr(t, "seed selected file", os.WriteFile(filepath.Join(selected, "file"), []byte("original"), 0o600))
	root, err := os.OpenRoot(selected)
	testutil.FailErr(t, "hold selected directory", err)
	defer func() { _ = root.Close() }()
	testutil.FailErr(t, "move selected directory", os.Rename(selected, filepath.Join(parent, "moved")))
	outside := t.TempDir()
	testutil.FailErr(t, "seed outside file", os.WriteFile(filepath.Join(outside, "file"), []byte("external"), 0o600))
	testutil.FailErr(t, "replace directory with outside link", os.Symlink(outside, selected))
	captured, err := store.CopyRootFile(t.Context(), root, "file", nil)
	testutil.FailErr(t, "capture held file", err)
	body, err := store.GetSHA(captured.SHA256)
	testutil.FailErr(t, "read captured object", err)
	if string(body) != "original" {
		t.Fatalf("captured replacement instead of held directory: %q", body)
	}
}
