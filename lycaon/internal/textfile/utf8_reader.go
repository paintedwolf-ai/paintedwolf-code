package textfile

import (
	"bufio"
	"bytes"
	"io"
	"unicode/utf8"
)

// UTF8Reader projects the declared representation through the existing strict
// decoder without retaining the complete input. UTF-8 keeps its byte chunks;
// UTF-16 uses the same RuneReader validation and emits complete scalar bytes.
// An empty encoding selects UTF-8 or an explicit BOM; it never guesses byte order.
type UTF8Reader struct {
	decoder             *RuneReader
	pending             [utf8.UTFMax]byte
	pendingN, pendingAt int
}

func NewUTF8Reader(input io.Reader, encoding string) (*UTF8Reader, error) {
	if encoding == "" {
		buffered := bufio.NewReaderSize(input, 64<<10)
		prefix, err := buffered.Peek(3)
		if err != nil && err != io.EOF {
			return nil, err
		}
		encoding = UTF8
		switch detected := Classify(prefix).Encoding; detected {
		case UTF8BOM, UTF16LEBOM, UTF16BEBOM:
			encoding = detected
		}
		input = buffered
	}
	decoder, err := NewRuneReader(input, encoding)
	if err != nil {
		return nil, err
	}
	return &UTF8Reader{decoder: decoder}, nil
}

func (r *UTF8Reader) Read(p []byte) (int, error) {
	if len(p) == 0 {
		return 0, nil
	}
	if r.decoder.order == nil {
		return r.readUTF8(p)
	}
	n := 0
	for n < len(p) {
		if r.pendingAt < r.pendingN {
			copied := copy(p[n:], r.pending[r.pendingAt:r.pendingN])
			n += copied
			r.pendingAt += copied
			continue
		}
		scalar, _, err := r.decoder.ReadRune()
		if err != nil {
			return n, err
		}
		r.pendingN = utf8.EncodeRune(r.pending[:], scalar)
		r.pendingAt = 0
	}
	return n, nil
}

func (r *UTF8Reader) readUTF8(p []byte) (int, error) {
	n, err := r.decoder.input.Read(p)
	if bytes.IndexByte(p[:n], 0) >= 0 {
		return n, ErrBinary
	}
	start := 0
	if r.pendingN > 0 {
		for start < n && !utf8.FullRune(r.pending[:r.pendingN]) {
			r.pending[r.pendingN] = p[start]
			r.pendingN++
			start++
		}
		if utf8.FullRune(r.pending[:r.pendingN]) {
			if scalar, size := utf8.DecodeRune(r.pending[:r.pendingN]); scalar == utf8.RuneError && size == 1 {
				return n, ErrUnsupported
			}
			r.pendingN = 0
		} else if err != nil {
			return n, ErrUnsupported
		} else {
			return n, nil
		}
	}
	end := n
	if start < end {
		last := end - 1
		for last > start && !utf8.RuneStart(p[last]) {
			last--
		}
		if !utf8.FullRune(p[last:end]) {
			r.pendingN = copy(r.pending[:], p[last:end])
			end = last
		}
	}
	if !utf8.Valid(p[start:end]) {
		return n, ErrUnsupported
	}
	if err != nil && r.pendingN > 0 {
		return n, ErrUnsupported
	}
	return n, err
}
