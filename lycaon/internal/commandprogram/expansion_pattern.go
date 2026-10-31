package commandprogram

import (
	"context"
	"errors"
	"regexp/syntax"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/dlclark/regexp2/v2"
	"github.com/lycaon/lycaon/internal/commandsurface"
	"mvdan.cc/sh/v3/pattern"
)

// parameterMatcher uses the existing metered parser and VM for all candidates
// of one parameter operator. Compilation, retained text and result fragments
// join the expansion's aggregate admission before allocation.
type parameterMatcher struct {
	run     *expansionRun
	session *regexp2.BoundedSession
	text    []rune
	bounds  []int
	err     error
}

func (r *expansionRun) patternOperation(value string, part commandsurface.ParameterPart, quoted bool) error {
	matcher, err := r.parameterPattern(value, part)
	if err != nil {
		return err
	}
	if matcher == nil {
		return r.emit(r.ctx, value, quoted)
	}
	defer matcher.session.Close()
	var result string
	switch part.Operator {
	case "#", "##", "%", "%%":
		result = matcher.remove(value, part.Operator)
	default:
		replacement := ""
		if part.Replacement != nil {
			replacement, err = r.expander.ExpandScalar(r.ctx, r.scope, *part.Replacement)
			if err != nil {
				return err
			}
		}
		result = matcher.replace(value, replacement, part.Operator)
	}
	if matcher.err != nil {
		return matcher.failure(matcher.err)
	}
	return r.emit(r.ctx, result, quoted)
}

func (r *expansionRun) parameterPattern(value string, part commandsurface.ParameterPart) (*parameterMatcher, error) {
	if part.Argument == nil {
		return nil, nil
	}
	fragments, err := r.expander.ExpandPattern(r.ctx, r.scope, *part.Argument)
	if err != nil {
		return nil, err
	}
	var raw int64
	for _, fragment := range fragments {
		raw += int64(len(fragment.Value))
	}
	if raw == 0 {
		return nil, nil
	}
	if raw > r.expander.limits.maximum["regex_pattern_bytes"] {
		return nil, r.expander.limits.exceeded("regex_pattern_bytes", raw, part.Argument.Span)
	}
	if int64(len(value)) > r.expander.limits.maximum["regex_input_bytes"] {
		return nil, r.expander.limits.exceeded("regex_input_bytes", int64(len(value)), part.Span)
	}
	// Share the predicate owner's bound for quoted glob translation and syntax
	// normalization. The maintained parser/VM meter their dynamic allocation.
	if err := r.charge(r.ctx, patternLexicalBytes(raw)); err != nil {
		return nil, err
	}
	var glob strings.Builder
	for _, fragment := range fragments {
		if fragment.Quoted {
			glob.WriteString(pattern.QuoteMeta(fragment.Value, 0))
		} else {
			glob.WriteString(fragment.Value)
		}
	}
	expression, err := pattern.Regexp(glob.String(), pattern.EntireString|pattern.ScalarCollation)
	if err != nil {
		return nil, invalidValue(part.Argument.Span, "a shell pattern")
	}
	matcher := &parameterMatcher{run: r}
	reserve := func(callCtx context.Context, n int64) (func(), error) {
		if err := r.charge(callCtx, n); err != nil {
			return nil, err
		}
		// Compiled code and parse generations stay charged until expansion closes.
		return func() {}, nil
	}
	limits := regexp2.SyntaxBudget{MaxBytes: r.expander.limits.memory, MaxSteps: r.expander.limits.maximum["regex_work_steps"], Reserve: reserve}
	tree, err := regexp2.ParseSyntaxBounded(r.ctx, expression, syntax.Perl|syntax.DotNL|regexp2.ScalarCollation, limits)
	if err != nil {
		return nil, matcher.failure(err)
	}
	program, err := regexp2.CompileMarkedBounded(r.ctx, tree, limits)
	if err != nil {
		return nil, matcher.failure(err)
	}
	count := utf8.RuneCountInString(value)
	if err := r.charge(r.ctx, int64(count)*12+8); err != nil {
		return nil, err
	}
	matcher.text = []rune(value)
	matcher.bounds = make([]int, 0, count+1)
	for i := range value {
		matcher.bounds = append(matcher.bounds, i)
	}
	matcher.bounds = append(matcher.bounds, len(value))
	matcher.session, err = program.Session(r.ctx, limits)
	if err != nil {
		return nil, matcher.failure(err)
	}
	return matcher, nil
}

