package tooloutput

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"os"
	"testing"

	"github.com/lycaon/lycaon/internal/blobstore"
	"github.com/lycaon/lycaon/internal/testutil"
)

type spillAdmissionFunc func(context.Context, int64) (func(), error)

func (f spillAdmissionFunc) ReserveSpill(ctx context.Context, n int64) (func(), error) {
	return f(ctx, n)
}

func TestScreenedStreamSpillKeepsCompleteBinarySeparateFromPreview(t *testing.T) {
	want := bytes.Repeat([]byte{'a', 0, '\r', '\n', 0xff}, 20000)
	released := 0
	out, err := RetainScreenedStream(t.Context(), ScreenedReader(io.NopCloser(bytes.NewReader(want))), StreamSpillOptions{
		Store: blobstore.Store{Root: t.TempDir()}, ClaimID: "fixture-stream-1", MaximumBytes: int64(len(want)), PreviewBytes: 3,
		Admission: spillAdmissionFunc(func(_ context.Context, n int64) (func(), error) {
			if n < 3*int64(len(want)) {
				t.Fatal("staging overlap was not reserved")
			}
			return func() { released++ }, nil
		}),
	})
	testutil.FailErr(t, "retain screened byte stream", err)
	defer out.Close()
	reader, err := out.Open()
	testutil.FailErr(t, "open full retained stream", err)
	got, err := io.ReadAll(reader)
	testutil.FailErr(t, "read full retained stream", err)
	sum := sha256.Sum256(want)
	if !bytes.Equal(got, want) || !out.Complete || out.Failure != nil || !bytes.Equal(out.Preview, want[:3]) || out.EmittedSHA256 != hex.EncodeToString(sum[:]) {
		t.Fatalf("spill confused preview and content: %+v bytes=%d", out, len(got))
	}
	if err := out.Close(); err == nil || released != 0 {
		t.Fatal("released quota with an active reader")
	}
	testutil.FailErr(t, "close full stream reader", reader.Close())
	testutil.FailErr(t, "discard unreferenced spill", out.Close())
	testutil.FailErr(t, "repeat spill cleanup", out.Close())
	if released != 1 {
		t.Fatalf("quota released %d times", released)
	}
	if _, err := out.Open(); err == nil {
		t.Fatal("released spill permitted new reads")
	}
}

func TestFailedScreenedProducerRetainsPrefixAsIncomplete(t *testing.T) {
	cause := errors.New("synthetic producer failure")
	input := io.MultiReader(bytes.NewReader([]byte("screened prefix")), failureReader{cause})
	out, err := RetainScreenedStream(t.Context(), ScreenedReader(io.NopCloser(input)), StreamSpillOptions{Store: blobstore.Store{Root: t.TempDir()}, ClaimID: "fixture-failed-stream", MaximumBytes: 100, PreviewBytes: 4, Admission: spillAdmissionFunc(func(context.Context, int64) (func(), error) { return func() {}, nil })})
	testutil.FailErr(t, "retain failed producer prefix", err)
	defer out.Close()
	reader, err := out.Open()
	testutil.FailErr(t, "open failed output", err)
	got, err := io.ReadAll(reader)
	testutil.FailErr(t, "read failed output", err)
	testutil.FailErr(t, "close failed output", reader.Close())
	if out.Complete || !errors.Is(out.Failure, cause) || string(got) != "screened prefix" {
		t.Fatalf("failed prefix became complete: %+v %q", out, got)
	}
}

func TestSpillQuotaRefusesBeforeStorageAndOtherClaimsKeepAccounting(t *testing.T) {
	root := t.TempDir()
	refused := errors.New("synthetic session spill quota")
	options := StreamSpillOptions{Store: blobstore.Store{Root: root}, ClaimID: "fixture-stream", MaximumBytes: 100, Admission: spillAdmissionFunc(func(context.Context, int64) (func(), error) { return nil, refused })}
	if out, err := RetainScreenedStream(t.Context(), ScreenedReader(io.NopCloser(bytes.NewReader([]byte("data")))), options); out != nil || !errors.Is(err, refused) {
		t.Fatalf("quota refusal=%v", err)
	}
	entries, err := os.ReadDir(root)
	testutil.FailErr(t, "inspect refused storage", err)
	if len(entries) != 0 {
		t.Fatal("quota refusal wrote storage")
	}
	released := false
	options.Admission = spillAdmissionFunc(func(context.Context, int64) (func(), error) { return func() { released = true }, nil })
	out, err := RetainScreenedStream(t.Context(), ScreenedReader(io.NopCloser(bytes.NewReader([]byte("data")))), options)
	testutil.FailErr(t, "retain claimed output", err)
	defer out.Close()
	store := options.Store
	store.Dir = ToolOutputSpillDir
	_, err = store.Retain("fixture-recovery", []string{out.Blob.ID})
	testutil.FailErr(t, "retain recovery reference", err)
	if err := out.Close(); err == nil || released {
		t.Fatal("freed disk accounting while recovery retained body")
	}
	testutil.FailErr(t, "release recovery reference", store.Release("fixture-recovery", []string{out.Blob.ID}))
	testutil.FailErr(t, "collect released output", out.Close())
	if !released {
		t.Fatal("collected output did not return quota")
	}
}

type failureReader struct{ err error }

func (r failureReader) Read([]byte) (int, error) { return 0, r.err }
