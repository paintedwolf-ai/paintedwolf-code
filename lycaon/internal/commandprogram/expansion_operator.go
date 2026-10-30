package commandprogram

import (
	"regexp"
	"strconv"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/lycaon/lycaon/internal/commandsurface"
	"mvdan.cc/sh/v3/expand"
	"mvdan.cc/sh/v3/pattern"
)

// operated expands ${name OP word}. Operators read state only; none assigns.
func (r *expansionRun) operated(part commandsurface.ParameterPart, quoted bool) error {
	value, set, err := r.parameterState(part)
	if err != nil {
		return err
	}
	colon := strings.HasPrefix(part.Operator, ":")
	missing := !set || (colon && value == "")
	switch strings.TrimPrefix(part.Operator, ":") {
	case "-":
		if missing {
			return r.operand(part.Argument, quoted)
		}
		return r.emit(value, quoted)
	case "+":
		if missing {
			return r.emit("", quoted)
		}
		return r.operand(part.Argument, quoted)
	case "?":
		if !missing {
			return r.emit(value, quoted)
		}
		message := "parameter null or not set"
		if part.Argument != nil && len(part.Argument.Parts) > 0 {
			if message, err = r.expander.ExpandScalar(r.ctx, r.scope, *part.Argument); err != nil {
				return err
			}
		}
		return invalidValue(part.Span, part.Name+": "+message)
	case "^", "^^", ",", ",,":
		return r.emit(convertCase(value, part.Operator), quoted)
	}
	compiled, err := r.parameterPattern(part)
	if err != nil {
		return err
	}
	if compiled == nil {
		return r.emit(value, quoted)
	}
	expression := &boundedPattern{expression: compiled, maximum: r.expander.limits.maximum["regex_work_steps"]}
	switch part.Operator {
	case "#", "##", "%", "%%":
		removed := removeMatch(expression, value, part.Operator)
		if expression.exceeded() {
			return r.expander.limits.exceeded("regex_work_steps", expression.steps, part.Span)
		}
		return r.emit(removed, quoted)
	}
	replacement := ""
	if part.Replacement != nil {
		if replacement, err = r.expander.ExpandScalar(r.ctx, r.scope, *part.Replacement); err != nil {
			return err
		}
	}
	replaced := replaceMatch(expression, value, replacement, part.Operator)
	if expression.exceeded() {
		return r.expander.limits.exceeded("regex_work_steps", expression.steps, part.Span)
	}
	if int64(len(replaced)) > r.expander.limits.valueBytes {
		return r.expander.limits.exceeded("variable_value_bytes", int64(len(replaced)), part.Span)
	}
	if err := r.charge(int64(len(replaced))); err != nil {
		return err
	}
	return r.emit(replaced, quoted)
}

// parameterState reads a parameter's value and whether it is set.
func (r *expansionRun) parameterState(part commandsurface.ParameterPart) (string, bool, error) {
	switch name := part.Name; {
	case name == "@" || name == "*":
		positional := r.scope.Positional()
		return strings.Join(positional, " "), len(positional) > 0, nil
	case positionalName(name):
		index, err := strconv.Atoi(name)
		positional := r.scope.Positional()
		if err != nil || index > len(positional) {
			//nolint:nilerr // An unrepresentable positional index is unset, as is any out-of-range index.
			return "", false, nil
		}
		return positional[index-1], true, nil
	case name == "!":
		job, set := r.scope.LastBackground()
		return job, set, nil
	case VariableName(name):
		value, set := r.scope.Variable(name)
		return value, set, nil
	}
	value, err := parameterValue(r.scope, part)
	return value, err == nil, err
}

// operand expands an operator word in place. Within double quotes its text
// stays quoted, as the enclosing quotes still apply.
func (r *expansionRun) operand(word *commandsurface.DeferredWord, quoted bool) error {
	if word == nil {
		return r.emit("", quoted)
	}
	if !quoted {
		return r.word(word.Parts)
	}
	r.assembly.present = true
	for _, inner := range word.Parts {
		var err error
		switch inner := inner.(type) {
		case commandsurface.LiteralPart:
			err = r.literal(inner.Value, expand.DoubleQuotedText)
		case commandsurface.SingleQuotedPart:
			err = r.assembly.add("'"+inner.Value+"'", true)
		case commandsurface.DoubleQuotedPart:
			err = r.double(inner)
		case commandsurface.ParameterPart:
			err = r.parameter(inner, true)
		case commandsurface.ArithmeticPart:
			err = r.arithmetic(inner, true)
		case commandsurface.SubstitutionPart:
			err = r.substitution(inner, true)
		default:
			err = &ExpansionError{Code: codeSyntaxUnsupported, Span: r.span, Expected: "a version 1 word part"}
		}
		if err != nil {
			return err
		}
	}
	return nil
}

