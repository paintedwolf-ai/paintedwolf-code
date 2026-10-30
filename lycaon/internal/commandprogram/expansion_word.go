package commandprogram

import (
	"context"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/lycaon/lycaon/internal/commandsurface"
	"github.com/lycaon/lycaon/internal/toolcontract"
	"mvdan.cc/sh/v3/expand"
)

// expansionPiece is expanded text with its quotation. Quoted text is never
// split and is literal in a pattern.
type expansionPiece struct {
	text   string
	quoted bool
}

// pieceBytes conservatively covers one piece record and its slice growth.
const pieceBytes = 48

type documentText struct{ literal, stripTabs, lineStart bool }

// expansionRun is the working state of one word. Fields mode assembles fields
// with fixed space/tab/LF splitting of unquoted expansions; scalar and pattern
// modes keep one value.
type expansionRun struct {
	ctx      context.Context
	expander *WordExpander
	scope    ExpansionScope
	span     toolcontract.SourceSpan
	mode     expansionMode
	document *documentText

	assembly fieldAssembly
	charged  int64
	releases []func()
}

func (r *expansionRun) word(parts []commandsurface.WordNode) error {
	for i, part := range parts {
		if err := r.ctx.Err(); err != nil {
			return err
		}
		var err error
		switch part := part.(type) {
		case commandsurface.LiteralPart:
			err = r.literalPart(part.Value, i == 0)
		case commandsurface.SingleQuotedPart:
			err = r.assembly.add(part.Value, true)
		case commandsurface.DoubleQuotedPart:
			err = r.double(part)
		case commandsurface.ParameterPart:
			err = r.parameter(part, r.document != nil)
		case commandsurface.ArithmeticPart:
			err = r.arithmetic(part, r.document != nil)
		case commandsurface.SubstitutionPart:
			err = r.substitution(part, r.document != nil)
		default:
			err = &ExpansionError{Code: codeSyntaxUnsupported, Span: r.span, Expected: "a version 1 word part"}
		}
		if err != nil {
			return err
		}
	}
	return nil
}

func (r *expansionRun) literalPart(text string, first bool) error {
	if r.document != nil {
		return r.documentLiteral(text)
	}
	if first && strings.HasPrefix(text, "~") && (len(text) == 1 || text[1] == '/') {
		// Only ~ and ~/ reach evaluation; account lookup is refused at parse.
		if home, set := r.scope.Variable("HOME"); set {
			if err := r.assembly.add(home, true); err != nil {
				return err
			}
			text = text[1:]
		}
	}
	return r.literal(text, expand.UnquotedText)
}

// literal applies the selected quote removal. Escaped text is quoted.
func (r *expansionRun) literal(text string, context expand.QuoteContext) error {
	var err error
	expand.RemoveQuotes(text, context, func(piece string, escaped bool) {
		if err == nil && piece != "" {
			if !escaped && context == expand.UnquotedText && r.mode == modeFields && r.document == nil {
				err = r.assembly.split(piece)
			} else {
				err = r.assembly.add(piece, escaped || context != expand.UnquotedText)
			}
		}
	})
	return err
}

func (r *expansionRun) documentLiteral(text string) error {
	d := r.document
	if d.stripTabs {
		if err := r.charge(int64(len(text))); err != nil {
			return err
		}
		var stripped strings.Builder
		stripped.Grow(len(text))
		for i := 0; i < len(text); i++ {
			if d.lineStart && text[i] == '\t' {
				continue
			}
			d.lineStart = text[i] == '\n'
			stripped.WriteByte(text[i])
		}
		text = stripped.String()
	}
	if d.literal {
		return r.assembly.add(text, true)
	}
	return r.literal(text, expand.HereDocumentText)
}

