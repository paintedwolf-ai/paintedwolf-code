//go:build stress

package jq

import (
	"io"
	"strings"
	"testing"
)

func TestStressJSONEventsBeyondWholeDocumentCapacity(t *testing.T) {
	const blocks = 1024
	const scalars = 16
	block := strings.Repeat(`"`+strings.Repeat("x", 4096)+`",`, scalars)
	input := io.MultiReader(strings.NewReader("["), &repeatedJSONBlock{block: block, remaining: blocks}, strings.NewReader("0]"))
	memory := &eventMemoryFixture{maximum: 128 << 10}
	stream := NewEventStream(t.Context(), input, DecodeLimits{MaximumDepth: 128, MaximumScalarBytes: 8192}, memory)
	defer stream.Close()
	events := 0
	for {
		_, err := stream.Decode()
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatalf("event %d: %v", events, err)
		}
		events++
	}
	if events != blocks*scalars+2 || stream.InputOffset() <= 64<<20 || memory.peak > 64<<10 {
		t.Fatalf("events %d bytes %d memory %+v", events, stream.InputOffset(), memory)
	}
	stream.Close()
	if memory.held != 0 {
		t.Fatalf("retained memory %+v", memory)
	}
}

type repeatedJSONBlock struct {
	block     string
	at        int
	remaining int
}

func (r *repeatedJSONBlock) Read(p []byte) (int, error) {
	if r.remaining == 0 {
		return 0, io.EOF
	}
	n := copy(p, r.block[r.at:])
	r.at += n
	if r.at == len(r.block) {
		r.at = 0
		r.remaining--
	}
	return n, nil
}
