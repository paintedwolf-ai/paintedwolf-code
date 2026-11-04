package search

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/commandprogram"
)

// countingOutput is a sink without source mappings, as a pipe producer is.
type countingOutput struct {
	bytes.Buffer
	writes int
	failed error
}

func (c *countingOutput) Write(p []byte) (int, error) {
	if c.failed != nil {
		return 0, c.failed
	}
	if len(p) > 0 {
		c.writes++
	}
	return c.Buffer.Write(p)
}

func (h *harness) grepInput(input io.ReadCloser, stdout, stderr io.Writer, argv ...string) error {
	h.t.Helper()
	streams := commandprogram.Streams{Input: input, Output: stdout, Error: stderr}
	_, err := h.owners.Invoke(h.t.Context(), h.tc, mustPaths(h.t, h), h.dispatch, requestFor(h, argv...), streams)
	return err
}

func TestPipeOutputGroupsRecordsInBoundedWrites(t *testing.T) {
	h := newHarness(t, map[string]string{})
	var want strings.Builder
	const lines = 20_000
	for i := range lines {
		fmt.Fprintf(&want, "match line %d\n", i)
	}
	var stdout countingOutput
	reader := strings.NewReader(want.String())
	if err := h.grepInput(readyInput{reader}, &stdout, io.Discard, "grep", "match"); err != nil {
		t.Fatal(err)
	}
	if stdout.String() != want.String() {
		t.Fatalf("output differs: %d bytes, want %d", stdout.Len(), want.Len())
	}
	// Delivery splits held output into the sink's 32 KiB pipe chunks.
	if limit := want.Len()/(32<<10) + 2; stdout.writes > limit {
		t.Fatalf("%d records took %d writes, want at most %d", lines, stdout.writes, limit)
	}
}

// readyInput always has more bytes, so only the buffer bound and the end of
// input deliver held output.
type readyInput struct{ io.Reader }

func (readyInput) InputReady() bool { return true }
func (readyInput) Close() error     { return nil }

// A consumer of a live stream must see each result before the next input can
// arrive: this source returns its second record only after the first is out.
func TestPipeOutputReachesConsumerBeforeAWaitingRead(t *testing.T) {
	h := newHarness(t, map[string]string{})
	var stdout countingOutput
	input := &awaitedInput{stdout: &stdout, parts: []string{"match one\n", "match two\n"}}
	if err := h.grepInput(input, &stdout, io.Discard, "grep", "match"); err != nil {
		t.Fatal(err)
	}
	if input.seen != "match one\n" {
		t.Fatalf("before the second read the consumer had %q", input.seen)
	}
	if stdout.String() != "match one\nmatch two\n" {
		t.Fatalf("output %q", stdout.String())
	}
}

type awaitedInput struct {
	stdout *countingOutput
	parts  []string
	reads  int
	seen   string
}

func (a *awaitedInput) Close() error { return nil }

func (a *awaitedInput) Read(p []byte) (int, error) {
	if a.reads == 1 {
		a.seen = a.stdout.String()
	}
	a.reads++
	if len(a.parts) == 0 {
		return 0, io.EOF
	}
	n := copy(p, a.parts[0])
	a.parts = a.parts[1:]
	return n, nil
}

// One writer shared by both streams keeps the order the utility produced.
func TestPipeOutputPrecedesLaterDiagnostics(t *testing.T) {
	h := newHarness(t, grepTree)
	var shared bytes.Buffer
	if err := h.grepInput(io.NopCloser(strings.NewReader("")), &shared, &shared, "grep", "-h", "alpha", "a.txt", "missing.txt"); err != nil {
		t.Fatal(err)
	}
	found, diagnostic := strings.Index(shared.String(), "alpha"), strings.Index(shared.String(), "missing.txt")
	if found < 0 || diagnostic < 0 || found > diagnostic {
		t.Fatalf("selected record must precede the diagnostic: %q", shared.String())
	}
}

func TestPipeOutputFailureIsReportedAtFinalDelivery(t *testing.T) {
	h := newHarness(t, grepTree)
	closed := errors.New("consumer closed")
	stdout := countingOutput{failed: closed}
	err := h.grepInput(io.NopCloser(strings.NewReader("")), &stdout, io.Discard, "grep", "alpha", "a.txt")
	if !errors.Is(err, closed) {
		t.Fatalf("got %v, want the sink failure", err)
	}
}
