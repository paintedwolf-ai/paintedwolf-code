package commandprogram

import (
	"context"
	"errors"
	"strings"
	"unicode/utf8"

	"github.com/lycaon/lycaon/internal/commandsurface"
	"github.com/lycaon/lycaon/internal/toolcontract"
)

// EvaluateArithmeticCommand evaluates (( expression )) with variable mutations
// enabled and returns the signed 64-bit result.
func (e *WordExpander) EvaluateArithmeticCommand(ctx context.Context, scope ExpansionScope, cmd commandsurface.ArithmeticCommand) (int64, error) {
	if scope == nil {
		return 0, errors.New("arithmetic command requires a variable scope")
	}
	r := expansionRun{ctx: ctx, expander: e, scope: scope, span: cmd.Span, mode: modeScalar, allowArithmeticMutation: true}
	defer r.close()
	return r.arithmeticValue(ctx, cmd.Expression)
}

// EvaluateArithmeticNode evaluates a non-mutating arithmetic node (such as an
// array subscript in an assignment).
func (e *WordExpander) EvaluateArithmeticNode(ctx context.Context, scope ExpansionScope, node commandsurface.ArithmeticNode, span toolcontract.SourceSpan) (int64, error) {
	if scope == nil {
		return 0, errors.New("arithmetic expression requires a variable scope")
	}
	r := expansionRun{ctx: ctx, expander: e, scope: scope, span: span, mode: modeScalar}
	defer r.close()
	return r.arithmeticValue(ctx, node)
}

func scopeArrayElement(scope ExpansionScope, name string, index int64) (string, bool) {
	if f, ok := scope.(*frame); ok {
		if value, ok := f.prefix[name]; ok {
			if index == 0 {
				return value, true
			}
			return "", false
		}
		return f.state.vars.VariableElement(name, index)
	}
	if indexed, ok := scope.(interface {
		VariableElement(string, int64) (string, bool)
	}); ok {
		return indexed.VariableElement(name, index)
	}
	if index < 0 {
		return "", false
	}
	if name == "PIPESTATUS" {
		statuses := scopePipeStatuses(scope)
		if index >= int64(len(statuses)) {
			return "", false
		}
		return statuses[index], true
	}
	if index == 0 {
		return scope.Variable(name)
	}
	return "", false
}

func scopeArrayElements(scope ExpansionScope, name string) ([]string, bool) {
	if f, ok := scope.(*frame); ok {
		if value, ok := f.prefix[name]; ok {
			return []string{value}, true
		}
		return f.state.vars.VariableElements(name)
	}
	if indexed, ok := scope.(interface {
		VariableElements(string) ([]string, bool)
	}); ok {
		return indexed.VariableElements(name)
	}
	if name == "PIPESTATUS" {
		return scopePipeStatuses(scope), true
	}
	if val, set := scope.Variable(name); set {
		return []string{val}, true
	}
	return nil, false
}

func (r *expansionRun) resolveParameterValue(part commandsurface.ParameterPart) (string, error) {
	if part.Index != nil {
		index, err := r.arithmeticValue(r.ctx, part.Index)
		if err != nil {
			return "", err
		}
		if index < 0 {
			return "", invalidValue(part.Span, "a non-negative array subscript")
		}
		val, _ := scopeArrayElement(r.scope, part.Name, index)
		return val, nil
	}
	return parameterValue(r.scope, part)
}

