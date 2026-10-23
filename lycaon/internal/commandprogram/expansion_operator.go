package commandprogram

import (
	"strconv"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/lycaon/lycaon/internal/commandsurface"
	"mvdan.cc/sh/v3/expand"
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
		size := caseSize(value, part.Operator)
		if size > r.expander.limits.valueBytes {
			return r.expander.limits.exceeded("variable_value_bytes", size, part.Span)
		}
		if err := r.charge(size); err != nil {
			return err
		}
		return r.emit(convertCase(value, part.Operator), quoted)
	}
	return r.patternOperation(value, part, quoted)
}

// parameterState reads a parameter's value and whether it is set.
func (r *expansionRun) parameterState(part commandsurface.ParameterPart) (string, bool, error) {
	if part.Index != nil {
		index, err := r.arithmeticValue(part.Index)
		if err != nil {
			return "", false, err
		}
		if index < 0 {
			return "", false, invalidValue(part.Span, "a non-negative array subscript")
		}
		value, set := scopeArrayElement(r.scope, part.Name, index)
		return value, set, nil
	}
	if part.Subscript == "@" || part.Subscript == "*" {
		elems, set := scopeArrayElements(r.scope, part.Name)
		joined, err := r.joinParameterElements(elems)
		return joined, set && len(elems) > 0, err
	}
	switch name := part.Name; {
	case name == "@" || name == "*":
		positional := r.scope.Positional()
		joined, err := r.joinParameterElements(positional)
		return joined, len(positional) > 0, err
	case positionalName(name):
		index, err := strconv.Atoi(name)
		positional := r.scope.Positional()
		if err != nil || index > len(positional) {
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

func (r *expansionRun) joinParameterElements(values []string) (string, error) {
	var size int64
	for _, value := range values {
		size += int64(len(value))
	}
	if len(values) > 0 {
		size += int64(len(values) - 1)
	}
	if size > r.expander.limits.valueBytes {
		return "", r.expander.limits.exceeded("variable_value_bytes", size, r.span)
	}
	if err := r.charge(size); err != nil {
		return "", err
	}
	return strings.Join(values, " "), nil
}

func caseSize(value, operator string) int64 {
	var size int64
	for offset, ch := range value {
		if operator == "^" || operator == "," {
			_, width := utf8.DecodeRuneInString(value[offset:])
			if operator == "^" {
				ch = unicode.ToUpper(ch)
			} else {
				ch = unicode.ToLower(ch)
			}
			return int64(utf8.RuneLen(ch) + len(value) - width)
		}
		if operator == "^^" {
			ch = unicode.ToUpper(ch)
		} else {
			ch = unicode.ToLower(ch)
		}
		size += int64(utf8.RuneLen(ch))
	}
	return size
}
