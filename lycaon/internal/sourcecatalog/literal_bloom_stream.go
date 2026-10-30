package sourcecatalog

import (
	"io"

	"github.com/lycaon/lycaon/internal/litprefilter"
	"github.com/lycaon/lycaon/internal/textfile"
)

// literalBloomStream retains only a filter and a split rune between writes.
type literalBloomStream struct {
	bloom     literalBloom
	validator textfile.UTF8Validator
	window    [3]byte
	filled    int
	carry     []byte
	folded    []byte
	invalid   bool
}

func newLiteralBloomStream(size int64) *literalBloomStream {
	return &literalBloomStream{bloom: newLiteralBloom(size)}
}

func (s *literalBloomStream) Write(raw []byte) (int, error) {
	n := len(raw)
	for len(raw) > 0 && !s.invalid {
		take := min(len(raw), literalReadQuantum)
		s.writeChunk(raw[:take])
		raw = raw[take:]
	}
	return n, nil
}

func (s *literalBloomStream) writeChunk(raw []byte) {
	if s.invalid {
		return
	}
	if !s.validator.Add(raw, false) {
		s.invalid = true
		s.bloom = nil
		return
	}
	if len(s.carry) > 0 {
		raw = append(s.carry, raw...)
	}
	complete := trimIncompleteUTF8Tail(raw)
	s.folded = litprefilter.AppendCanonicalFold(s.folded[:0], complete)
	s.carry = append(s.carry[:0], raw[len(complete):]...)
	for _, b := range s.folded {
		if s.filled < len(s.window) {
			s.window[s.filled] = b
			s.filled++
			if s.filled < len(s.window) {
				continue
			}
		} else {
			s.window[0], s.window[1], s.window[2] = s.window[1], s.window[2], b
		}
		s.bloom.add(s.window)
	}
	return
}

func (s *literalBloomStream) finish() (literalBloom, bool) {
	if s.invalid || !s.validator.Add(nil, true) || len(s.carry) > 0 {
		return nil, false
	}
	return s.bloom, true
}

var _ io.Writer = (*literalBloomStream)(nil)