func (r *expansionRun) emitElements(elems []string, subscript string, quoted bool, slice *commandsurface.ParameterSlice, positional bool, span toolcontract.SourceSpan) error {
	var err error
	if slice != nil {
		elems, err = r.sliceElements(elems, slice, positional, span)
		if err != nil {
			return err
		}
	}
	if quoted && subscript == "@" && r.mode == modeFields {
		for i, value := range elems {
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
	for _, value := range elems {
		joined += len(value) + 1
	}
	if err := r.charge(r.ctx, int64(joined)); err != nil {
		return err
	}
	return r.emit(r.ctx, strings.Join(elems, " "), quoted)
}

func (r *expansionRun) sliceString(value string, slice *commandsurface.ParameterSlice, span toolcontract.SourceSpan) (string, error) {
	var offset int64
	var err error
	if slice.Offset != nil {
		offset, err = r.arithmeticValue(r.ctx, slice.Offset)
		if err != nil {
			return "", err
		}
	}
	// The rune vector and UTF-8 result overlap until the expansion completes.
	if err := r.charge(r.ctx, 4*int64(utf8.RuneCountInString(value))+int64(len(value))); err != nil {
		return "", err
	}
	runes := []rune(value)
	n := int64(len(runes))
	if offset < 0 {
		offset = n + offset
		if offset < 0 {
			return "", nil
		}
	}
	if offset > n {
		offset = n
	}
	if slice.Length == nil {
		return string(runes[offset:]), nil
	}
	length, err := r.arithmeticValue(r.ctx, slice.Length)
	if err != nil {
		return "", err
	}
	if length < 0 {
		end := n + length
		if end < offset {
			return "", invalidValue(span, "non-negative substring length")
		}
		return string(runes[offset:end]), nil
	}
	end := n
	if length < n-offset {
		end = offset + length
	}
	return string(runes[offset:end]), nil
}

func (r *expansionRun) sliceElements(elems []string, slice *commandsurface.ParameterSlice, positional bool, span toolcontract.SourceSpan) ([]string, error) {
	var offset int64
	var err error
	if slice.Offset != nil {
		offset, err = r.arithmeticValue(r.ctx, slice.Offset)
		if err != nil {
			return nil, err
		}
	}
	if positional {
		all := append([]string{r.scope.ProgramName()}, elems...)
		if offset < 0 {
			offset = int64(len(elems)) + offset
			if offset < 0 {
				return nil, nil
			}
			elems = elems[offset:]
		} else if offset >= int64(len(all)) {
			elems = nil
		} else {
			elems = all[offset:]
		}
	} else {
		n := int64(len(elems))
		if offset < 0 {
			offset = n + offset
			if offset < 0 {
				return nil, nil
			}
		}
		if offset > n {
			offset = n
		}
		elems = elems[offset:]
	}
	if slice.Length != nil {
		length, err := r.arithmeticValue(r.ctx, slice.Length)
		if err != nil {
			return nil, err
		}
		if length < 0 {
			return nil, invalidValue(span, "non-negative slice length")
		}
		if length < int64(len(elems)) {
			elems = elems[:length]
		}
	}
	return elems, nil
}

func (r *expansionRun) unboundIndexed(part commandsurface.ParameterPart) (bool, error) {
	if part.Subscript == "@" || part.Subscript == "*" {
		_, set := scopeArrayElements(r.scope, part.Name)
		return set, nil
	}
	index, err := r.arithmeticValue(r.ctx, part.Index)
	if err != nil {
		return false, err
	}
	if index < 0 {
		return false, invalidValue(part.Span, "a non-negative array subscript")
	}
	_, set := scopeArrayElement(r.scope, part.Name, index)
	return set, nil
}

// assignScopeValue assigns a variable, or one indexed array element, in the
// frame an arithmetic command or assigning parameter operator expands in.
func assignScopeValue(ctx context.Context, scope ExpansionScope, name string, index *int64, text string, span toolcontract.SourceSpan) error {
	if f, ok := scope.(*frame); ok {
		if index != nil {
			return f.state.vars.AssignIndex(ctx, name, *index, text, false)
		}
		return f.state.vars.Assign(ctx, name, text)
	}
	if vars, ok := scope.(*Variables); ok {
		if index != nil {
			return vars.AssignIndex(ctx, name, *index, text, false)
		}
		return vars.Assign(ctx, name, text)
	}
	return &ExpansionError{Code: codeSyntaxUnsupported, Span: span, Expected: "a mutable variable scope"}
}
