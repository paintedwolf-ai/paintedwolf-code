package literal

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/commandprogram"
	"github.com/lycaon/lycaon/internal/toolcontract"
)

// pipedStdin is stdin from a producer whose bytes [0,len) map to source
// bytes [100,100+len) of one capture.
type pipedStdin struct {
	io.Reader
	length int64
}

func (pipedStdin) Close() error { return nil }

func (p pipedStdin) SettledSourceMappings(context.Context) ([]toolcontract.StreamSourceMapping, error) {
	digest := sha256.Sum256(bytes.Repeat([]byte{'x'}, 200))
	pin := toolcontract.SourcePin{SourceID: "piped-source", Revision: "piped-revision", Representation: toolcontract.LogicalUTF8LF, SHA256: hex.EncodeToString(digest[:]), StartByte: 0, EndByte: 200}
	return []toolcontract.StreamSourceMapping{{Source: pin, SourceStartByte: 100, SourceEndByte: 100 + p.length, OutputStartByte: 0, OutputEndByte: p.length}}, nil
}

func newPipedStdin(text string) pipedStdin {
	return pipedStdin{Reader: strings.NewReader(text), length: int64(len(text))}
}

// sourceRanges lists each mapping as output and source byte ranges.
func sourceRanges(mappings []toolcontract.StreamSourceMapping) [][4]int64 {
	out := make([][4]int64, len(mappings))
	for i, m := range mappings {
		out[i] = [4]int64{m.OutputStartByte, m.OutputEndByte, m.SourceStartByte, m.SourceEndByte}
	}
	return out
}

// A line window over mapped stdin keeps the producer's source; a byte
// window may split a character and stays unmapped.
func TestHeadAndTailCarryPipedStdinMappings(t *testing.T) {
	const text = "alpha\nbeta\ngamma\n"
	for _, c := range []struct {
		argv []string
		want [][4]int64
	}{
		{[]string{"head", "-n", "2"}, [][4]int64{{0, 11, 100, 111}}},
		{[]string{"tail", "-n", "1"}, [][4]int64{{0, 6, 111, 117}}},
		{[]string{"head", "-c", "3"}, [][4]int64{}},
	} {
		h := newHarness(t, nil)
		memory, err := toolcontract.NewProgramMemory(toolcontract.ProgramMemoryLimits{Working: 1 << 20, State: 1 << 20})
		if err != nil {
			t.Fatal(err)
		}
		h.owners.Revisions.Memory = memory
		var stdout mappingBuffer
		streams := commandprogram.Streams{Input: newPipedStdin(text), Output: &stdout, Error: &bytes.Buffer{}}
		if _, err := h.owners.Invoke(t.Context(), h.tc, mustPaths(t, h), requestFor(h, c.argv...), streams); err != nil {
			t.Fatalf("%v: %v", c.argv, err)
		}
		if got := sourceRanges(stdout.mappings); fmt.Sprint(got) != fmt.Sprint(c.want) {
			t.Fatalf("%v printed %q with mappings %v, want %v", c.argv, stdout.String(), got, c.want)
		}
	}
}
