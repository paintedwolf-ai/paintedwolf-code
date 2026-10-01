package structrewrite

import (
	"context"
	"strings"

	"github.com/odvcencio/gotreesitter"
)

// ellipsisHole records a sequence variable removed for parsing.
type ellipsisHole struct {
	offset int
	mv     metaVar
}

// holeCursor restores elided variables in source order.
type holeCursor struct {
	holes []ellipsisHole
	next  int
}

// drainThrough restores holes through the next child boundary.
func (h *holeCursor) drainThrough(kids []*patternNode, limit int) []*patternNode {
	return h.drain(kids, limit, true)
}

// drainBefore excludes holes exactly on a node's closing edge.
func (h *holeCursor) drainBefore(kids []*patternNode, limit int) []*patternNode {
	return h.drain(kids, limit, false)
}

func (h *holeCursor) drain(kids []*patternNode, limit int, inclusive bool) []*patternNode {
	if h == nil {
		return kids
	}
	for h.next < len(h.holes) {
		off := h.holes[h.next].offset
		if off > limit || (!inclusive && off == limit) {
			break
		}
		mv := h.holes[h.next].mv
		kids = append(kids, &patternNode{meta: &mv})
		h.next++
	}
	return kids
}

// placedAll reports whether every hole landed in the extracted subtree.
func (h *holeCursor) placedAll() bool {
	return h == nil || h.next == len(h.holes)
}

// isMetaNameRune reports whether r may appear in a metavariable name.
func isMetaNameRune(r rune, afterFirst bool) bool {
	switch {
	case r == '_' || (r >= 'A' && r <= 'Z'):
		return true
	case afterFirst && r >= '0' && r <= '9':
		return true
	default:
		return false
	}
}

// stripEllipses removes sequence variables and their orphaned separators.
func stripEllipses(processed string, expando rune) (string, []ellipsisHole) {
	rs := []rune(processed)
	out := make([]rune, 0, len(rs))
	var holes []ellipsisHole
	byteLen := func(rr []rune) int { return len(string(rr)) }

	for i := 0; i < len(rs); {
		if rs[i] != expando {
			out = append(out, rs[i])
			i++
			continue
		}
		run := i
		for run < len(rs) && rs[run] == expando {
			run++
		}
		if run-i != 3 {
			out = append(out, rs[i:run]...)
			i = run
			continue
		}
		nameEnd := run
		for nameEnd < len(rs) && isMetaNameRune(rs[nameEnd], nameEnd > run) {
			nameEnd++
		}
		name := string(rs[run:nameEnd])
		if name != "" && !isMetaName(name) {
			out = append(out, rs[i:nameEnd]...)
			i = nameEnd
			continue
		}
		out, i = elideOne(out, rs, i, nameEnd)
		holes = append(holes, ellipsisHole{
			offset: byteLen(out),
			mv:     metaVar{name: name, ellipsis: true, capture: name != "" && name != "_"},
		})
	}
	return string(out), holes
}

// elideOne removes one sequence variable and its list separator.
func elideOne(out, rs []rune, start, end int) ([]rune, int) {
	i := end
	for i < len(rs) && (rs[i] == ' ' || rs[i] == '\t' || rs[i] == '\n') {
		i++
	}
	if i < len(rs) && rs[i] == ',' {
		i++
		for i < len(rs) && (rs[i] == ' ' || rs[i] == '\t' || rs[i] == '\n') {
			i++
		}
		return out, i
	}
	// Remove a preceding separator when no trailing separator exists.
	trimmed := len(out)
	for trimmed > 0 && (out[trimmed-1] == ' ' || out[trimmed-1] == '\t' || out[trimmed-1] == '\n') {
		trimmed--
	}
	if trimmed > 0 && out[trimmed-1] == ',' {
		return out[:trimmed-1], end
	}
	return out, end
}

// compileElided parses a pattern with sequence variables removed and restored.
func compileElided(ctx context.Context, lang *gotreesitter.Language, langName, processed string, expando rune) (*compiledPattern, error) {
	stripped, holes := stripEllipses(processed, expando)
	if len(holes) == 0 || strings.TrimSpace(stripped) == "" {
		return nil, nil
	}
	for _, wrapper := range append(patternContexts(langName), patternWrapper{}) {
		candidate, err := parsePattern(ctx, lang, stripped, wrapper)
		if err != nil {
			return nil, err
		}
		if candidate == nil {
			continue
		}
		cursor := &holeCursor{holes: shiftHoles(holes, len(wrapper.prefix))}
		root := candidate.lower(lang, expando, cursor)
		if cursor.placedAll() && patternFaithful(root, stripped) {
			return &compiledPattern{root: root, expando: expando}, nil
		}
	}
	return nil, nil
}

// shiftHoles rebases hole offsets into wrapper coordinates.
func shiftHoles(holes []ellipsisHole, shift int) []ellipsisHole {
	if shift == 0 {
		return holes
	}
	out := make([]ellipsisHole, len(holes))
	for i, h := range holes {
		out[i] = ellipsisHole{offset: h.offset + shift, mv: h.mv}
	}
	return out
}
