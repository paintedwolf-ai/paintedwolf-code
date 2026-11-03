package blobstore

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"testing"

	"github.com/lycaon/lycaon/internal/bytebound"
	"github.com/lycaon/lycaon/internal/testutil"
)

func TestPutContextCancellationAfterPlaintextDoesNotPublish(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	root := t.TempDir()
	source := &cancelAtEOF{Reader: bytes.NewReader(bytes.Repeat([]byte("fixture"), 10000)), cancel: cancel}
	store := Store{Root: root, Dir: "fixture-output"}
	blob, err := store.PutStream(ctx, "body.bin", source, bytebound.Materialization(1<<20), fixtureCodecMemory{})
	if !errors.Is(err, context.Canceled) || blob.ID != "" {
		t.Fatalf("canceled storage published: %+v %v", blob, err)
	}
	files := 0
	err = filepath.WalkDir(root, func(_ string, entry os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if !entry.IsDir() {
			files++
		}
		return nil
	})
	testutil.FailErr(t, "inspect canceled storage", err)
	if files != 0 {
		t.Fatalf("canceled storage retained %d files", files)
	}
}

func TestCanceledCompressionPreservesUnpublishedPlaintext(t *testing.T) {
	tmp, err := os.CreateTemp(t.TempDir(), "fixture-*")
	testutil.FailErr(t, "create stage", err)
	defer tmp.Close()
	want := []byte("fixture plaintext")
	_, err = tmp.Write(want)
	testutil.FailErr(t, "write stage", err)
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if err := compressStagedFile(ctx, tmp); !errors.Is(err, context.Canceled) {
		t.Fatalf("compression error=%v", err)
	}
	got, err := os.ReadFile(tmp.Name())
	testutil.FailErr(t, "read stage", err)
	if !bytes.Equal(got, want) {
		t.Fatal("canceled compression replaced plaintext")
	}
}

type cancelAtEOF struct {
	*bytes.Reader
	cancel context.CancelFunc
}

func (r *cancelAtEOF) Read(p []byte) (int, error) {
	n, err := r.Reader.Read(p)
	if errors.Is(err, io.EOF) {
		r.cancel()
	}
	return n, err
}

type fixtureCodecMemory struct{}

func (fixtureCodecMemory) Reserve(context.Context, int64) (func(), error) { return func() {}, nil }

func TestPutAtCancellationPreservesPublishedBodyAndReleasesStage(t *testing.T) {
	store := Store{Root: t.TempDir()}
	const rel = "tool-output/body.txt"
	_, err := store.PutAt(t.Context(), rel, bytes.NewBufferString("published"), bytebound.Materialization(1<<20))
	testutil.FailErr(t, "publish original body", err)
	before, err := os.ReadFile(filepath.Join(store.Root, rel))
	testutil.FailErr(t, "read original body", err)
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	reader := &cancelAtEOF{Reader: bytes.NewReader(bytes.Repeat([]byte("replacement"), 10000)), cancel: cancel}
	blob, err := store.PutAt(ctx, rel, reader, bytebound.Materialization(1<<20))
	if !errors.Is(err, context.Canceled) || blob.ID != "" {
		t.Fatalf("canceled replacement published: %+v %v", blob, err)
	}
	after, err := os.ReadFile(filepath.Join(store.Root, rel))
	testutil.FailErr(t, "read surviving body", err)
	if !bytes.Equal(before, after) {
		t.Fatal("canceled replacement changed the published body")
	}
	files := 0
	testutil.FailErr(t, "inspect publication and staging", filepath.WalkDir(store.Root, func(_ string, entry os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if !entry.IsDir() {
			files++
		}
		return nil
	}))
	if files != 1 {
		t.Fatalf("canceled replacement retained staging files: %d files", files)
	}
}