func (r *expansionRun) double(part commandsurface.DoubleQuotedPart) error {
	if !r.onlyEmptyPositional(part.Parts) {
		r.assembly.present = true
	}
	for _, inner := range part.Parts {
		var err error
		switch inner := inner.(type) {
		case commandsurface.LiteralPart:
			err = r.literal(inner.Value, expand.DoubleQuotedText)
		case commandsurface.ParameterPart:
			err = r.parameter(inner, true)
		case commandsurface.ArithmeticPart:
			err = r.arithmetic(inner, true)
		case commandsurface.SubstitutionPart:
			err = r.substitution(inner, true)
		default:
			err = &ExpansionError{Code: codeSyntaxUnsupported, Span: r.span, Expected: "a version 1 double-quoted part"}
		}
		if err != nil {
			return err
		}
	}
	return nil
}

// onlyEmptyPositional reports "$@" with no positional parameters, which
// expands to zero fields rather than one empty field.
func (r *expansionRun) onlyEmptyPositional(parts []commandsurface.WordNode) bool {
	if r.mode != modeFields || len(parts) == 0 || len(r.scope.Positional()) > 0 {
		return false
	}
	for _, part := range parts {
		if p, ok := part.(commandsurface.ParameterPart); !ok || p.Name != "@" || p.Length {
			return false
		}
	}
	return true
}

func (r *expansionRun) parameter(part commandsurface.ParameterPart, quoted bool) error {
	if part.Operator != "" {
		return r.operated(part, quoted)
	}
	if (part.Name == "@" || part.Name == "*") && !part.Length {
		positional := r.scope.Positional()
		if quoted && part.Name == "@" && r.mode == modeFields {
			for i, value := range positional {
				if i > 0 {
					if err := r.assembly.push(); err != nil {
						return err
					}
				}
				if err := r.assembly.add(value, true); err != nil {
					return err
				}
			}
			return nil
		}
		joined := 0
		for _, value := range positional {
			joined += len(value) + 1
		}
		if err := r.charge(int64(joined)); err != nil {
			return err
		}
		return r.emit(strings.Join(positional, " "), quoted)
	}
	if (part.Name == "PIPESTATUS[@]" || part.Name == "PIPESTATUS[*]") && !part.Length {
		statuses := scopePipeStatuses(r.scope)
		if quoted && part.Name == "PIPESTATUS[@]" && r.mode == modeFields {
			for i, value := range statuses {
				if i > 0 {
					if err := r.assembly.push(); err != nil {
						return err
					}
				}
				if err := r.assembly.add(value, true); err != nil {
					return err
				}
			}
			return nil
		}
		joined := 0
		for _, value := range statuses {
			joined += len(value) + 1
		}
		if err := r.charge(int64(joined)); err != nil {
			return err
		}
		return r.emit(strings.Join(statuses, " "), quoted)
	}
	if err := r.unbound(part); err != nil {
		return err
	}
	value, err := parameterValue(r.scope, part)
	if err != nil {
		return err
	}
	if part.Length && part.Name != "@" && part.Name != "*" && part.Name != "PIPESTATUS[@]" && part.Name != "PIPESTATUS[*]" {
		value = strconv.Itoa(utf8.RuneCountInString(value))
	}
	return r.emit(value, quoted)
}

func scopePipeStatuses(scope ExpansionScope) []string {
	var raw []int
	if ps, ok := scope.(interface{ PipeStatus() []int }); ok {
		raw = ps.PipeStatus()
	}
	if len(raw) == 0 {
		raw = []int{scope.LastStatus()}
	}
	out := make([]string, len(raw))
	for i, status := range raw {
		out[i] = strconv.Itoa(status)
	}
	return out
}

