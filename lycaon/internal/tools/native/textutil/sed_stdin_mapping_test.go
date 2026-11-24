//go:build darwin || linux

package textutil

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

// Records sed prints unchanged from mapped stdin keep their producer's
// source; edited records stay unmapped.
func TestSedCarriesPipedStdinMappings(t *testing.T) {
	const text = "one\ntwo\nthree\n"
	for _, c := range []struct {
		script string
		want   [][4]int64
	}{
		{"sed -n 2p", [][4]int64{{0, 4, 104, 108}}},
		{"sed 2d", [][4]int64{{0, 4, 100, 104}, {4, 10, 108, 114}}},
		{"sed s/two/2/", [][4]int64{{0, 4, 100, 104}, {6, 12, 108, 114}}},
	} {
		h := newHarness(t)
		request, release := h.bind(c.script)
		var output mappedSedOutput
		result, err := h.owners.Builtins(h.ports())["sed"].Invoke(t.Context(), request, commandprogram.Streams{Input: newPipedStdin(text), Output: &output, Error: io.Discard})
		release()
		testutil.FailErr(t, c.script, err)
		if result.Status != 0 {
			t.Fatalf("%s: %+v", c.script, result)
		}
		if got := sourceRanges(output.mappings); fmt.Sprint(got) != fmt.Sprint(c.want) {
			t.Fatalf("%s printed %q with mappings %v, want %v", c.script, output.String(), got, c.want)
		}
	}
}
