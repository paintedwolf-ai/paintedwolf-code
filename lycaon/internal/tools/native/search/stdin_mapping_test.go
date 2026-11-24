package search

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
	"github.com/lycaon/lycaon/internal/testutil"
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

// Text grep prints from mapped stdin keeps its producer's source; prefixes,
// separators and replacements stay unmapped.
func TestGrepCarriesPipedStdinMappings(t *testing.T) {
	const text = "alpha\nbeta\ngamma"
	for _, c := range []struct {
		argv []string
		want [][4]int64
	}{
		{[]string{"grep", "beta"}, [][4]int64{{0, 4, 106, 110}}},
		{[]string{"grep", "-n", "a"}, [][4]int64{{2, 7, 100, 105}, {10, 14, 106, 110}, {17, 22, 111, 116}}},
		{[]string{"grep", "-o", "et"}, [][4]int64{{0, 2, 107, 109}}},
		{[]string{"rg", "-r", "X", "beta"}, [][4]int64{}},
	} {
		h := newHarness(t, grepTree)
		memory, err := toolcontract.NewProgramMemory(toolcontract.ProgramMemoryLimits{Working: 1 << 20, State: 1 << 20})
		testutil.FailErr(t, "program memory", err)
		h.owners.Revisions.Memory = memory
		var stdout mappingBuffer
		streams := commandprogram.Streams{Input: newPipedStdin(text), Output: &stdout, Error: io.Discard}
		result, err := invokeSearch(h.owners, t.Context(), h.tc, mustPaths(t, h), h.dispatch, requestFor(h, c.argv...), streams)
		if err != nil || result.Terminal != toolcontract.ProducerCompleted {
			t.Fatalf("%v: %+v %v", c.argv, result, err)
		}
		if got := sourceRanges(stdout.mappings); fmt.Sprint(got) != fmt.Sprint(c.want) {
			t.Fatalf("%v printed %q with mappings %v, want %v", c.argv, stdout.String(), got, c.want)
		}
		for _, m := range stdout.mappings {
			if out, src := stdout.String()[m.OutputStartByte:m.OutputEndByte], text[m.SourceStartByte-100:m.SourceEndByte-100]; out != src {
				t.Fatalf("%v maps %q to %q", c.argv, out, src)
			}
		}
	}
}
