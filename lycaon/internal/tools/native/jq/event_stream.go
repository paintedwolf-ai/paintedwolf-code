package jq

import (
	"bufio"
	"bytes"
	"context"
	"fmt"
	"io"
	"strings"

	"github.com/itchyny/gojq"
)

// EventLimits bound parser nesting and one encoded scalar.
type EventLimits struct {
	MaximumDepth       int
	MaximumScalarBytes int64
}
type EventMemory interface {
	Reserve(context.Context, int64) (func(), error)
}
type EventLimitError struct {
	Limit             string
	Maximum, Observed int64
}

func (e *EventLimitError) Error() string {
	return fmt.Sprintf("%s exceeded: %d > %d", e.Limit, e.Observed, e.Maximum)
}

type eventFrame struct {
	kind                        byte
	index                       int
	key                         string
	release                     func()
	after, populated, mustValue bool
}

// EventStream retains the current scalar and open container path.
type EventStream struct {
	ctx                   context.Context
	input                 *bufio.Reader
	limits                EventLimits
	memory                EventMemory
	frames                []eventFrame
	offset                int64
	line, column          int
	release, eventRelease func()
	failure               error
}

func NewEventStream(ctx context.Context, input io.Reader, limits EventLimits, memory EventMemory) *EventStream {
	s := &EventStream{ctx: ctx, limits: limits, memory: memory}
	if ctx == nil || input == nil || limits.MaximumDepth < 1 || limits.MaximumScalarBytes < 1 {
		s.failure = fmt.Errorf("JSON events require a context, input and positive bounds")
		return s
	}
	s.release, s.failure = s.reserve(4096 + int64(limits.MaximumDepth)*128)
	if s.failure == nil {
		s.input = bufio.NewReaderSize(input, 4096)
		s.frames = make([]eventFrame, 0, limits.MaximumDepth)
	}
	return s
}
func (s *EventStream) reserve(n int64) (func(), error) {
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
func (s *EventStream) Close() {
	s.failure = io.ErrClosedPipe
	if s.eventRelease != nil {
		s.eventRelease()
		s.eventRelease = nil
	}
	for i := range s.frames {
		if s.frames[i].release != nil {
			s.frames[i].release()
		}
	}
	s.frames = nil
	if s.release != nil {
		s.release()
		s.release = nil
	}
}
func (s *EventStream) InputLine() int {
	if s.input == nil {
		return s.line
	}
	buffered, _ := s.input.Peek(s.input.Buffered())
	if bytes.IndexByte(buffered, '\n') >= 0 {
		return s.line + 1
	}
	return s.line
}

func (s *EventStream) Position() (int, int) { return s.line + 1, s.column }

func (s *EventStream) Retain() func() {
	release := s.eventRelease
	s.eventRelease = nil
	if release == nil {
		return func() {}
	}
	return release
}

func (s *EventStream) InputOffset() int64 { return s.offset }
func (s *EventStream) peek() (byte, error) {
	if err := s.ctx.Err(); err != nil {
		return 0, err
	}
	p, err := s.input.Peek(1)
	if err != nil {
		return 0, err
	}
	return p[0], nil
}
func (s *EventStream) read() (byte, error) {
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
func (s *EventStream) space() (byte, error) {
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
func (s *EventStream) syntax() error { return fmt.Errorf("invalid JSON at byte %d", s.offset+1) }
func (s *EventStream) complete() {
	if len(s.frames) > 0 {
		f := &s.frames[len(s.frames)-1]
		f.after, f.populated = true, true
	}
}
func (s *EventStream) path(length int) ([]any, error) {
	size := int64(128 + 32*length)
	for _, f := range s.frames[:length] {
		size += int64(len(f.key))
	}
	release, err := s.reserve(size)
	if err != nil {
		return nil, err
	}
	previous := s.eventRelease
	s.eventRelease = func() {
		release()
		if previous != nil {
			previous()
		}
	}
	result := make([]any, length)
	for i, f := range s.frames[:length] {
		if f.kind == '[' {
			result[i] = f.index
		} else {
			result[i] = strings.Clone(f.key)
		}
	}
	return result, nil
}

// Decode returns one leaf or container-end event before reading the next.
func (s *EventStream) Decode() (value any, failure error) {
	defer func() {
		if failure != nil {
			s.failure = failure
		}
	}()
	if s.failure != nil {
		return nil, s.failure
	}
	if s.eventRelease != nil {
		s.eventRelease()
		s.eventRelease = nil
	}
	for {
		b, err := s.space()
		if err != nil {
			if err == io.EOF && len(s.frames) > 0 {
				return nil, unexpectedJSONEnd(err)
			}
			return nil, err
		}
		if len(s.frames) > 0 {
			f := &s.frames[len(s.frames)-1]
			close := byte(']')
			if f.kind == '{' {
				close = '}'
			}
			if b == close {
				if f.mustValue {
					return nil, s.syntax()
				}
				_, _ = s.read()
				return s.finish()
			}
			if f.after {
				if b != ',' {
					return nil, s.syntax()
				}
				_, _ = s.read()
				f.after, f.mustValue = false, true
				if f.kind == '[' {
					f.index++
				}
				continue
			}
			if f.kind == '{' {
				if b != '"' {
					return nil, s.syntax()
				}
				value, release, err := s.scalar()
				if err != nil {
					return nil, err
				}
				key, ok := value.(string)
				if !ok {
					release()
					return nil, s.syntax()
				}
				if f.release != nil {
					f.release()
				}
				f.key, f.release = key, release
				b, err = s.space()
				if err != nil {
					return nil, unexpectedJSONEnd(err)
				}
				if b != ':' {
					return nil, s.syntax()
				}
				_, _ = s.read()
				b, err = s.space()
				if err != nil {
					return nil, unexpectedJSONEnd(err)
				}
			}
			f.mustValue = false
		}
		if b == '[' || b == '{' {
			if len(s.frames) >= s.limits.MaximumDepth {
				return nil, &EventLimitError{Limit: "query_depth", Maximum: int64(s.limits.MaximumDepth), Observed: int64(len(s.frames) + 1)}
			}
			_, _ = s.read()
			s.frames = append(s.frames, eventFrame{kind: b})
			continue
		}
		value, release, err := s.scalar()
		if err != nil {
			return nil, err
		}
		s.eventRelease = release
		path, err := s.path(len(s.frames))
		if err != nil {
			return nil, err
		}
		s.complete()
		return []any{path, value}, nil
	}
}
func (s *EventStream) finish() (any, error) {
	index := len(s.frames) - 1
	f := s.frames[index]
	length := len(s.frames)
	if !f.populated {
		length--
	}
	path, err := s.path(length)
	if err != nil {
		return nil, err
	}
	if f.release != nil {
		f.release()
	}
	s.frames[index] = eventFrame{}
	s.frames = s.frames[:index]
	s.complete()
	if f.populated {
		return []any{path}, nil
	}
	var empty any = []any{}
	if f.kind == '{' {
		empty = gojq.NewObject(0)
	}
	return []any{path, empty}, nil
}

func unexpectedJSONEnd(err error) error {
	if err == io.EOF {
		return io.ErrUnexpectedEOF
	}
	return err
}
