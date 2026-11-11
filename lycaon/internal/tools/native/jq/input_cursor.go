package jq

import (
	"bufio"
	"bytes"
	"context"
	"fmt"
	"io"
)

// DecodeLimits bound JSON nesting and one encoded scalar.
type DecodeLimits struct {
	MaximumDepth       int
	MaximumScalarBytes int64
}

// DecodeMemory admits parser storage before allocation.
type DecodeMemory interface {
	Reserve(context.Context, int64) (func(), error)
}
type DecodeLimitError struct {
	Limit             string
	Maximum, Observed int64
}

func (e *DecodeLimitError) Error() string {
	return fmt.Sprintf("%s exceeded: %d > %d", e.Limit, e.Observed, e.Maximum)
}

type jsonCursor struct {
	ctx          context.Context
	input        *bufio.Reader
	limits       DecodeLimits
	memory       DecodeMemory
	offset       int64
	line, column int
	release      func()
	failure      error
}

func newJSONCursor(ctx context.Context, input io.Reader, limits DecodeLimits, memory DecodeMemory) *jsonCursor {
	s := &jsonCursor{ctx: ctx, limits: limits, memory: memory}
	if ctx == nil || input == nil || limits.MaximumDepth < 1 || limits.MaximumScalarBytes < 1 {
		s.failure = fmt.Errorf("JSON decoding requires a context, input and positive bounds")
		return s
	}
	s.release, s.failure = s.reserve(4096 + 512)
	if s.failure == nil {
		s.input = bufio.NewReaderSize(input, 4096)
	}
	return s
}
func (s *jsonCursor) Close() {
	s.input = nil
	s.failure = io.ErrClosedPipe
	if s.release != nil {
		s.release()
		s.release = nil
	}
}
func (s *jsonCursor) reserve(n int64) (func(), error) {
	if err := s.ctx.Err(); err != nil {
		return nil, err
	}
	if s.memory != nil {
		release, err := s.memory.Reserve(s.ctx, n)
		if err != nil {
			if release != nil {
				release()
			}
			return nil, err
		}
		if release == nil {
			return nil, fmt.Errorf("JSON decoding requires retained admission")
		}
		return release, nil
	}
	return func() {}, nil
}
func (s *jsonCursor) InputLine() int {
	if s.input == nil {
		return s.line
	}
	buffered, _ := s.input.Peek(s.input.Buffered())
	if bytes.IndexByte(buffered, '\n') >= 0 {
		return s.line + 1
	}
	return s.line
}

func (s *jsonCursor) Position() (int, int) { return s.line + 1, s.column }

func (s *jsonCursor) InputOffset() int64 { return s.offset }
func (s *jsonCursor) peek() (byte, error) {
	if err := s.ctx.Err(); err != nil {
		return 0, err
	}
	p, err := s.input.Peek(1)
	if err != nil {
		return 0, err
	}
	return p[0], nil
}
func (s *jsonCursor) read() (byte, error) {
	b, err := s.input.ReadByte()
	if err == nil {
		s.offset++
		if b == '\n' {
			s.line++
			s.column = 0
		} else {
			s.column++
		}
	}
	return b, err
}
func (s *jsonCursor) space() (byte, error) {
	for {
		b, err := s.peek()
		if err != nil {
			return 0, err
		}
		if b != ' ' && b != '\n' && b != '\r' && b != '\t' {
			return b, nil
		}
		_, _ = s.read()
	}
}
func (s *jsonCursor) syntax() error { return fmt.Errorf("invalid JSON at byte %d", s.offset+1) }
