package jq

import (
	"bytes"
	"io"
)

func (s *EventStream) scalar() (value any, release func(), failure error) {
	var raw []byte
	var held func()
	defer func() {
		if failure != nil && held != nil {
			held()
		}
	}()
	appendByte := func(b byte) error {
		if int64(len(raw)) >= s.limits.MaximumScalarBytes {
			return &EventLimitError{Limit: "query_value_bytes", Maximum: s.limits.MaximumScalarBytes, Observed: int64(len(raw) + 1)}
		}
		if len(raw) == cap(raw) {
			capacity := min(s.limits.MaximumScalarBytes, max(256, 2*int64(cap(raw))))
			nextRelease, err := s.reserve(capacity + 128)
			if err != nil {
				return err
			}
			next := make([]byte, len(raw), int(capacity))
			copy(next, raw)
			if held != nil {
				held()
			}
			raw, held = next, nextRelease
		}
		raw = append(raw, b)
		return nil
	}
	first, err := s.peek()
	if err != nil {
		return nil, nil, err
	}
	quoted := first == '"'
	escaped := false
	for {
		b, err := s.peek()
		if err != nil {
			if err == io.EOF && !quoted && len(raw) > 0 {
				break
			}
			if err == io.EOF {
				err = io.ErrUnexpectedEOF
			}
			return nil, nil, err
		}
		if !quoted && (b == ' ' || b == '\n' || b == '\r' || b == '\t' || b == ',' || b == ']' || b == '}' || b == '[' || b == '{' || b == ':') {
			break
		}
		if err := appendByte(b); err != nil {
			return nil, nil, err
		}
		_, _ = s.read()
		if quoted && len(raw) > 1 {
			if b == '"' && !escaped {
				break
			}
			if b == '\\' && !escaped {
				escaped = true
			} else {
				escaped = false
			}
		}
	}
	if len(raw) == 0 {
		return nil, nil, s.syntax()
	}
	scratch, err := s.reserve(4096)
	if err != nil {
		return nil, nil, err
	}
	defer scratch()
	retained, err := s.reserve(2*int64(len(raw)) + 128)
	if err != nil {
		return nil, nil, err
	}
	decoder := NewValueStream(bytes.NewReader(raw))
	value, err = decoder.Decode()
	if err == nil {
		_, err = decoder.Decode()
		if err == io.EOF {
			err = nil
		} else {
			err = s.syntax()
		}
	}
	if err != nil {
		retained()
		return nil, nil, err
	}
	if held != nil {
		held()
		held = nil
	}
	return value, retained, nil
}
