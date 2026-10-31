package commandprogram

import (
	"context"
	"fmt"
	"strconv"

	"github.com/lycaon/lycaon/internal/commandsurface"
)

// braceWords yields each word of a field word's brace expansion in order,
// before any other expansion; a word without braces is yielded unchanged.
// Admission bounded the total, and every field still counts against
// expanded_fields as the yielded words assemble.
func (r *expansionRun) braceWords(ctx context.Context, parts []commandsurface.WordNode, yield func([]commandsurface.WordNode) error) error {
	if !hasBrace(parts) {
		return yield(parts)
	}
	return r.splitBraceWords(ctx, parts, func(word []commandsurface.WordNode) error {
		joined, err := r.joinLiterals(ctx, word)
		if err != nil {
			return err
		}
		return yield(joined)
	})
}

func hasBrace(parts []commandsurface.WordNode) bool {
	for _, part := range parts {
		if _, ok := part.(commandsurface.BracePart); ok {
			return true
		}
	}
	return false
}

// splitBraceWords expands the leftmost brace part, then the braces of each
// resulting word, as bash does.
func (r *expansionRun) splitBraceWords(ctx context.Context, parts []commandsurface.WordNode, yield func([]commandsurface.WordNode) error) error {
	at := -1
	for i, part := range parts {
		if _, ok := part.(commandsurface.BracePart); ok {
			at = i
			break
		}
	}
	if at < 0 {
		return yield(parts)
	}
	brace := parts[at].(commandsurface.BracePart)
	expand := func(middle []commandsurface.WordNode) error {
		if err := r.charge(ctx, int64(len(parts)+len(middle))*pieceBytes); err != nil {
			return err
		}
		next := make([]commandsurface.WordNode, 0, len(parts)+len(middle))
		next = append(append(append(next, parts[:at]...), middle...), parts[at+1:]...)
		return r.splitBraceWords(ctx, next, yield)
	}
	if sequence := brace.Sequence; sequence != nil {
		for i := range sequence.Count() {
			if err := ctx.Err(); err != nil {
				return err
			}
			value, err := r.braceValue(ctx, *sequence, i)
			if err != nil {
				return err
			}
			if err := expand([]commandsurface.WordNode{commandsurface.LiteralPart{SyntaxLocation: brace.SyntaxLocation, Value: value}}); err != nil {
				return err
			}
		}
		return nil
	}
	for _, alternative := range brace.Alternatives {
		if err := expand(alternative.Parts); err != nil {
			return err
		}
	}
	return nil
}

// braceValue formats one sequence value: a letter, or a decimal integer
// zero-padded to the sequence width.
func (r *expansionRun) braceValue(ctx context.Context, sequence commandsurface.BraceSequence, i uint64) (string, error) {
	value := sequence.Value(i)
	if sequence.Letters {
		//nolint:gosec // Letter brace sequences are ASCII bytes in ['A','Z'] or ['a','z'].
		return string(rune(value)), nil
	}
	if err := r.charge(ctx, int64(sequence.Width)+20); err != nil {
		return "", err
	}
	if sequence.Width > 0 {
		return fmt.Sprintf("%0*d", sequence.Width, value), nil
	}
	return strconv.FormatInt(value, 10), nil
}

// joinLiterals merges adjacent literal parts, so a brace word's leading text
// is one literal for tilde and @label conversion, exactly as if written whole.
func (r *expansionRun) joinLiterals(ctx context.Context, parts []commandsurface.WordNode) ([]commandsurface.WordNode, error) {
	out := make([]commandsurface.WordNode, 0, len(parts))
	for _, part := range parts {
		literal, ok := part.(commandsurface.LiteralPart)
		if last := len(out) - 1; ok && last >= 0 {
			if previous, joined := out[last].(commandsurface.LiteralPart); joined {
				if err := r.charge(ctx, int64(len(previous.Value)+len(literal.Value))); err != nil {
					return nil, err
				}
				previous.Value += literal.Value
				out[last] = previous
				continue
			}
		}
		out = append(out, part)
	}
	return out, nil
}