func patternLexicalBytes(raw int64) int64 { return 1024 + (raw+1)*(raw+1)*8 }

func (m *parameterMatcher) failure(err error) error {
	var limit *regexp2.SyntaxBudgetError
	if errors.As(err, &limit) {
		id := "expansion_memory_bytes"
		observed := limit.Observed
		switch limit.Resource {
		case "steps":
			id = "regex_work_steps"
		case "time":
			id = "regex_cpu_ms"
			observed = (observed + int64(time.Millisecond) - 1) / int64(time.Millisecond)
		}
		return m.run.expander.limits.exceeded(id, observed, m.run.span)
	}
	return err
}

func (m *parameterMatcher) matches(start, end int) bool {
	if m.err != nil {
		return false
	}
	groups, err := m.session.Find(m.text[start:end], 0, false)
	m.err = err
	return err == nil && len(groups) > 1 && groups[0] == 0 && groups[1] == end-start
}

func (m *parameterMatcher) remove(value, operator string) string {
	n := len(m.text)
	switch operator {
	case "#":
		for end := 0; end <= n && m.err == nil; end++ {
			if m.matches(0, end) {
				return value[m.bounds[end]:]
			}
		}
	case "##":
		for end := n; end >= 0 && m.err == nil; end-- {
			if m.matches(0, end) {
				return value[m.bounds[end]:]
			}
		}
	case "%":
		for start := n; start >= 0 && m.err == nil; start-- {
			if m.matches(start, n) {
				return value[:m.bounds[start]]
			}
		}
	case "%%":
		for start := 0; start <= n && m.err == nil; start++ {
			if m.matches(start, n) {
				return value[:m.bounds[start]]
			}
		}
	}
	return value
}

func (m *parameterMatcher) longest(start int) int {
	for end := len(m.text); end >= start && m.err == nil; end-- {
		if m.matches(start, end) {
			return end
		}
	}
	return -1
}

// replace retains source/replacement references while finding the exact output
// size. It admits every fragment and the final buffer before either is built.
func (m *parameterMatcher) replace(value, replacement, operator string) string {
	var pieces []string
	var size int64
	appendText := func(text string) bool {
		next := size + int64(len(text))
		if next > m.run.expander.limits.valueBytes {
			m.err = m.run.expander.limits.exceeded("variable_value_bytes", next, m.run.span)
			return false
		}
		if err := m.run.charge(m.run.ctx, 48); err != nil {
			m.err = err
			return false
		}
		pieces = append(pieces, text)
		size = next
		return true
	}
	copied := 0
	for start := 0; start < len(m.bounds) && m.err == nil; start++ {
		if start < copied {
			continue
		}
		end := -1
		switch operator {
		case "/%":
			if m.matches(start, len(m.text)) {
				end = len(m.text)
			}
		default:
			end = m.longest(start)
		}
		if end < start || (end == start && operator != "/#" && operator != "/%") {
			if operator == "/#" {
				break
			}
			continue
		}
		if !appendText(value[m.bounds[copied]:m.bounds[start]]) || !appendText(replacement) {
			return ""
		}
		copied = end
		if operator != "//" {
			break
		}
	}
	if m.err != nil {
		return ""
	}
	if !appendText(value[m.bounds[copied]:]) {
		return ""
	}
	if err := m.run.charge(m.run.ctx, size); err != nil {
		m.err = err
		return ""
	}
	var out strings.Builder
	out.Grow(int(size))
	for _, piece := range pieces {
		out.WriteString(piece)
	}
	return out.String()
}
