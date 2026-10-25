package jq

import (
	"context"
	"errors"
	"io"
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
// tool call or a command execution evaluates. No module or filesystem
// loader is available to it; input and inputs read only the supplied
// iterator, and $ENV and env read only the supplied environment.
type Program struct {
	code   *gojq.Code
	values []any
	query  string
}

// ProgramOptions are what a compiled query may read beyond its input.
type ProgramOptions struct {
	Variables []Variable
	// Inputs backs input and inputs; nil leaves them empty.
	Inputs gojq.Iter
	// Environ is the NAME=value list $ENV presents; nil presents none.
	Environ []string
	// Stderr receives debug and stderr messages; nil discards them.
	Stderr io.Writer
	// Position names the current input for input_filename and
	// input_line_number; nil reports null and 0.
	Position func() (name any, line int)
}

// CompileProgram parses and compiles query once.
func CompileProgram(query string, options ProgramOptions) (*Program, error) {
	q, err := gojq.Parse(query)
	if err != nil {
		return nil, err
	}
	names := make([]string, len(options.Variables))
	values := make([]any, len(options.Variables))
	for i, v := range options.Variables {
		names[i] = "$" + strings.TrimPrefix(v.Name, "$")
		values[i] = gojq.FromGo(v.Value)
	}
	environ := options.Environ
	compilerOptions := append([]gojq.CompilerOption{gojq.WithEnvironLoader(func() []string { return environ })}, queryDiagnostics(options)...)
	if len(names) > 0 {
		compilerOptions = append(compilerOptions, gojq.WithVariables(names))
	}
	if options.Inputs != nil {
		compilerOptions = append(compilerOptions, gojq.WithInputIter(options.Inputs))
	}
	code, err := gojq.Compile(q, compilerOptions...)
	if err != nil {
		return nil, err
	}
	return &Program{code: code, values: values, query: query}, nil
}

// queryDiagnostics are jq's debug, stderr, input_filename and input_line_number.
func queryDiagnostics(options ProgramOptions) []gojq.CompilerOption {
	stderr := options.Stderr
	if stderr == nil {
		stderr = io.Discard
	}
	position := options.Position
	if position == nil {
		position = func() (any, int) { return nil, 0 }
	}
	debug := func(v any) {
		raw, _ := gojq.Marshal([]any{"DEBUG:", v})
		_, _ = stderr.Write(append(raw, '\n'))
	}
	return []gojq.CompilerOption{
		gojq.WithFunction("debug", 0, 1, func(v any, args []any) any {
			if len(args) == 1 {
				debug(args[0])
			} else {
				debug(v)
			}
			return v
		}),
		gojq.WithFunction("stderr", 0, 0, func(v any, _ []any) any {
			if s, ok := v.(string); ok {
				_, _ = io.WriteString(stderr, s)
				return v
			}
			raw, _ := gojq.Marshal(v)
			_, _ = stderr.Write(raw)
			return v
		}),
		gojq.WithFunction("input_filename", 0, 0, func(any, []any) any {
			name, _ := position()
			return name
		}),
		gojq.WithFunction("input_line_number", 0, 0, func(any, []any) any {
			_, line := position()
			return line
		}),
	}
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
// observes ctx; because gojq exposes no VM instruction/step counter hook and
// instead polls ctx.Done() at the start of every bytecode instruction in
// Code.RunWithContext, the caller's context deadline (query_cpu_ms) bounds
// intra-value VM work while emit and input decoding meter host-observable
// value transitions (query_work_steps). A result emit refuses ends the run
// with that error. A halt ends the run and is returned; a runtime error is a
// *QueryError.
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
