package textutil

import (
	"bytes"
	"fmt"
	"io"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/commandprogram"
)

type writeCounter struct {
	bytes.Buffer
	writes int
}

func (c *writeCounter) Write(p []byte) (int, error) {
	c.writes++
	return c.Buffer.Write(p)
}

func TestSortOutputGroupsRecordsInBoundedWrites(t *testing.T) {
	h := newHarness(t)
	var input strings.Builder
	const lines = 20_000
	for i := range lines {
		fmt.Fprintf(&input, "record %05d\n", i)
	}
	request, release := h.bind("sort")
	defer release()
	var stdout writeCounter
	streams := commandprogram.Streams{Input: io.NopCloser(strings.NewReader(input.String())), Output: &stdout, Error: io.Discard}
	if _, err := h.owners.Builtins(h.ports())["sort"].Invoke(t.Context(), request, streams); err != nil {
		t.Fatal(err)
	}
	if stdout.String() != input.String() {
		t.Fatalf("sorted output differs: %d bytes, want %d", stdout.Len(), input.Len())
	}
	if limit := input.Len()/sortOutputBytes + 2; stdout.writes > limit {
		t.Fatalf("%d records took %d writes, want at most %d", lines, stdout.writes, limit)
	}
}
