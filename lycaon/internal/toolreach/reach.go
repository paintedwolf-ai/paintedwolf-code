// Package toolreach is the one authority on what a reader can call on a frozen
// model request. Recovery copy, prompts and native replacements all ask it, so
// none of them can name a call the request cannot reach.
package toolreach

import (
	"fmt"
	"regexp"
	"slices"
	"sort"
	"strconv"
	"strings"

	"github.com/lycaon/lycaon/internal/toolsurface"
)

// LoaderTool loads a deferred schema onto the next model request.
const LoaderTool = "request_tools"

// ProgramTool runs native programs.
const ProgramTool = "command"

// Status is how a tool can enter the reader's next call.
type Status uint8

const (
	Absent Status = iota
	// Callable means the tool's schema is on this request.
	Callable
	// Loadable means the plan defers the tool and the loader is on this request.
	Loadable
)

// Request is one frozen model request. The zero value reaches nothing, because a
// missing request is no evidence that any tool is available.
type Request struct {
	offered  []string
	plan     toolsurface.Plan
	programs Programs
	known    bool
}

// Programs answers whether a request's profile and surface can run a native
// program. Path grants and approval are still checked when the program runs.
type Programs func(name string) bool

// Freeze captures the schemas offered on a request and its compiled plan.
func Freeze(offered []string, plan toolsurface.Plan) Request {
	return Request{offered: slices.Clone(offered), plan: plan, known: true}
}

// Offered freezes a request that defers nothing.
func Offered(names []string) Request { return Freeze(names, toolsurface.Plan{}) }

// WithPrograms adds the request's frozen program answer. Without one, no program
// is reachable.
func (r Request) WithPrograms(programs Programs) Request {
	r.programs = programs
	return r
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

// Call is one call a reader can make. It is offered for submission and never
// executes itself.
type Call struct {
	Tool string         `json:"tool"`
	Args map[string]any `json:"args,omitempty"`
	// Partial means the reader supplies the remaining arguments.
	Partial bool `json:"partial,omitempty"`
	// Program names the native program a command call runs.
	Program string `json:"program,omitempty"`
}

// ProgramCall is the exact command call that runs one native program.
func ProgramCall(words ...string) Call {
	return Call{Tool: ProgramTool, Program: words[0], Args: map[string]any{ProgramTool: QuoteProgram(words)}}
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

// Load is the exact call that loads a deferred tool.
func Load(tool string) Call {
	return Call{Tool: LoaderTool, Args: map[string]any{"need": tool}}
}

// Sequence returns the calls that reach call on this request, in order, and
// false when nothing can. A deferred target adds its loader first; loading is
// separate from the target because the schema arrives on the next request.
func (r Request) Sequence(call Call) ([]Call, bool) {
	if call.Program != "" && !r.Program(call.Program) {
		return nil, false
	}
	switch r.Status(call.Tool) {
	case Callable:
		return []Call{call}, true
	case Loadable:
		return []Call{Load(call.Tool), call}, true
	default:
		return nil, false
	}
}
