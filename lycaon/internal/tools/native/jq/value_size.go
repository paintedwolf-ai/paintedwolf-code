package jq

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"math/big"
	"unicode/utf8"

	"github.com/itchyny/gojq"
)

// JSONSize measures compact query encoding without retaining its output.
// Sizes above maximum return maximum+1.
func JSONSize(ctx context.Context, value any, maximum int64) (int64, error) {
	if ctx == nil || maximum < 0 || maximum == math.MaxInt64 {
		return 0, fmt.Errorf("JSON sizing requires context and an addressable bound")
	}
	s := valueSizer{ctx: ctx, maximum: maximum}
	err := s.value(value)
	return s.bytes, err
}

type valueSizer struct {
	ctx            context.Context
	bytes, maximum int64
}

func (s *valueSizer) add(n int64) bool {
	if n > s.maximum-s.bytes {
		s.bytes = s.maximum + 1
		return false
	}
	s.bytes += n
	return true
}

func (s *valueSizer) value(value any) error {
	if err := s.ctx.Err(); err != nil {
		return err
	}
	if s.bytes > s.maximum {
		return nil
	}
	switch value := value.(type) {
	case nil:
		s.add(4)
	case bool:
		if value {
			s.add(4)
		} else {
			s.add(5)
		}
	case string:
		return s.text(value)
	case json.Number:
		s.add(int64(len(value)))
	case int, float64, *big.Int:
		raw, err := EncodeJSON(value, false)
		if err != nil {
			return err
		}
		s.add(int64(len(raw)))
	case []any:
		if !s.add(2 + int64(max(0, len(value)-1))) {
			return nil
		}
		for _, child := range value {
			if err := s.value(child); err != nil {
				return err
			}
			if s.bytes > s.maximum {
				break
			}
		}
	case *gojq.OrderedObject:
		if !s.add(2) {
			return nil
		}
		var failure error
		first := true
		value.Range(func(key string, child any) bool {
			if !first && !s.add(1) {
				return false
			}
			first = false
			failure = s.text(key)
			if failure != nil || !s.add(1) {
				return false
			}
			failure = s.value(child)
			return failure == nil && s.bytes <= s.maximum
		})
		return failure
	default:
		return fmt.Errorf("unsupported JSON value %T", value)
	}
	return nil
}

func (s *valueSizer) text(value string) error {
	if !s.add(2) {
		return nil
	}
	checked := -4096
	for at := 0; at < len(value); {
		if at-checked >= 4096 {
			checked = at
			if err := s.ctx.Err(); err != nil {
				return err
			}
		}
		size, width := int64(1), 1
		b := value[at]
		switch {
		case b == '"' || b == '\\' || b == '\b' || b == '\f' || b == '\n' || b == '\r' || b == '\t':
			size = 2
		case b < 0x20 || b == 0x7f:
			size = 6
		case b >= utf8.RuneSelf:
			_, width = utf8.DecodeRuneInString(value[at:])
			size = int64(width)
			if width == 1 {
				size = 6
			}
		}
		if !s.add(size) {
			break
		}
		at += width
	}
	return nil
}
