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
	"time"

	"github.com/lycaon/lycaon/internal/blobstore"
	"github.com/lycaon/lycaon/internal/testutil"
)

type spillAdmissionFunc func(context.Context, int64) (func(), error)

type retainedPreviewBytes struct{ current, peak int64 }

func (q *retainedPreviewBytes) ReserveProgramState(_ context.Context, n int64) (func(), error) {
	q.current += n
	q.peak = max(q.peak, q.current)
	return func() { q.current -= n }, nil
}

func (f spillAdmissionFunc) ReserveSpill(ctx context.Context, n int64) (func(), error) {
	return f(ctx, n)
}

func TestSettledStreamChargesActualPreviewBytes(t *testing.T) {
	state := &retainedPreviewBytes{}
	out, err := RetainScreenedStream(t.Context(), ScreenedReader(io.NopCloser(bytes.NewReader(nil))), StreamSpillOptions{
		Store: blobstore.Store{Root: t.TempDir()}, ClaimID: "empty-child-output", MaximumBytes: 1024, PreviewBytes: 4096,
		RetainedState: state, WorkingMemory: fixtureStreamMemory{}, Admission: spillAdmissionFunc(func(context.Context, int64) (func(), error) { return func() {}, nil }),
	})
	testutil.FailErr(t, "retain empty output", err)
	if len(out.Preview) != 0 || state.current > 2048 || state.peak < 4096 {
		t.Fatalf("empty settled output retained its maximum preview: current=%d peak=%d", state.current, state.peak)
	}
	testutil.FailErr(t, "release empty output", out.Close())
	if state.current != 0 {
		t.Fatalf("retained state after release: %d", state.current)
	}
}

func TestScreenedStreamSpillKeepsCompleteBinarySeparateFromPreview(t *testing.T) {
	want := bytes.Repeat([]byte{'a', 0, '\r', '\n', 0xff}, 20000)
	released := 0
	var held, peak int64
	var reservations []int64
	out, err := RetainScreenedStream(t.Context(), ScreenedReader(io.NopCloser(bytes.NewReader(want))), StreamSpillOptions{
		Store: blobstore.Store{Root: t.TempDir()}, ClaimID: "fixture-stream-1", MaximumBytes: int64(len(want)), PreviewBytes: 3,
		RetainedState: fixtureStreamMemory{}, WorkingMemory: fixtureStreamMemory{}, Admission: spillAdmissionFunc(func(_ context.Context, n int64) (func(), error) {
			reservations = append(reservations, n)
			held += n
			peak = max(peak, held)
			return func() { released++; held -= n }, nil
		}),
	})
	testutil.FailErr(t, "retain screened byte stream", err)
	defer out.Close()
	reader, err := out.Open(t.Context())
	testutil.FailErr(t, "open full retained stream", err)
	got, err := io.ReadAll(reader)
	testutil.FailErr(t, "read full retained stream", err)
	sum := sha256.Sum256(want)
	if !bytes.Equal(got, want) || !out.Complete || out.Failure != nil || !bytes.Equal(out.Preview, want[:3]) || cap(out.Preview) > 3 || out.EmittedSHA256 != hex.EncodeToString(sum[:]) {
		t.Fatalf("spill confused preview and content: %+v bytes=%d", out, len(got))
	}
	if len(reservations) < 2 || reservations[len(reservations)-1] != out.Blob.Size+RetainedFrameBytes || held != out.Blob.Size+RetainedFrameBytes || peak < 3*int64(len(want))+(1<<20) {
		t.Fatalf("retention reserved %v, want the stored %d bytes after staging", reservations, out.Blob.Size)
	}
	if err := out.Close(); err == nil || held != out.Blob.Size+RetainedFrameBytes {
		t.Fatal("released quota with an active reader")
	}
	testutil.FailErr(t, "close full stream reader", reader.Close())
	testutil.FailErr(t, "discard unreferenced spill", out.Close())
	testutil.FailErr(t, "repeat spill cleanup", out.Close())
	if held != 0 || released != len(reservations) {
		t.Fatalf("quota released %d times", released)
	}
	if _, err := out.Open(t.Context()); err == nil {
		t.Fatal("released spill permitted new reads")
	}
}

func TestFailedScreenedProducerRetainsPrefixAsIncomplete(t *testing.T) {
	cause := errors.New("synthetic producer failure")
	input := io.MultiReader(bytes.NewReader([]byte("screened prefix")), failureReader{cause})
	out, err := RetainScreenedStream(t.Context(), ScreenedReader(io.NopCloser(input)), StreamSpillOptions{Store: blobstore.Store{Root: t.TempDir()}, ClaimID: "fixture-failed-stream", MaximumBytes: 100, PreviewBytes: 4, RetainedState: fixtureStreamMemory{}, WorkingMemory: fixtureStreamMemory{}, Admission: spillAdmissionFunc(func(context.Context, int64) (func(), error) { return func() {}, nil })})
	testutil.FailErr(t, "retain failed producer prefix", err)
	defer out.Close()
	reader, err := out.Open(t.Context())
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
	options := StreamSpillOptions{Store: blobstore.Store{Root: root}, ClaimID: "fixture-stream", MaximumBytes: 100, RetainedState: fixtureStreamMemory{}, WorkingMemory: fixtureStreamMemory{}, Admission: spillAdmissionFunc(func(context.Context, int64) (func(), error) { return nil, refused })}
	if out, err := RetainScreenedStream(t.Context(), ScreenedReader(io.NopCloser(bytes.NewReader([]byte("data")))), options); out != nil || !errors.Is(err, refused) {
		t.Fatalf("quota refusal=%v", err)
	}
	entries, err := os.ReadDir(root)
	testutil.FailErr(t, "inspect refused storage", err)
	if len(entries) != 0 {
		t.Fatal("quota refusal wrote storage")
	}
	released := 0
	options.Admission = spillAdmissionFunc(func(context.Context, int64) (func(), error) { return func() { released++ }, nil })
	out, err := RetainScreenedStream(t.Context(), ScreenedReader(io.NopCloser(bytes.NewReader([]byte("data")))), options)
	testutil.FailErr(t, "retain claimed output", err)
	defer out.Close()
	store := options.Store
	store.Dir = ToolOutputSpillDir
	_, err = store.Retain("fixture-recovery", []string{out.Blob.ID})
	testutil.FailErr(t, "retain recovery reference", err)
	if err := out.Close(); err == nil || released != 1 {
		t.Fatal("freed disk accounting while recovery retained body")
	}
	testutil.FailErr(t, "release recovery reference", store.Release("fixture-recovery", []string{out.Blob.ID}))
	testutil.FailErr(t, "collect released output", out.Close())
	if released != 2 {
		t.Fatal("collected output did not return quota")
	}
}

