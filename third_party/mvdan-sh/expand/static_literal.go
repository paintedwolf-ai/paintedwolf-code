package expand

import (
	"fmt"
	"math"
	"strings"
	"unsafe"

	"mvdan.cc/sh/v3/syntax"
)

type staticPiece struct {
	text  string
	quote quoteLevel
}

type staticFrame struct {
	parts []syntax.WordPart
	index int
	quote quoteLevel
}

// StaticLiteral removes quotes from a proven static word with no expansion
// callbacks, environment access, account lookup, field splitting, or globbing.
// Dynamic syntax and real brace/tilde expansion return static=false. All working
// storage is admitted against the same pool as parsing and host lowering.
func StaticLiteral(word *syntax.Word, pool *syntax.AllocationBudget) (value string, static bool, err error) {
	if pool == nil {
		return "", false, fmt.Errorf("static literal requires an allocation budget")
	}
	if word == nil || len(word.Parts) == 0 {
		return "", true, nil
	}
	if len(word.Parts) > 0 {
		if first, ok := word.Parts[0].(*syntax.Lit); ok && strings.HasPrefix(first.Value, "~") {
			return "", false, nil
		}
	}
	pieces, maximum, static, err := staticPieces(word, pool)
	if err != nil || !static {
		return "", false, err
	}
	braces, err := syntax.ClassifyBraces(word, pool)
	if err != nil || braces {
		return "", false, err
	}
	if err := pool.ReserveHeap(maximum, word.Pos()); err != nil {
		return "", false, err
	}
	var output strings.Builder
	output.Grow(int(maximum))
	for _, piece := range pieces {
		if piece.quote == quoteSingle {
			output.WriteString(piece.text)
			continue
		}
		literalText(piece.text, piece.quote, func(text string, _ bool) { output.WriteString(text) })
	}
	return output.String(), true, nil
}

func staticPieces(word *syntax.Word, pool *syntax.AllocationBudget) ([]staticPiece, int64, bool, error) {
	frames, err := staticAppend(pool, []staticFrame(nil), staticFrame{parts: word.Parts})
	if err != nil {
		return nil, 0, false, err
	}
	var pieces []staticPiece
	var maximum int64
	for len(frames) > 0 {
		frame := &frames[len(frames)-1]
		if frame.index == len(frame.parts) {
			frames = frames[:len(frames)-1]
			continue
		}
		part := frame.parts[frame.index]
		frame.index++
		piece := staticPiece{quote: frame.quote}
		switch part := part.(type) {
		case *syntax.Lit:
			piece.text = part.Value
		case *syntax.SglQuoted:
			if part.Dollar {
				return nil, 0, false, nil
			}
			piece.text, piece.quote = part.Value, quoteSingle
		case *syntax.DblQuoted:
			if part.Dollar {
				return nil, 0, false, nil
			}
			frames, err = staticAppend(pool, frames, staticFrame{parts: part.Parts, quote: quoteDouble})
			if err != nil {
				return nil, 0, false, err
			}
			continue
		default:
			return nil, 0, false, nil
		}
		if strings.IndexByte(piece.text, 0) >= 0 {
			return nil, 0, false, nil
		}
		if int64(len(piece.text)) > math.MaxInt64-maximum {
			return nil, 0, false, pool.ReserveHeap(math.MaxInt64, word.Pos())
		}
		maximum += int64(len(piece.text))
		pieces, err = staticAppend(pool, pieces, piece)
		if err != nil {
			return nil, 0, false, err
		}
	}
	return pieces, maximum, true, nil
}

func staticAppend[T any](pool *syntax.AllocationBudget, dst []T, value T) ([]T, error) {
	if len(dst) == cap(dst) {
		if cap(dst) > (math.MaxInt-1)/2 {
			return nil, pool.ReserveHeap(math.MaxInt64, syntax.Pos{})
		}
		capacity := 2*cap(dst) + 1
		size := int64(unsafe.Sizeof(value))
		if size > 0 && int64(capacity) > math.MaxInt64/size {
			return nil, pool.ReserveHeap(math.MaxInt64, syntax.Pos{})
		}
		if err := pool.ReserveHeap(int64(capacity)*size, syntax.Pos{}); err != nil {
			return nil, err
		}
		replacement := make([]T, len(dst), capacity)
		copy(replacement, dst)
		dst = replacement
	}
	return append(dst, value), nil
}
