// Package toolreach reports what a reader can call on a frozen model request.
package toolreach

import (
	"context"
	"fmt"
	"regexp"
	"slices"
	"sort"
	"strconv"
	"strings"

	"github.com/lycaon/lycaon/internal/toolschema"
	"github.com/lycaon/lycaon/internal/toolsurface"
)

type requestContextKey struct{}

// WithRequest attaches the frozen model request to ctx.
func WithRequest(ctx context.Context, request Request) context.Context {
	if ctx == nil {
		ctx = context.Background()
	}
	return context.WithValue(ctx, requestContextKey{}, request)
}

// FromContext reads the frozen model request from ctx; an absent value reaches nothing.
func FromContext(ctx context.Context) Request {
	if ctx == nil {
		return Request{}
	}
	request, _ := ctx.Value(requestContextKey{}).(Request)
	return request
}

// LoaderTool loads a deferred schema onto the next model request.
const LoaderTool = "request_tools"

// ProgramTool runs native programs.
const ProgramTool = "command"

// Status classifies how a tool can enter the reader's next call.
type Status uint8

const (
	Absent Status = iota
	// Callable means the tool's schema is offered on this request.
	Callable
	// Loadable means the plan defers the tool and the loader is offered on this request.
	Loadable
)

// Request is one frozen model request; the zero value reaches nothing.
type Request struct {
	offered      []string
	plan         toolsurface.Plan
	programs     Programs
	programModes ProgramModes
	schemas      map[string]map[string]any
	known        bool
}

// Programs reports whether a request's profile and surface admit a native command.
type Programs func(name string) bool

// ProgramModes reports whether a request's profile and surface admit a catalog command mode.
type ProgramModes func(name, mode string) bool

// Freeze captures the tool names offered on a request and its availability plan.
func Freeze(offered []string, plan toolsurface.Plan) Request {
	return Request{offered: slices.Clone(offered), plan: plan, known: true}
}

// Offered freezes a request that defers nothing.
func Offered(names []string) Request { return Freeze(names, toolsurface.Plan{}) }

// WithPrograms attaches a frozen program predicate to the request.
func (r Request) WithPrograms(programs Programs) Request {
	r.programs = programs
	return r
}

// WithProgramTable attaches a frozen program and effect-mode admission table to the request.
func (r Request) WithProgramTable(table *ProgramTable) Request {
	if table == nil {
		r.programs = nil
		r.programModes = nil
		return r
	}
	r.programs = table.Program
	r.programModes = table.Mode
	return r
}

// WithSchemas attaches projected argument schemas for callable or loadable tools.
func (r Request) WithSchemas(schemas map[string]map[string]any) Request {
	r.schemas = schemas
	return r
}

// ToolSchema returns the projected argument schema recorded for tool.
func (r Request) ToolSchema(tool string) (map[string]any, bool) {
	if r.schemas == nil {
		return nil, false
	}
	s, ok := r.schemas[tool]
	return s, ok && s != nil
}

// AcceptsField reports whether tool is reachable and its projected schema admits dottedPath.
func (r Request) AcceptsField(tool, dottedPath string) bool {
	if r.Status(tool) == Absent {
		return false
	}
	schema, ok := r.ToolSchema(tool)
	return ok && toolschema.SchemaAcceptsField(schema, dottedPath)
}

// AcceptsEnumValue reports whether tool's projected schema admits dottedPath and value.
func (r Request) AcceptsEnumValue(tool, dottedPath, value string) bool {
	if r.Status(tool) == Absent {
		return false
	}
	schema, ok := r.ToolSchema(tool)
	return ok && toolschema.SchemaAdmitsStringValue(schema, dottedPath, value)
}

// Program reports whether a native program can enter the reader's next call.
func (r Request) Program(name string) bool {
	return r.Status(ProgramTool) != Absent && r.programs != nil && r.programs(name)
}

// Known reports whether this value describes an actual request.
func (r Request) Known() bool { return r.known }

// OfferedNames returns the frozen schema names.
func (r Request) OfferedNames() []string { return slices.Clone(r.offered) }

// Plan returns the frozen availability plan.
func (r Request) Plan() toolsurface.Plan { return r.plan }

