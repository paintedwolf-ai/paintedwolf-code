package jq

import (
	"context"
	"errors"
	"slices"
	"strings"

	"github.com/itchyny/gojq"
)

// Variable is one named query input ($name).
type Variable struct {
	Name  string
	Value any
}

// Program is one compiled gojq query: the single compilation a structured
// tool call or a command execution evaluates. No environment, module or
// filesystem loader is available to it; input and inputs read only the
// supplied iterator.
type Program struct {
	code   *gojq.Code
	values []any
	query  string
}

// CompileProgram parses and compiles query once. inputs backs jq's input and
// inputs builtins; nil leaves them empty.
func CompileProgram(query string, variables []Variable, inputs gojq.Iter) (*Program, error) {
	q, err := gojq.Parse(query)
	if err != nil {
		return nil, err
	}
	names := make([]string, len(variables))
	values := make([]any, len(variables))
	for i, v := range variables {
		names[i] = "$" + strings.TrimPrefix(v.Name, "$")
		values[i] = v.Value
	}
	options := []gojq.CompilerOption{gojq.WithEnvironLoader(func() []string { return nil })}
	if len(names) > 0 {
		options = append(options, gojq.WithVariables(names))
	}
	if inputs != nil {
		options = append(options, gojq.WithInputIter(inputs))
	}
	code, err := gojq.Compile(q, options...)
	if err != nil {
		return nil, err
	}
	return &Program{code: code, values: values, query: query}, nil
}

// sortedVariables orders a variable map by name.
func sortedVariables(vars map[string]any) []Variable {
	names := make([]string, 0, len(vars))
	for name := range vars {
		names = append(names, name)
	}
	slices.Sort(names)
	out := make([]Variable, len(names))
	for i, name := range names {
		out[i] = Variable{Name: name, Value: vars[name]}
	}
	return out
}

// Halt is a query's explicit halt or halt_error: its exit code and, for
// halt_error, the value it reports.
type Halt struct {
	Code    int
	Value   any
	Message bool
}

// QueryError is a runtime error the query raised for one input.
type QueryError struct{ Err error }

func (e *QueryError) Error() string { return e.Err.Error() }
func (e *QueryError) Unwrap() error { return e.Err }

// Run evaluates one input, passing each result to emit in order. Evaluation
// observes ctx; a result emit refuses ends the run with that error. A halt
// ends the run and is returned; a runtime error is a *QueryError.
func (p *Program) Run(ctx context.Context, input any, emit func(any) error) (*Halt, error) {
	iter := p.code.RunWithContext(ctx, input, p.values...)
	for {
		v, ok := iter.Next()
		if !ok {
			return nil, nil
		}
		if err, isErr := v.(error); isErr {
			if ctxErr := ctx.Err(); ctxErr != nil {
				return nil, ctxErr
			}
			var halt *gojq.HaltError
			if errors.As(err, &halt) {
				return &Halt{Code: halt.ExitCode(), Value: halt.Value(), Message: halt.Value() != nil}, nil
			}
			return nil, &QueryError{Err: err}
		}
		if err := emit(v); err != nil {
			return nil, err
		}
	}
}
