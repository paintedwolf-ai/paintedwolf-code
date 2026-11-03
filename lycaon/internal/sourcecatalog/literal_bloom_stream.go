package sourcecatalog

import (
	"io"
	"unicode/utf8"

	"github.com/lycaon/lycaon/internal/litprefilter"
)

// literalBloomStream folds complete runes directly into the trigram window.
type literalBloomStream struct {
	bloom    literalBloom
	window   [3]byte
	filled   int
	pending  [utf8.UTFMax]byte
	pendingN int
	invalid  bool
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
	if s.pendingN > 0 {
		for !utf8.FullRune(s.pending[:s.pendingN]) && len(raw) > 0 {
			s.pending[s.pendingN] = raw[0]
			s.pendingN++
			raw = raw[1:]
		}
		if !utf8.FullRune(s.pending[:s.pendingN]) {
			return
		}
		s.addEncodedRune(s.pending[:s.pendingN])
		s.pendingN = 0
	}
	for len(raw) > 0 && !s.invalid {
		if raw[0] < utf8.RuneSelf {
			c := raw[0]
			if c == 0 {
				s.invalidate()
				return
			}
			if 'a' <= c && c <= 'z' {
				c -= 'a' - 'A'
			}
			s.addByte(c)
			raw = raw[1:]
			continue
		}
		if !utf8.FullRune(raw) {
			s.pendingN = copy(s.pending[:], raw)
			return
		}
		raw = raw[s.addEncodedRune(raw):]
	}
}

func (s *literalBloomStream) addEncodedRune(raw []byte) int {
	r, size := utf8.DecodeRune(raw)
	if r == utf8.RuneError && size == 1 {
		s.invalidate()
		return size
	}
	var encoded [utf8.UTFMax]byte
	for _, b := range utf8.AppendRune(encoded[:0], litprefilter.CanonicalFold(r)) {
		s.addByte(b)
	}
	return size
}

func (s *literalBloomStream) addByte(b byte) {
	if s.filled < len(s.window) {
		s.window[s.filled] = b
		s.filled++
		if s.filled < len(s.window) {
			return
		}
	} else {
		s.window[0], s.window[1], s.window[2] = s.window[1], s.window[2], b
	}
	s.bloom.add(s.window)
}

func (s *literalBloomStream) invalidate() {
	s.invalid, s.bloom = true, nil
}

func (s *literalBloomStream) finish() (literalBloom, bool) {
	if s.invalid || s.pendingN > 0 {
		return nil, false
	}
	return s.bloom, true
}

var _ io.Writer = (*literalBloomStream)(nil)