// parameterValue reads one parameter; @ and * reach it only as ${#@} and ${#*}.
func parameterValue(scope ExpansionScope, part commandsurface.ParameterPart) (string, error) {
	switch name := part.Name; {
	case name == "@" || name == "*" || name == "#":
		return strconv.Itoa(len(scope.Positional())), nil
	case name == "PIPESTATUS[@]" || name == "PIPESTATUS[*]":
		return strconv.Itoa(len(scopePipeStatuses(scope))), nil
	case name == "PIPESTATUS":
		return scopePipeStatuses(scope)[0], nil
	case strings.HasPrefix(name, "PIPESTATUS[") && strings.HasSuffix(name, "]"):
		index, err := strconv.Atoi(name[len("PIPESTATUS[") : len(name)-1])
		statuses := scopePipeStatuses(scope)
		if err != nil || index < 0 || index >= len(statuses) {
			//nolint:nilerr // An unrepresentable status index is unset, as is any out-of-range index.
			return "", nil
		}
		return statuses[index], nil
	case name == "?":
		return strconv.Itoa(scope.LastStatus()), nil
	case name == "!":
		job, _ := scope.LastBackground()
		return job, nil
	case name == "0":
		return scope.ProgramName(), nil
	case positionalName(name):
		index, err := strconv.Atoi(name)
		positional := scope.Positional()
		if err != nil || index > len(positional) {
			//nolint:nilerr // An unrepresentable positional index is unset, as is any out-of-range index.
			return "", nil
		}
		return positional[index-1], nil
	case VariableName(name):
		value, _ := scope.Variable(name)
		return value, nil
	}
	return "", &ExpansionError{Code: codeSyntaxUnsupported, Span: part.Span, Expected: "invocation variables and supported positional/status parameters"}
}

func positionalName(name string) bool {
	if name == "" || name[0] < '1' || name[0] > '9' {
		return false
	}
	for i := 1; i < len(name); i++ {
		if name[i] < '0' || name[i] > '9' {
			return false
		}
	}
	return true
}

func (r *expansionRun) substitution(part commandsurface.SubstitutionPart, quoted bool) error {
	output, err := r.expander.substitute(r, part)
	if err != nil {
		return err
	}
	return r.emit(output, quoted)
}

// emit adds expansion output. Unquoted output splits in fields mode.
func (r *expansionRun) emit(value string, quoted bool) error {
	if r.document != nil {
		r.document.lineStart = false
	}
	if quoted || r.mode != modeFields {
		return r.assembly.add(value, quoted)
	}
	return r.assembly.split(value)
}

func (r *expansionRun) join(pieces []expansionPiece, maximum int64, id string) (string, error) {
	if len(pieces) == 1 {
		if size := int64(len(pieces[0].text)); size > maximum {
			return "", r.expander.limits.exceeded(id, size, r.span)
		}
		return pieces[0].text, nil
	}
	var size int64
	for _, piece := range pieces {
		size += int64(len(piece.text))
	}
	if size > maximum {
		return "", r.expander.limits.exceeded(id, size, r.span)
	}
	if err := r.charge(size); err != nil {
		return "", err
	}
	var value strings.Builder
	value.Grow(int(size))
	for _, piece := range pieces {
		value.WriteString(piece.text)
	}
	return value.String(), nil
}

// unbound refuses an unset variable or positional parameter under set -u.
func (r *expansionRun) unbound(part commandsurface.ParameterPart) error {
	scope, ok := r.scope.(interface{ Nounset() bool })
	if !ok || !scope.Nounset() || part.Length {
		return nil
	}
	set := true
	switch {
	case part.Name == "PIPESTATUS":
		set = true
	case strings.HasPrefix(part.Name, "PIPESTATUS[") && strings.HasSuffix(part.Name, "]"):
		index, err := strconv.Atoi(part.Name[len("PIPESTATUS[") : len(part.Name)-1])
		set = err == nil && index >= 0 && index < len(scopePipeStatuses(r.scope))
	case VariableName(part.Name):
		_, set = r.scope.Variable(part.Name)
	case positionalName(part.Name):
		index, err := strconv.Atoi(part.Name)
		set = err == nil && index <= len(r.scope.Positional())
	}
	if set {
		return nil
	}
	return invalidValue(part.Span, part.Name+": unbound variable")
}
