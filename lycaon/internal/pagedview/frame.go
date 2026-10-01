package pagedview

import (
	"encoding/json"
	"errors"
)

const MaxRows = 200
const MaxFrameBytes = 256 * 1024

var ErrFrameSize = errors.New("presentation row exceeds frame limit")

type Revision struct{ Intent, Projection string }
type Span struct{ Start, End int64 }
type Extent struct {
	Rows     int64
	Complete bool
}
type Frame[R, A any] struct {
	Revision Revision
	Span     Span
	Extent   Extent
	Anchor   A
	Rows     []R
}

// Pack bounds encoded row bytes as well as count. The adapter reserves envelope bytes.
func Pack[R any](rows []R, envelopeBytes int) ([]R, error) {
	if envelopeBytes < 0 || envelopeBytes >= MaxFrameBytes {
		return nil, ErrFrameSize
	}
	used := envelopeBytes
	for i, row := range rows {
		encoded, err := json.Marshal(row)
		if err != nil {
			return nil, err
		}
		size := len(encoded) + 1
		if i >= MaxRows || size > MaxFrameBytes-used {
			if i == 0 {
				return nil, ErrFrameSize
			}
			return rows[:i], nil
		}
		used += size
	}
	return rows, nil
}

// AnchorOffset keeps relative navigation inside the current projection without overflow.
func AnchorOffset(anchor, offset, total int64, before int, follow bool) (int64, error) {
	if anchor < 0 || anchor >= total || offset < 0 || before < 0 {
		return 0, ErrRange
	}
	remaining := total - anchor
	if follow {
		remaining--
	}
	if offset > remaining {
		if !follow {
			return 0, ErrRange
		}
		offset = remaining
	}
	return max(0, anchor+offset-int64(before)), nil
}
