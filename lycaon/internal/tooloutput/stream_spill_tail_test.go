package tooloutput

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"math/rand/v2"
	"testing"

	"github.com/lycaon/lycaon/internal/blobstore"
	"github.com/lycaon/lycaon/internal/testutil"
)

// chunkedReader returns its input in caller-chosen chunk sizes.
type chunkedReader struct {
	data   []byte
	chunks *rand.Rand
}

func (r *chunkedReader) Read(b []byte) (int, error) {
	if len(r.data) == 0 {
		return 0, io.EOF
	}
	n := min(len(b), len(r.data), 1+r.chunks.IntN(2*StreamTailBytes))
	copy(b, r.data[:n])
	r.data = r.data[n:]
	return n, nil
}

func spillFixture(t *testing.T, input []byte, preview int, seed uint64, state *retainedPreviewBytes) *StreamSpill {
	t.Helper()
	reader := &chunkedReader{data: input, chunks: rand.New(rand.NewPCG(seed, seed))}
	out, err := RetainScreenedStream(t.Context(), ScreenedReader(io.NopCloser(reader)), StreamSpillOptions{
		Store: blobstore.Store{Root: t.TempDir()}, ClaimID: fmt.Sprintf("tail-fixture-%d", seed), MaximumBytes: int64(len(input)) + 1, PreviewBytes: preview,
		RetainedState: state, WorkingMemory: fixtureStreamMemory{}, Admission: spillAdmissionFunc(func(context.Context, int64) (func(), error) { return func() {}, nil }),
	})
	testutil.FailErr(t, "retain fixture stream", err)
	return out
}

func numberedLines(n int) []byte {
	var b bytes.Buffer
	for i := range n {
		fmt.Fprintf(&b, "line %d\n", i)
	}
	return b.Bytes()
}

func referenceLines(input []byte) int64 {
	lines := int64(bytes.Count(input, []byte{'\n'}))
	if len(input) > 0 && input[len(input)-1] != '\n' {
		lines++
	}
	return lines
}

// Any chunking yields the reference tail and line count, and the settled
// reservation covers only bytes actually retained.
func TestStreamSpillTailAndLinesMatchReferenceForAnyChunking(t *testing.T) {
	inputs := map[string][]byte{
		"empty":        nil,
		"one line":     []byte("only line\n"),
		"unterminated": []byte("first\nsecond"),
		"exact ring":   bytes.Repeat([]byte{'x'}, StreamTailBytes),
		"many lines":   numberedLines(5000),
	}
	for name, input := range inputs {
		for seed := range uint64(8) {
			t.Run(fmt.Sprintf("%s/%d", name, seed), func(t *testing.T) {
				state := &retainedPreviewBytes{}
				out := spillFixture(t, input, 64, seed, state)
				want := input[max(0, len(input)-StreamTailBytes):]
				if !bytes.Equal(out.Display.Tail, want) || out.Display.Lines != referenceLines(input) {
					t.Fatalf("tail %d bytes lines %d; want %d bytes lines %d", len(out.Display.Tail), out.Display.Lines, len(want), referenceLines(input))
				}
				claim := int64(len(fmt.Sprintf("tail-fixture-%d", seed)))
				if want := 2*claim + 512 + int64(len(out.Display.Preview)+len(out.Display.Tail)) + 256; state.current != want {
					t.Fatalf("settled state %d; want %d for the retained display bytes", state.current, want)
				}
				testutil.FailErr(t, "release fixture stream", out.Close())
				if state.current != 0 {
					t.Fatalf("retained state after release: %d", state.current)
				}
			})
		}
	}
}

func TestStreamSpillWithoutPreviewKeepsNoTail(t *testing.T) {
	out := spillFixture(t, []byte("a\nb\n"), 0, 1, &retainedPreviewBytes{})
	defer out.Close()
	if out.Display.Tail != nil || out.Display.Lines != 2 {
		t.Fatalf("tail %q lines %d", out.Display.Tail, out.Display.Lines)
	}
}
