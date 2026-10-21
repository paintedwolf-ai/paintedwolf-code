package commandprogram

import (
	"errors"
	"strings"

	"github.com/lycaon/lycaon/internal/commandsurface"
)

// fields materializes the assembled fields. A field with an active unquoted
// glob character is a pathname pattern over the logical-path view: sorted
// matches replace it, and a pattern with no match stays literal.
func (r *expansionRun) fields() (Fields, error) {
	limits := r.expander.limits
	out := make(Fields, 0, len(r.assembly.complete))
	var argv int64
	appendField := func(value string) error {
		size := int64(len(value))
		if size > limits.argumentBytes {
			return limits.exceeded("argument_bytes", size, r.span)
		}
		if argv += size; argv > limits.argvBytes {
			return limits.exceeded("argv_bytes", argv, r.span)
		}
		if int64(len(out)+1) > limits.fields {
			return limits.exceeded("expanded_fields", int64(len(out)+1), r.span)
		}
		out = append(out, Field{Value: value, Span: r.span})
		return nil
	}
	for _, pieces := range r.assembly.complete {
		value, err := r.join(pieces, limits.argumentBytes, "argument_bytes")
		if err != nil {
			return nil, err
		}
		matches, err := r.glob(pieces)
		if err != nil {
			return nil, err
		}
		if len(matches) == 0 {
			matches = []string{value}
		}
		for _, match := range matches {
			if err := appendField(match); err != nil {
				return nil, err
			}
		}
	}
	return out, nil
}

// glob matches a field whose unquoted text has an active glob character.
// Quoted text is escaped, so it matches literally.
func (r *expansionRun) glob(pieces []expansionPiece) ([]string, error) {
	active := false
	var size int64
	for _, piece := range pieces {
		size += int64(len(piece.text))
		active = active || (!piece.quoted && strings.ContainsAny(piece.text, "*?["))
	}
	if !active {
		return nil, nil
	}
	if err := r.charge(2 * size); err != nil {
		return nil, err
	}
	var pattern strings.Builder
	pattern.Grow(int(2 * size))
	for _, piece := range pieces {
		if !piece.quoted {
			pattern.WriteString(piece.text)
			continue
		}
		for i := 0; i < len(piece.text); i++ {
			if strings.IndexByte(`\*?[`, piece.text[i]) >= 0 {
				pattern.WriteByte('\\')
			}
			pattern.WriteByte(piece.text[i])
		}
	}
	if !commandsurface.GlobActive(pattern.String()) {
		return nil, nil
	}
	return r.expander.match(r, pattern.String())
}

// match walks one pattern against the program's remaining glob budgets.
// Walks are serialized so concurrent words cannot overdraw them.
func (e *WordExpander) match(r *expansionRun, pattern string) ([]string, error) {
	e.globMu.Lock()
	budget := commandsurface.GlobBudget{Matches: int(e.limits.globMatches - e.globMatches), Visits: int(e.limits.visits - e.globVisits)}
	remaining := budget
	matches, err := commandsurface.MatchGlob(r.ctx, e.files, pattern, &budget)
	e.globMatches += int64(remaining.Matches - budget.Matches)
	e.globVisits += int64(remaining.Visits - budget.Visits)
	e.globMu.Unlock()
	var exceeded *commandsurface.GlobBudgetError
	if errors.As(err, &exceeded) {
		id := "glob_matches"
		if exceeded.Limit == commandsurface.GlobLimitEntries {
			id = "traversal_visits"
		}
		return nil, e.limits.exceeded(id, e.limits.maximum[id]+1, r.span)
	}
	if err != nil {
		return nil, err
	}
	var size int64
	for _, match := range matches {
		size += int64(len(match)) + 16
	}
	if err := r.charge(size); err != nil {
		return nil, err
	}
	return matches, nil
}