// Status classifies one tool on this request.
func (r Request) Status(tool string) Status {
	if !r.known {
		return Absent
	}
	if slices.Contains(r.offered, tool) {
		return Callable
	}
	if r.plan.Deferred(tool) && slices.Contains(r.offered, LoaderTool) {
		return Loadable
	}
	return Absent
}

// Call is one call offered for submission.
type Call struct {
	Tool string         `json:"tool"`
	Args map[string]any `json:"args,omitempty"`
	// Partial means the reader supplies the remaining arguments.
	Partial bool `json:"partial,omitempty"`
	// Program names the native program a command call runs.
	Program string `json:"program,omitempty"`
	words   []string
}

// ProgramCall builds a command call that runs one native program invocation.
func ProgramCall(words ...string) Call {
	return Call{
		Tool:    ProgramTool,
		Program: words[0],
		Args:    map[string]any{ProgramTool: QuoteProgram(words)},
		words:   slices.Clone(words),
	}
}

var plainWord = regexp.MustCompile(`^[A-Za-z0-9_./:=@%+,-]+$`)

// QuoteProgram renders words as one native program, quoting only where needed.
func QuoteProgram(words []string) string {
	quoted := make([]string, len(words))
	for i, word := range words {
		if plainWord.MatchString(word) {
			quoted[i] = word
			continue
		}
		quoted[i] = "'" + strings.ReplaceAll(word, "'", `'\''`) + "'"
	}
	return strings.Join(quoted, " ")
}

// Text renders the call the way copy shows it.
func (c Call) Text() string {
	parts := flattenArgs("", c.Args)
	if c.Partial && c.Program == "" {
		parts = append(parts, "…")
	}
	return "`" + c.Tool + "(" + strings.Join(parts, ", ") + ")`"
}

func flattenArgs(prefix string, args map[string]any) []string {
	keys := make([]string, 0, len(args))
	for key := range args {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	var out []string
	for _, key := range keys {
		path := key
		if prefix != "" {
			path = prefix + "." + key
		}
		if nested, ok := args[key].(map[string]any); ok {
			out = append(out, flattenArgs(path, nested)...)
			continue
		}
		out = append(out, path+"="+argText(args[key]))
	}
	return out
}

func argText(value any) string {
	if text, ok := value.(string); ok {
		return strconv.Quote(text)
	}
	return fmt.Sprintf("%v", value)
}

// Load builds the call that loads a deferred tool.
func Load(tool string) Call {
	return Call{Tool: LoaderTool, Args: map[string]any{"need": tool}}
}

// Sequence returns the calls that reach call on this request, or false when unreachable.
func (r Request) Sequence(call Call) ([]Call, bool) {
	if call.Program != "" {
		admitted := false
		if len(call.words) > 0 {
			admitted = r.ProgramInvocation(probeWords(call)...)
		} else {
			admitted = r.Program(call.Program)
		}
		if !admitted {
			return nil, false
		}
	}
	_, hasSchema := r.ToolSchema(call.Tool)
	switch r.Status(call.Tool) {
	case Callable:
		if hasSchema && call.Program == "" && !callArgsAdmitted(r, "", call.Tool, call.Args) {
			return nil, false
		}
		return []Call{call}, true
	case Loadable:
		if call.Program != "" {
			return []Call{Load(call.Tool), call}, true
		}
		if hasSchema {
			if !callArgsAdmitted(r, "", call.Tool, call.Args) {
				return nil, false
			}
			return []Call{Load(call.Tool), call}, true
		}
		if len(call.Args) > 0 {
			return []Call{Load(call.Tool)}, true
		}
		return []Call{Load(call.Tool), call}, true
	default:
		return nil, false
	}
}

func callArgsAdmitted(r Request, prefix, tool string, args map[string]any) bool {
	for key, val := range args {
		path := key
		if prefix != "" {
			path = prefix + "." + key
		}
		if nested, ok := val.(map[string]any); ok {
			if !r.AcceptsField(tool, path) {
				return false
			}
			if !callArgsAdmitted(r, path, tool, nested) {
				return false
			}
			continue
		}
		if s, ok := val.(string); ok {
			if !r.AcceptsEnumValue(tool, path, s) {
				return false
			}
		} else if !r.AcceptsField(tool, path) {
			return false
		}
	}
	return true
}
