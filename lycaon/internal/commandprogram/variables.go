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
// invocation. Refusals carry no source span; the evaluator locates them at the
// assignment.
type Variables struct {
	memory            *toolcontract.ProgramMemory
	bounds            catalogBounds
	values            map[string]variable
	program           string
	positional        []string
	positionalRelease func()
	status            int
	pipeStatus        []int
	background        string
	hasJob            bool
	// ifsUnset records an explicit unset IFS; until then IFS reads as its
	// default without holding program state.
	ifsUnset bool
}

const defaultIFS = " \t\n"

type variable struct {
	value    string
	elements map[int64]string
	ordered  []string
	array    bool
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
	v := &Variables{memory: memory, bounds: bounds, values: map[string]variable{}, program: program}
	if err := v.SetPositional(ctx, positional); err != nil {
		return nil, err
	}
	for _, entry := range environment {
		name, value, ok := strings.Cut(entry, "=")
		// IFS is never taken from the environment.
		if !ok || name == "" || name == "IFS" {
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
	if v.values[name].array {
		return v.AssignIndex(ctx, name, 0, value, false)
	}
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
	if name == "IFS" {
		v.ifsUnset = true
	}
	if current, ok := v.values[name]; ok {
		current.release()
		delete(v.values, name)
	}
}

// Environment is the exported, set variables sorted by name, with overrides
// (a NAME=value prefix of one dispatch) applied without changing the frame.
func (v *Variables) Environment(ctx context.Context, overrides map[string]string) ([]string, func(), error) {
	var count, size int64
	for name, entry := range v.values {
		if _, overridden := overrides[name]; entry.exported && entry.set && !entry.array && !overridden {
			count++
			size += int64(len(name) + len(entry.value) + 1)
		}
	}
	for name, value := range overrides {
		if err := assignable(v.bounds, name, value); err != nil {
			return nil, nil, err
		}
		count++
		size += int64(len(name) + len(value) + 1)
	}
	if size > v.bounds.maximum["environment_bytes"] {
		return nil, nil, v.bounds.exceeded("environment_bytes", size, toolcontract.SourceSpan{})
	}
	// Names, output string headers and NAME=value text coexist with the frame.
	release, err := v.memory.Reserve(ctx, count*32+size)
	if err != nil {
		return nil, nil, err
	}
	names := make([]string, 0, count)
	for name, entry := range v.values {
		if _, overridden := overrides[name]; entry.exported && entry.set && !entry.array && !overridden {
			names = append(names, name)
		}
	}
	for name := range overrides {
		names = append(names, name)
	}
	sort.Strings(names)
	out := make([]string, 0, len(names))
	for _, name := range names {
		value, overridden := overrides[name]
		if !overridden {
			value = v.values[name].value
		}
		out = append(out, name+"="+value)
	}
	return out, release, nil
}

// Clone copies the frame for an isolated group or substitution unit.
func (v *Variables) Clone(ctx context.Context) (*Variables, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	clone := &Variables{memory: v.memory, bounds: v.bounds, values: map[string]variable{}, program: v.program, status: v.status, pipeStatus: v.pipeStatus, background: v.background, hasJob: v.hasJob, ifsUnset: v.ifsUnset}
	if err := clone.SetPositional(ctx, v.positional); err != nil {
		return nil, err
	}
	for name, entry := range v.values {
		if err := clone.replace(ctx, name, variableSize(name, entry), func() variable {
			copied := entry
			if entry.array {
				copied = indexedVariable(copyElements(entry), entry.exported)
			}
			return copied
		}); err != nil {
			clone.Close()
			return nil, err
		}
	}
	return clone, nil
}

// Close releases the frame's retained state.
func (v *Variables) Close() {
	if v.positionalRelease != nil {
		v.positionalRelease()
		v.positionalRelease = nil
	}
	for name := range v.values {
		v.Unset(name)
	}
}

func (v *Variables) SetPositional(ctx context.Context, positional []string) error {
	var size int64
	for _, value := range positional {
		size += int64(len(value) + 16)
	}
	release, err := v.memory.ReserveProgramState(ctx, size)
	if err != nil {
		return err
	}
	values := append([]string(nil), positional...)
	if v.positionalRelease != nil {
		v.positionalRelease()
	}
	v.positional, v.positionalRelease = values, release
	return nil
}
func (v *Variables) SetStatus(status int) { v.status = status }
func (v *Variables) SetPipeStatus(statuses []int) {
	// Callers hand over an immutable status vector already owned by the frame.
	v.pipeStatus = statuses
}
func (v *Variables) SetBackground(job string) {
	v.background, v.hasJob = job, true
}

func (v *Variables) Variable(name string) (string, bool) {
	entry, ok := v.values[name]
	if !ok && name == "IFS" && !v.ifsUnset {
		return defaultIFS, true
	}
	if entry.array {
		val, ok := entry.elements[0]
		return val, ok
	}
	return entry.value, entry.set
}

func (v *Variables) VariableElement(name string, index int64) (string, bool) {
	if index < 0 {
		return "", false
	}
	if name == "PIPESTATUS" {
		statuses := v.PipeStatus()
		if index >= int64(len(statuses)) {
			return "", false
		}
		return itoaStatus(statuses[index]), true
	}
	entry := v.values[name]
	if entry.array {
		val, ok := entry.elements[index]
		return val, ok
	}
	if entry.set && index == 0 {
		return entry.value, true
	}
	return "", false
}

// VariableElements borrows a read-only projection retained with the variable.
func (v *Variables) VariableElements(name string) ([]string, bool) {
	if name == "PIPESTATUS" {
		statuses := v.PipeStatus()
		out := make([]string, len(statuses))
		for i, s := range statuses {
			out[i] = itoaStatus(s)
		}
		return out, true
	}
	entry := v.values[name]
	if !entry.set {
		return nil, false
	}
	if !entry.array {
		return []string{entry.value}, true
	}
	return entry.ordered, true
}

func itoaStatus(n int) string {
	if n == 0 {
		return "0"
	}
	var buf [20]byte
	i := len(buf)
	for n > 0 {
		i--
		buf[i] = byte('0' + n%10)
		n /= 10
	}
	return string(buf[i:])
}
func (v *Variables) ProgramName() string { return v.program }
func (v *Variables) Positional() []string {
	return v.positional
}
func (v *Variables) LastStatus() int { return v.status }
func (v *Variables) PipeStatus() []int {
	if len(v.pipeStatus) > 0 {
		return v.pipeStatus
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
	case name == "PIPESTATUS":
		return &ExpansionError{Code: codeSyntaxUnsupported, Expected: "read-only PIPESTATUS"}
	case strings.IndexByte(value, 0) >= 0:
		return invalidValue(toolcontract.SourceSpan{}, "variable values without NUL bytes")
	case int64(len(value)) > bounds.maximum["variable_value_bytes"]:
		return bounds.exceeded("variable_value_bytes", int64(len(value)), toolcontract.SourceSpan{})
	}
	return nil
}

// store charges already-owned text and metadata before retaining another entry.
func (v *Variables) store(ctx context.Context, name string, entry variable) error {
	return v.replace(ctx, name, variableSize(name, entry), func() variable { return entry })
}

func variableSize(name string, entry variable) int64 {
	bytes := int64(len(name) + len(entry.value) + variableBytes)
	for _, elem := range entry.elements {
		bytes += int64(len(elem) + arrayElementBytes)
	}
	return bytes
}

// replace admits the replacement before constructing it. Both generations stay
// charged until the new value is installed; refusal leaves the old value intact.
func (v *Variables) replace(ctx context.Context, name string, bytes int64, build func() variable) error {
	previous, replacing := v.values[name]
	if count := int64(len(v.values) + 1); !replacing && count > v.bounds.maximum["variable_count"] {
		return v.bounds.exceeded("variable_count", count, toolcontract.SourceSpan{})
	}
	release, err := v.memory.ReserveProgramState(ctx, bytes)
	if err != nil {
		return err
	}
	entry := build()
	entry.release = release
	v.values[name] = entry
	if replacing {
		previous.release()
	}
	return nil
}