type failureReader struct{ err error }

func (r failureReader) Read([]byte) (int, error) { return 0, r.err }

type fixtureStreamMemory struct{}

func (fixtureStreamMemory) Reserve(context.Context, int64) (func(), error) { return func() {}, nil }

// An admission blocked on a reader must not prevent that reader from closing.
func TestStreamSpillMemoryWaitAllowsReaderClose(t *testing.T) {
	out, err := RetainScreenedStream(t.Context(), ScreenedReader(io.NopCloser(bytes.NewReader([]byte("fixture")))), StreamSpillOptions{
		Store: blobstore.Store{Root: t.TempDir()}, ClaimID: "fixture-memory-wait", MaximumBytes: 100,
		RetainedState: fixtureStreamMemory{}, WorkingMemory: fixtureStreamMemory{}, Admission: spillAdmissionFunc(func(context.Context, int64) (func(), error) { return func() {}, nil }),
	})
	testutil.FailErr(t, "retain stream", err)
	defer out.Close()
	first, err := out.Open(t.Context())
	testutil.FailErr(t, "open first reader", err)
	entered, proceed := make(chan struct{}), make(chan struct{})
	out.body.memory = streamMemoryFunc(func(ctx context.Context, _ int64) (func(), error) {
		close(entered)
		select {
		case <-proceed:
			return func() {}, nil
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	})
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	opened := make(chan error, 1)
	go func() {
		reader, err := out.Open(ctx)
		if err == nil {
			err = reader.Close()
		}
		opened <- err
	}()
	select {
	case <-entered:
	case <-ctx.Done():
		t.Fatal("reader never requested memory")
	}
	closed := make(chan error, 1)
	go func() { closed <- first.Close() }()
	select {
	case err := <-closed:
		testutil.FailErr(t, "close reader during admission", err)
	case <-ctx.Done():
		t.Fatal("memory wait prevented existing reader close")
	}
	// The spill can be collected while admission is pending. The new reader must
	// then release its acquired lease and refuse to reopen the collected body.
	testutil.FailErr(t, "close spill during admission", out.Close())
	close(proceed)
	select {
	case err := <-opened:
		if err == nil {
			t.Fatal("opened a released spill")
		}
	case <-ctx.Done():
		t.Fatal("pending reader did not settle")
	}
}

type streamMemoryFunc func(context.Context, int64) (func(), error)

func (f streamMemoryFunc) Reserve(ctx context.Context, n int64) (func(), error) { return f(ctx, n) }

func (fixtureStreamMemory) ReserveProgramState(context.Context, int64) (func(), error) {
	return func() {}, nil
}

func TestMidStreamSpillQuotaRetainsOnlyAdmittedPrefixAndReleasesLeases(t *testing.T) {
	cause := errors.New("synthetic incremental quota")
	ceiling := int64((1 << 20) + 3*(64<<10))
	var held int64
	admission := spillAdmissionFunc(func(_ context.Context, n int64) (func(), error) {
		if n > ceiling-held {
			return nil, cause
		}
		held += n
		return func() { held -= n }, nil
	})
	data := bytes.Repeat([]byte("x"), 256<<10)
	out, err := RetainScreenedStream(t.Context(), ScreenedReader(io.NopCloser(bytes.NewReader(data))), StreamSpillOptions{Store: blobstore.Store{Root: t.TempDir()}, ClaimID: "quota-prefix", MaximumBytes: 256 << 20, PreviewBytes: 32, Admission: admission, WorkingMemory: fixtureStreamMemory{}, RetainedState: fixtureStreamMemory{}})
	testutil.FailErr(t, "retain quota prefix", err)
	defer out.Close()
	if out.Complete || !errors.Is(out.Failure, cause) || out.Blob.Size <= 0 || out.Blob.Size >= int64(len(data)) || held != out.Blob.Size+RetainedFrameBytes {
		t.Fatalf("prefix=%+v held=%d", out, held)
	}
	reader, err := out.Open(t.Context())
	testutil.FailErr(t, "open quota prefix", err)
	got, err := io.ReadAll(reader)
	testutil.FailErr(t, "read quota prefix", err)
	testutil.FailErr(t, "close quota prefix", reader.Close())
	if !bytes.Equal(got, data[:len(got)]) {
		t.Fatal("quota prefix changed bytes")
	}
	testutil.FailErr(t, "release quota prefix", out.Close())
	if held != 0 {
		t.Fatalf("quota leaked %d", held)
	}
}
