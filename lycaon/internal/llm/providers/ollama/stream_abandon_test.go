package ollama

import (
	"context"
	"io"
	"testing"
	"time"

	"github.com/lycaon/lycaon/internal/llm/modelcall"
)

// A ctx check at the top of the read loop cannot release a goroutine already
// parked handing a chunk to the consumer; the send must observe cancellation.
func TestOllamaDecoderReleasesAbandonedStream(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()

	body := newLineReader(
		`{"message":{"content":"one"},"done":false}`,
		`{"message":{"content":"two"},"done":false}`,
		`{"message":{"content":"three"},"done":false}`,
	)

	ch := decodeOllamaStream(ctx, body, "desktop", "model", 8192, nil)
	if _, ok := <-ch; !ok {
		t.Fatal("expected at least one decoded chunk")
	}
	cancel()
	time.Sleep(abandonSettle)

	assertStreamClosed(t, ch, "ollama decoder")
}

const abandonSettle = 50 * time.Millisecond

func assertStreamClosed(t *testing.T, ch <-chan modelcall.StreamChunk, what string) {
	t.Helper()
	select {
	case chunk, ok := <-ch:
		if ok {
			t.Fatalf("%s: producer was stranded on a send after the consumer abandoned the stream (delivered %+v)", what, chunk)
		}
	case <-time.After(2 * time.Second):
		t.Fatalf("%s: stream neither closed nor delivered after abandonment", what)
	}
}

// newLineReader serves newline-delimited payloads one line per Read, the way a
// live HTTP body arrives. The decoder parks handing over the second chunk and
// never reaches the end, so the exit under test is attributable to the send.
func newLineReader(lines ...string) *lineReader {
	return &lineReader{lines: lines}
}

type lineReader struct {
	lines []string
	buf   []byte
}

func (r *lineReader) Read(p []byte) (int, error) {
	if len(r.buf) == 0 {
		if len(r.lines) == 0 {
			return 0, io.EOF
		}
		r.buf = []byte(r.lines[0] + "\n")
		r.lines = r.lines[1:]
	}
	n := copy(p, r.buf)
	r.buf = r.buf[n:]
	return n, nil
}
