package search

import (
	"bytes"
	"io"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/commandprogram"
	"github.com/lycaon/lycaon/internal/toolcontract"
)

// mappingBuffer is an output endpoint that retains candidate mappings.
type mappingBuffer struct {
	bytes.Buffer
	mappings []toolcontract.StreamSourceMapping
}

func (b *mappingBuffer) RecordSourceMapping(m toolcontract.StreamSourceMapping) error {
	b.mappings = append(b.mappings, m)
	return nil
}

func (h *harness) execMapped(argv ...string) (string, []toolcontract.StreamSourceMapping) {
	h.t.Helper()
	var stdout mappingBuffer
	streams := commandprogram.Streams{Input: io.NopCloser(strings.NewReader("")), Output: &stdout, Error: io.Discard}
	result, err := h.owners.Invoke(h.t.Context(), h.tc, mustPaths(h.t, h), h.dispatch, requestFor(h, argv...), streams)
	if err != nil || result.Terminal != toolcontract.ProducerCompleted {
		h.t.Fatalf("%v: %+v %v", argv, result, err)
	}
	return stdout.String(), stdout.mappings
}

// TestPrintedRecordsMapToTheirCaptures checks that each printed line's text
// (and each -o match) maps to its exact source bytes, prefixes stay unmapped,
// and replaced text carries no mapping.
func TestPrintedRecordsMapToTheirCaptures(t *testing.T) {
	h := newHarness(t, grepTree)
	source := []byte(grepTree["a.txt"])
	for _, c := range []struct {
		argv   []string
		ranges [][2]int // source ranges in output order
	}{
		{[]string{"grep", "-n", "alpha", "a.txt"}, [][2]int{{4, 13}, {30, 39}}},
		{[]string{"grep", "-o", "alpha", "a.txt"}, [][2]int{{8, 13}, {34, 39}}},
		{[]string{"grep", "-B", "1", "six", "a.txt"}, [][2]int{{25, 29}, {30, 39}}},
		{[]string{"rg", "-r", "X", "alpha", "a.txt"}, nil},
	} {
		out, mappings := h.execMapped(c.argv...)
		if len(mappings) != len(c.ranges) {
			t.Fatalf("%v: mappings %+v for %q", c.argv, mappings, out)
		}
		for i, m := range mappings {
			if m.SourceStartByte != int64(c.ranges[i][0]) || m.SourceEndByte != int64(c.ranges[i][1]) || m.Source.SHA256 == "" {
				t.Fatalf("%v mapping %d = %+v", c.argv, i, m)
			}
			if got, want := out[m.OutputStartByte:m.OutputEndByte], string(source[m.SourceStartByte:m.SourceEndByte]); got != want {
				t.Fatalf("%v mapping %d covers %q, source %q", c.argv, i, got, want)
			}
		}
	}
}
