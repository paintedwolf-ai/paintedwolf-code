package jq

import (
	"context"
	"errors"
	"io"
	"strings"

	"github.com/itchyny/gojq"
)

type eventFrame struct {
	kind                        byte
	index                       int
	key                         string
	release                     func()
	after, populated, mustValue bool
}

// EventStream retains the current scalar and open container path.
type EventStream struct {
	*jsonCursor
	frames                     []eventFrame
	frameRelease, eventRelease func()
}

func NewEventStream(ctx context.Context, input io.Reader, limits DecodeLimits, memory DecodeMemory) *EventStream {
	s := &EventStream{jsonCursor: newJSONCursor(ctx, input, limits, memory)}
	if s.failure != nil {
		return s
	}
	s.frameRelease, s.failure = s.reserve(int64(limits.MaximumDepth) * 128)
	if s.failure != nil {
		failure := s.failure
		s.jsonCursor.Close()
		s.failure = failure
		return s
	}
	s.frames = make([]eventFrame, 0, limits.MaximumDepth)
	return s
}
func (s *EventStream) Close() {
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
	if s.frameRelease != nil {
		s.frameRelease()
		s.frameRelease = nil
	}
	s.jsonCursor.Close()
}
func (s *EventStream) Retain() func() {
	release := s.eventRelease
	s.eventRelease = nil
	if release == nil {
		return func() {}
	}
	return release
}

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
			if errors.Is(err, io.EOF) && len(s.frames) > 0 {
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
				value, release, err := s.scalar(s.limits.MaximumScalarBytes)
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
				return nil, &DecodeLimitError{Limit: "query_depth", Maximum: int64(s.limits.MaximumDepth), Observed: int64(len(s.frames) + 1)}
			}
			_, _ = s.read()
			s.frames = append(s.frames, eventFrame{kind: b})
			continue
		}
		value, release, err := s.scalar(s.limits.MaximumScalarBytes)
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
	if errors.Is(err, io.EOF) {
		return io.ErrUnexpectedEOF
	}
	return err
}
