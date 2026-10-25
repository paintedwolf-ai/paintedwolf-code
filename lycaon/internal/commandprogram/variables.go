package commandprogram

import (
	"context"
	"errors"
	"sort"
	"strings"

	"github.com/lycaon/lycaon/internal/toolcontract"
)

// variableBytes is the retained-state envelope of one entry beyond its text.
const variableBytes = 96

// Variables is one invocation frame's variables, export marks, positional
// parameters and last statuses. It implements ExpansionScope; the evaluator
// owns every mutation. Values are opaque text, so a secret reference stays a
// reference until a declared sink resolves it. Nothing persists past the
// invocation, and IFS is fixed: an environment IFS is inert data. Refusals
// carry no source span; the evaluator locates them at the assignment.
type Variables struct {
	memory     *toolcontract.ProgramMemory
	bounds     catalogBounds
	values     map[string]variable
	program    string
	positional []string
	status     int
	pipeStatus []int
	background string
	hasJob     bool
}

type variable struct {
	value    string
	set      bool
	exported bool
	release  func()
}

var _ ExpansionScope = (*Variables)(nil)

// NewVariables starts a frame from the captured child environment, whose
// entries are exported. Every entry is charged to program_state_bytes.
func NewVariables(ctx context.Context, memory *toolcontract.ProgramMemory, program string, environment, positional []string) (*Variables, error) {
	if memory == nil {
		return nil, errors.New("invocation variables require aggregate state admission")
	}
	bounds, err := languageBounds()
	if err != nil {
		return nil, err
	}
	v := &Variables{memory: memory, bounds: bounds, values: map[string]variable{}, program: program, positional: append([]string(nil), positional...)}
	for _, entry := range environment {
		name, value, ok := strings.Cut(entry, "=")
		if !ok || name == "" {
			continue
		}
		if err := v.store(ctx, name, variable{value: value, set: true, exported: true}); err != nil {
			v.Close()
			return nil, err
		}
	}
	return v, nil
}

// VariableName reports a name assignments and $name expansion accept.
func VariableName(name string) bool {
	if name == "" || !(name[0] == '_' || isLetter(name[0])) {
		return false
	}
	for i := 1; i < len(name); i++ {
		if !(name[i] == '_' || isLetter(name[i]) || (name[i] >= '0' && name[i] <= '9')) {
			return false
		}
	}
	return true
}

func isLetter(c byte) bool { return (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') }

// Assign sets a variable in this frame, keeping its export mark.
func (v *Variables) Assign(ctx context.Context, name, value string) error {
	if err := assignable(v.bounds, name, value); err != nil {
		return err
	}
	return v.store(ctx, name, variable{value: value, set: true, exported: v.values[name].exported})
}

// Export marks a variable for child environments; an unset name stays unset.
func (v *Variables) Export(ctx context.Context, name string) error {
	if err := assignable(v.bounds, name, ""); err != nil {
		return err
	}
	current := v.values[name]
	current.exported = true
	return v.store(ctx, name, current)
}

// Unset removes a variable and its export mark; expanding it is then empty.
func (v *Variables) Unset(name string) {
	if current, ok := v.values[name]; ok {
		current.release()
		delete(v.values, name)
	}
}

// Environment is the exported, set variables sorted by name, with overrides
// (a NAME=value prefix of one dispatch) applied without changing the frame.
func (v *Variables) Environment(overrides map[string]string) ([]string, error) {
	names := make([]string, 0, len(v.values)+len(overrides))
	for name, entry := range v.values {
		if _, overridden := overrides[name]; entry.exported && entry.set && !overridden {
			names = append(names, name)
		}
	}
	for name, value := range overrides {
		if err := assignable(v.bounds, name, value); err != nil {
			return nil, err
		}
		names = append(names, name)
	}
	sort.Strings(names)
	out := make([]string, 0, len(names))
	var size int64
	for _, name := range names {
		value, overridden := overrides[name]
		if !overridden {
			value = v.values[name].value
		}
		if size += int64(len(name) + len(value) + 1); size > v.bounds.maximum["environment_bytes"] {
			return nil, v.bounds.exceeded("environment_bytes", size, toolcontract.SourceSpan{})
		}
		out = append(out, name+"="+value)
	}
	return out, nil
}

// Clone copies the frame for an isolated group or substitution unit.
func (v *Variables) Clone(ctx context.Context) (*Variables, error) {
	clone := &Variables{memory: v.memory, bounds: v.bounds, values: map[string]variable{}, program: v.program, positional: append([]string(nil), v.positional...), status: v.status, pipeStatus: append([]int(nil), v.pipeStatus...), background: v.background, hasJob: v.hasJob}
	for name, entry := range v.values {
		if err := clone.store(ctx, name, entry); err != nil {
			clone.Close()
			return nil, err
		}
	}
	return clone, nil
}

// Close releases the frame's retained state.
func (v *Variables) Close() {
	for name := range v.values {
		v.Unset(name)
	}
}

func (v *Variables) SetPositional(positional []string) {
	v.positional = append([]string(nil), positional...)
}
func (v *Variables) SetStatus(status int) { v.status = status }
func (v *Variables) SetPipeStatus(statuses []int) {
	v.pipeStatus = append([]int(nil), statuses...)
}
func (v *Variables) SetBackground(job string) {
	v.background, v.hasJob = job, true
}

func (v *Variables) Variable(name string) (string, bool) {
	entry := v.values[name]
	return entry.value, entry.set
}
func (v *Variables) ProgramName() string { return v.program }
func (v *Variables) Positional() []string {
	return append([]string(nil), v.positional...)
}
func (v *Variables) LastStatus() int { return v.status }
func (v *Variables) PipeStatus() []int {
	if len(v.pipeStatus) > 0 {
		return append([]int(nil), v.pipeStatus...)
	}
	return []int{v.status}
}
func (v *Variables) LastBackground() (string, bool) {
	return v.background, v.hasJob
}

func assignable(bounds catalogBounds, name, value string) error {
	switch {
	case !VariableName(name):
		return invalidValue(toolcontract.SourceSpan{}, "a variable name")
	case name == "IFS":
		return &ExpansionError{Code: codeSyntaxUnsupported, Expected: "fixed space/tab/newline splitting"}
	case name == "PIPESTATUS":
		return &ExpansionError{Code: codeSyntaxUnsupported, Expected: "read-only PIPESTATUS"}
	case strings.IndexByte(value, 0) >= 0:
		return invalidValue(toolcontract.SourceSpan{}, "variable values without NUL bytes")
	case int64(len(value)) > bounds.maximum["variable_value_bytes"]:
		return bounds.exceeded("variable_value_bytes", int64(len(value)), toolcontract.SourceSpan{})
	}
	return nil
}

// store reserves the new entry before releasing the one it replaces, so old
// and new values are both charged while they coexist.
func (v *Variables) store(ctx context.Context, name string, entry variable) error {
	previous, replacing := v.values[name]
	if count := int64(len(v.values) + 1); !replacing && count > v.bounds.maximum["variable_count"] {
		return v.bounds.exceeded("variable_count", count, toolcontract.SourceSpan{})
	}
	release, err := v.memory.ReserveProgramState(ctx, int64(len(name)+len(entry.value)+variableBytes))
	if err != nil {
		return err
	}
	entry.release = release
	v.values[name] = entry
	if replacing {
		previous.release()
	}
	return nil
}
