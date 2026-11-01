package jq

import (
	"context"
	"fmt"
	"io"

	"github.com/itchyny/gojq"
)

// BoundedValueStream admits one decoded document and releases it on the next read.
type BoundedValueStream struct {
	parser   *EventStream
	releases []func()
	bytes    int64
	maximum  int64
}

func NewBoundedValueStream(ctx context.Context, input io.Reader, limits EventLimits, memory EventMemory, maximum int64) *BoundedValueStream {
	return &BoundedValueStream{parser: NewEventStream(ctx, input, limits, memory), maximum: maximum}
}
func (s *BoundedValueStream) Close() { s.release(); s.parser.Close() }
func (s *BoundedValueStream) release() {
	for _, release := range s.releases {
		release()
	}
	s.releases = nil
	s.bytes = 0
}
func (s *BoundedValueStream) InputLine() int { return s.parser.InputLine() }

func (s *BoundedValueStream) Position() (int, int) { return s.parser.Position() }
func (s *BoundedValueStream) Retain() func() {
	releases := s.releases
	s.releases = nil
	s.bytes = 0
	return func() {
		for _, release := range releases {
			release()
		}
	}
}

func (s *BoundedValueStream) InputOffset() int64 { return s.parser.InputOffset() }
func (s *BoundedValueStream) admit(n int64) error {
	if n > s.maximum-s.bytes {
		return &EventLimitError{Limit: "query_value_bytes", Maximum: s.maximum, Observed: s.bytes + n}
	}
	release, err := s.parser.reserve(n + 128)
	if err != nil {
		return err
	}
	s.releases = append(s.releases, release)
	s.bytes += n
	return nil
}
func (s *BoundedValueStream) Decode() (any, error) {
	s.release()
	if s.parser.failure != nil {
		return nil, s.parser.failure
	}
	value, err := s.value(0)
	if err != nil {
		s.parser.failure = err
		s.release()
	}
	return value, err
}
func (s *BoundedValueStream) value(depth int) (any, error) {
	p := s.parser
	b, err := p.space()
	if err != nil {
		if err == io.EOF && depth > 0 {
			err = io.ErrUnexpectedEOF
		}
		return nil, err
	}
	if b != '[' && b != '{' {
		start := p.offset
		value, release, err := p.scalar()
		if err != nil {
			return nil, err
		}
		s.releases = append(s.releases, release)
		size := p.offset - start
		if size > s.maximum-s.bytes {
			return nil, &EventLimitError{Limit: "query_value_bytes", Maximum: s.maximum, Observed: s.bytes + size}
		}
		s.bytes += size
		return value, nil
	}
	if depth >= p.limits.MaximumDepth {
		return nil, &EventLimitError{Limit: "query_depth", Maximum: int64(p.limits.MaximumDepth), Observed: int64(depth + 1)}
	}
	if err := s.admit(64); err != nil {
		return nil, err
	}
	_, _ = p.read()
	if b == '[' {
		return s.array(depth)
	}
	return s.object(depth)
}
func (s *BoundedValueStream) separator(end byte, first bool) (bool, error) {
	b, err := s.parser.space()
	if err != nil {
		if err == io.EOF {
			err = io.ErrUnexpectedEOF
		}
		return false, err
	}
	if b == end {
		_, _ = s.parser.read()
		return true, nil
	}
	if first {
		return false, nil
	}
	if b != ',' {
		return false, s.parser.syntax()
	}
	_, _ = s.parser.read()
	b, err = s.parser.space()
	if err != nil {
		return false, unexpectedJSONEnd(err)
	}
	if b == end {
		return false, s.parser.syntax()
	}
	return false, nil
}
func (s *BoundedValueStream) array(depth int) (any, error) {
	result := []any{}
	for {
		done, err := s.separator(']', len(result) == 0)
		if err != nil {
			return nil, err
		}
		if done {
			return result, nil
		}
		if len(result) == cap(result) {
			capacity := max(4, 2*cap(result))
			if err := s.admit(int64(capacity) * 16); err != nil {
				return nil, err
			}
			next := make([]any, len(result), capacity)
			copy(next, result)
			result = next
		}
		value, err := s.value(depth + 1)
		if err != nil {
			return nil, err
		}
		result = append(result, value)
	}
}
func (s *BoundedValueStream) object(depth int) (any, error) {
	result := gojq.NewObject(0)
	first := true
	for {
		done, err := s.separator('}', first)
		if err != nil {
			return nil, err
		}
		if done {
			return result, nil
		}
		first = false
		b, err := s.parser.space()
		if err != nil {
			return nil, unexpectedJSONEnd(err)
		}
		if b != '"' {
			return nil, s.parser.syntax()
		}
		key, release, err := s.parser.scalar()
		if err != nil {
			return nil, err
		}
		s.releases = append(s.releases, release)
		name, ok := key.(string)
		if !ok {
			return nil, fmt.Errorf("JSON object key is not a string")
		}
		if err := s.admit(256 + int64(len(name))); err != nil {
			return nil, err
		}
		b, err = s.parser.space()
		if err != nil {
			return nil, unexpectedJSONEnd(err)
		}
		if b != ':' {
			return nil, s.parser.syntax()
		}
		_, _ = s.parser.read()
		value, err := s.value(depth + 1)
		if err != nil {
			return nil, err
		}
		result.Set(name, value)
	}
}