// parameterPattern compiles the operator's glob; quoted fragments are
// literal. An empty pattern matches nothing to remove or replace.
func (r *expansionRun) parameterPattern(part commandsurface.ParameterPart) (*regexp.Regexp, error) {
	if part.Argument == nil {
		return nil, nil
	}
	value, err := r.expander.ExpandPattern(r.ctx, r.scope, *part.Argument)
	if err != nil {
		return nil, err
	}
	var glob strings.Builder
	for _, fragment := range value {
		if fragment.Quoted {
			glob.WriteString(pattern.QuoteMeta(fragment.Value, 0))
		} else {
			glob.WriteString(fragment.Value)
		}
	}
	if glob.Len() == 0 {
		return nil, nil
	}
	if size := int64(glob.Len()); size > r.expander.limits.maximum["regex_pattern_bytes"] {
		return nil, r.expander.limits.exceeded("regex_pattern_bytes", size, part.Argument.Span)
	}
	expression, err := pattern.Regexp(glob.String(), pattern.EntireString)
	if err != nil {
		return nil, invalidValue(part.Argument.Span, "a shell pattern")
	}
	compiled, err := regexp.Compile("(?s)" + expression)
	if err != nil {
		return nil, invalidValue(part.Argument.Span, "a shell pattern")
	}
	return compiled, nil
}

// removeMatch drops the shortest or longest matching prefix (#, ##) or
// suffix (%, %%). Candidates are character boundaries.
func removeMatch(expression *boundedPattern, value, operator string) string {
	bounds := characterBounds(value)
	switch operator {
	case "#":
		for _, end := range bounds {
			if expression.MatchString(value[:end]) {
				return value[end:]
			}
		}
	case "##":
		for i := len(bounds) - 1; i >= 0; i-- {
			if expression.MatchString(value[:bounds[i]]) {
				return value[bounds[i]:]
			}
		}
	case "%":
		for i := len(bounds) - 1; i >= 0; i-- {
			if expression.MatchString(value[bounds[i]:]) {
				return value[:bounds[i]]
			}
		}
	case "%%":
		for _, start := range bounds {
			if expression.MatchString(value[start:]) {
				return value[:start]
			}
		}
	}
	return value
}

// replaceMatch replaces the first (/), every (//), the leading (/#) or the
// trailing (/%) longest match.
func replaceMatch(expression *boundedPattern, value, replacement, operator string) string {
	bounds := characterBounds(value)
	longest := func(start int) int {
		for i := len(bounds) - 1; i >= 0 && bounds[i] >= start; i-- {
			if expression.MatchString(value[start:bounds[i]]) {
				return bounds[i]
			}
		}
		return -1
	}
	switch operator {
	case "/#":
		if end := longest(0); end >= 0 {
			return replacement + value[end:]
		}
		return value
	case "/%":
		for _, start := range bounds {
			if expression.MatchString(value[start:]) {
				return value[:start] + replacement
			}
		}
		return value
	}
	var out strings.Builder
	copied := 0
	for i := 0; i < len(bounds); i++ {
		start := bounds[i]
		if start < copied {
			continue
		}
		end := longest(start)
		if end <= start {
			continue
		}
		out.WriteString(value[copied:start])
		out.WriteString(replacement)
		copied = end
		if operator == "/" {
			break
		}
	}
	out.WriteString(value[copied:])
	return out.String()
}

// boundedPattern counts whole-candidate match attempts against the catalog
// work ceiling; once over it, every further attempt fails.
type boundedPattern struct {
	expression     *regexp.Regexp
	steps, maximum int64
}

func (b *boundedPattern) MatchString(candidate string) bool {
	b.steps++
	return !b.exceeded() && b.expression.MatchString(candidate)
}

func (b *boundedPattern) exceeded() bool { return b.steps > b.maximum }

// characterBounds lists every character boundary of value, including 0 and len.
func characterBounds(value string) []int {
	bounds := make([]int, 0, len(value)+1)
	for i := range value {
		bounds = append(bounds, i)
	}
	return append(bounds, len(value))
}

func convertCase(value, operator string) string {
	switch operator {
	case "^^":
		return strings.ToUpper(value)
	case ",,":
		return strings.ToLower(value)
	}
	first, width := utf8.DecodeRuneInString(value)
	if width == 0 {
		return value
	}
	if operator == "^" {
		first = unicode.ToUpper(first)
	} else {
		first = unicode.ToLower(first)
	}
	return string(first) + value[width:]
}
