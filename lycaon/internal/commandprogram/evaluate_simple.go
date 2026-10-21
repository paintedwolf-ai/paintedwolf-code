package commandprogram

import (
	"context"
	"errors"
	"strconv"
	"strings"
	"sync"

	"github.com/lycaon/lycaon/internal/commandsurface"
	"github.com/lycaon/lycaon/internal/toolcontract"
)

// controlBuiltins change evaluator state rather than invoking an owner. They are
// reserved: a function cannot shadow them.
var controlBuiltins = map[string]bool{"exit": true, "return": true, "break": true, "continue": true}

var catalogCommands = sync.OnceValues(func() ([]toolcontract.CommandSpecification, error) {
	catalog, err := toolcontract.LanguageV1()
	return catalog.Commands, err
})

// resolveCommand binds argv to the longest catalog command whose words prefix it
// ("git status" before "git"). Native names bind first, including unsupported
// option spellings, so a native word never falls back to an installed binary.
func resolveCommand(argv Fields) (toolcontract.CommandSpecification, bool, error) {
	commands, err := catalogCommands()
	if err != nil {
		return toolcontract.CommandSpecification{}, false, err
	}
	best, words := toolcontract.CommandSpecification{}, 0
	for _, spec := range commands {
		names := strings.Fields(spec.Name)
		if len(names) == 0 || len(names) > len(argv) || len(names) <= words || !commandWord(spec, names[0], argv[0].Value) {
			continue
		}
		match := true
		for i := 1; i < len(names); i++ {
			match = match && argv[i].Value == names[i]
		}
		if match {
			best, words = spec, len(names)
		}
	}
	return best, words > 0, nil
}

func (f *frame) expandWords(ctx context.Context, words []commandsurface.DeferredWord) (Fields, error) {
	var out Fields
	for _, word := range words {
		fields, err := f.e.ports.Expander.ExpandFields(ctx, f, word)
		if err != nil {
			return nil, err
		}
		out = append(out, fields...)
	}
	return out, nil
}

func (f *frame) expandAssignments(ctx context.Context, items []commandsurface.Assignment) ([]assignment, error) {
	out := make([]assignment, 0, len(items))
	for _, item := range items {
		value, err := f.e.ports.Expander.ExpandScalar(ctx, f, item.Value)
		if err != nil {
			return nil, err
		}
		out = append(out, assignment{loc: item.SyntaxLocation, name: item.Name, value: value})
	}
	return out, nil
}

func substitutionFailure(err error) *SubstitutionFailure {
	var failure *SubstitutionFailure
	if errors.As(err, &failure) {
		return failure
	}
	return nil
}

// simple expands one reached simple command and selects its owner. A failed
// substitution prevents the dispatch and leaves its ordinary status.
func (f *frame) simple(ctx context.Context, s commandsurface.Statement, c commandsurface.SimpleCommand) (signal, error) {
	fields, err := f.expandWords(ctx, c.Words)
	var assigns []assignment
	if err == nil {
		assigns, err = f.expandAssignments(ctx, c.Assignments)
	}
	if failure := substitutionFailure(err); failure != nil {
		return signal{}, f.builtinStatus(s.SyntaxLocation, "substitution", failure.Status)
	}
	if err != nil {
		return signal{}, err
	}
	if len(fields) == 0 {
		for _, a := range assigns {
			if err := f.state.vars.set(ctx, a.name, a.value, false); err != nil {
				return signal{}, f.stateRefusal(a.loc, err)
			}
		}
		return signal{}, f.builtinStatus(s.SyntaxLocation, "assignment", 0)
	}
	name := fields[0].Value
	if controlBuiltins[name] {
		return f.controlBuiltin(s, name, fields)
	}
	if decl, ok := f.state.functions[name]; ok {
		return signal{}, f.call(ctx, s, decl, fields, assigns)
	}
	switch name {
	case "source":
		return signal{}, f.source(ctx, s, fields)
	case "true", "false", ":":
		return signal{}, f.inProcess(ctx, s, name, func(context.Context, toolcontract.OperationInvocation) (toolcontract.ProducerResult, error) {
			return BooleanBuiltin(ctx, name, fields.Values()[1:])
		})
	case "test", "[":
		return signal{}, f.inProcess(ctx, s, name, func(ctx context.Context, inv toolcontract.OperationInvocation) (toolcontract.ProducerResult, error) {
			return f.predicates().TestBuiltin(ctx, inv, name, fields.Values()[1:])
		})
	case "unset":
		return signal{}, f.unset(s, fields)
	}
	return signal{}, f.dispatch(ctx, s, fields, assigns)
}

func (f *frame) predicates() PredicateRuntime {
	runtime := f.e.ports.Predicates
	runtime.Expansion, runtime.Scope = f.e.ports.Expander, f
	if runtime.Memory == nil && !runtime.LocalBoundsOnly {
		runtime.Memory = f.e.ports.Memory
	}
	return runtime
}

func (f *frame) testExpression(ctx context.Context, s commandsurface.Statement, c commandsurface.TestExpression) error {
	return f.inProcess(ctx, s, "[[", func(ctx context.Context, inv toolcontract.OperationInvocation) (toolcontract.ProducerResult, error) {
		return f.predicates().TestExpression(ctx, inv, c)
	})
}

// inProcess runs a native host builtin as its own dynamic execution.
func (f *frame) inProcess(ctx context.Context, s commandsurface.Statement, name string, run func(context.Context, toolcontract.OperationInvocation) (toolcontract.ProducerResult, error)) error {
	child, err := f.begin(s.SyntaxLocation, name)
	if err != nil {
		return err
	}
	result, err := run(ctx, child.inv)
	if err != nil && result.Validate() != nil {
		result = ownerResult(err)
	}
	return f.settle(child, result, err)
}

// dispatch hands one bound native operation, or a confined external program,
// to its owner. Admission and child receipts happen per call inside the owner.
func (f *frame) dispatch(ctx context.Context, s commandsurface.Statement, fields Fields, assigns []assignment) error {
	spec, native, err := resolveCommand(fields)
	if err != nil {
		return err
	}
	operation := "external"
	if native {
		operation = spec.Name
	}
	child, err := f.begin(s.SyntaxLocation, operation)
	if err != nil {
		return err
	}
	if native {
		if _, err := f.e.ports.Binder.Bind(spec, fields); err != nil {
			var argv *ArgvError
			if errors.As(err, &argv) {
				child.settle(toolcontract.ProducerResult{Status: 2, Terminal: toolcontract.ProducerRefused, StopCode: argv.Code, EffectCertainty: toolcontract.NoEffect}, err)
				return f.refuse(s.SyntaxLocation, argv.Code, argv.Expected, err)
			}
			child.close()
			return err
		}
	}
	request := OperationRequest{Invocation: child.inv, OperationID: operation, Statement: s, Argv: fields, Environment: f.environment(assigns), EffectMode: effectMode(spec, native)}
	var result toolcontract.ProducerResult
	if native && spec.Name != "external" {
		result, err = f.state.dispatch.Invoke(ctx, request, f.streams)
	} else {
		if err := f.e.charge(f, s.SyntaxLocation, "concurrent_processes", 1); err != nil {
			child.close()
			return err
		}
		result, err = f.state.dispatch.Launch(ctx, request, f.streams)
		f.e.release("concurrent_processes", 1)
	}
	return f.settle(child, result, err)
}

// effectMode names the catalog mode when the command has exactly one; otherwise
// the owner selects it from the bound form.
func effectMode(spec toolcontract.CommandSpecification, native bool) string {
	if native && len(spec.Effects) == 1 {
		return spec.Effects[0].Mode
	}
	return ""
}

// settle records an owner's result. An ordinary failure with a diagnostic stays
// handleable; refusal, interruption, owner stops and invalid results latch and
// return the owner's own error so its rejection code reaches feedback.
func (f *frame) settle(child *childRun, result toolcontract.ProducerResult, err error) error {
	if invalid := result.Validate(); invalid != nil {
		child.settle(result, errors.Join(err, invalid))
		_ = f.state.latch.Record(child.inv.Identity.ExecutionID, result, true)
		return errors.Join(err, invalid)
	}
	child.settle(result, err)
	f.publish.mark(result.PublicationEligible)
	recordErr := f.state.control.Record(child.inv.Identity.ExecutionID, result, true, false)
	if stop, stopped := f.state.latch.Stop(); stopped {
		if stop.ExecutionID == child.inv.Identity.ExecutionID && err != nil {
			return err
		}
		return &AdmissionStoppedError{Stop: stop}
	}
	return recordErr
}

// controlBuiltin implements exit, return, break and continue. A malformed or
// out-of-range operand is an ordinary usage status 2, never modulo wrapping.
func (f *frame) controlBuiltin(s commandsurface.Statement, name string, fields Fields) (signal, error) {
	args := fields.Values()[1:]
	n, ok := f.state.control.Status(), len(args) <= 1
	if ok && len(args) == 1 {
		n, ok = builtinOperand(args[0], name == "exit" || name == "return")
	}
	switch {
	case !ok:
		return signal{}, f.builtinStatus(s.SyntaxLocation, name, 2)
	case name == "exit":
		return signal{}, f.state.control.Exit(n)
	case name == "return":
		if !f.returnable {
			return signal{}, f.builtinStatus(s.SyntaxLocation, name, 2)
		}
		return signal{kind: signalReturn}, f.builtinStatus(s.SyntaxLocation, name, n)
	default:
		levels := 1
		if len(args) == 1 {
			levels = n
		}
		if f.loops == 0 {
			return signal{}, f.builtinStatus(s.SyntaxLocation, name, 2)
		}
		kind := signalBreak
		if name == "continue" {
			kind = signalContinue
		}
		return signal{kind: kind, levels: min(levels, f.loops)}, f.builtinStatus(s.SyntaxLocation, name, 0)
	}
}

// builtinOperand accepts unsigned decimal digits: a status 0..255, or a loop
// count of at least one.
func builtinOperand(value string, status bool) (int, bool) {
	if value == "" || strings.Trim(value, "0123456789") != "" || len(value) > 3 {
		return 0, false
	}
	n, err := strconv.Atoi(value)
	if err != nil {
		return 0, false
	}
	if status {
		return n, n <= 255
	}
	return n, n >= 1
}

func (f *frame) unset(s commandsurface.Statement, fields Fields) error {
	args := fields.Values()[1:]
	functions := false
	if len(args) > 0 && (args[0] == "-v" || args[0] == "-f") {
		functions, args = args[0] == "-f", args[1:]
	}
	for _, name := range args {
		if !variableName(name) {
			return f.builtinStatus(s.SyntaxLocation, "unset", 2)
		}
	}
	for _, name := range args {
		if functions {
			delete(f.state.functions, name)
		} else {
			f.state.vars.unset(name)
		}
	}
	return f.builtinStatus(s.SyntaxLocation, "unset", 0)
}

func (f *frame) exportDeclaration(ctx context.Context, s commandsurface.Statement, c commandsurface.EnvironmentDeclaration) error {
	assigns, err := f.expandAssignments(ctx, c.Assignments)
	if err == nil {
		for _, word := range c.Words {
			var value string
			value, err = f.e.ports.Expander.ExpandScalar(ctx, f, word)
			if err != nil {
				break
			}
			name, val, hasValue := strings.Cut(value, "=")
			if !variableName(name) {
				return f.builtinStatus(s.SyntaxLocation, "export", 1)
			}
			if hasValue {
				assigns = append(assigns, assignment{loc: word.SyntaxLocation, name: name, value: val})
			} else if err := f.state.vars.export(ctx, name); err != nil {
				return f.stateRefusal(word.SyntaxLocation, err)
			}
		}
	}
	if failure := substitutionFailure(err); failure != nil {
		return f.builtinStatus(s.SyntaxLocation, "export", failure.Status)
	}
	if err != nil {
		return err
	}
	for _, a := range assigns {
		if err := f.state.vars.set(ctx, a.name, a.value, true); err != nil {
			return f.stateRefusal(a.loc, err)
		}
	}
	return f.builtinStatus(s.SyntaxLocation, "export", 0)
}

func variableName(name string) bool {
	if name == "" || (name[0] >= '0' && name[0] <= '9') {
		return false
	}
	for i := 0; i < len(name); i++ {
		c := name[i]
		if c != '_' && (c < 'a' || c > 'z') && (c < 'A' || c > 'Z') && (c < '0' || c > '9') {
			return false
		}
	}
	return true
}

// call runs a declared function with invocation-local positional arguments.
// The body shares variables, functions and exit control with its caller.
func (f *frame) call(ctx context.Context, s commandsurface.Statement, decl commandsurface.FunctionDeclaration, fields Fields, assigns []assignment) error {
	if int64(f.functions+1) > f.e.limits["function_depth"] {
		return f.refuseLimit(s.SyntaxLocation, "function_depth", int64(f.functions+1))
	}
	child, err := f.begin(s.SyntaxLocation, decl.Name)
	if err != nil {
		return err
	}
	defer child.close()
	restore, err := f.scopedAssignments(ctx, assigns)
	if err != nil {
		return f.stateRefusal(s.SyntaxLocation, err)
	}
	defer restore()
	g := *f
	g.parent, g.positional, g.functions, g.loops, g.returnable = child.inv, fields.Values()[1:], f.functions+1, 0, true
	_, err = g.statement(ctx, decl.Body)
	result := ordinaryResult(f.state.control.Status())
	if stop, ok := f.state.latch.Stop(); ok {
		result = stop.Result
	}
	child.settle(result, err)
	return err
}

// scopedAssignments applies NAME=value prefixes as exported variables for one
// function call, restoring the previous values afterwards.
func (f *frame) scopedAssignments(ctx context.Context, assigns []assignment) (func(), error) {
	type saved struct {
		name string
		prev *variable
	}
	var undo []saved
	restore := func() {
		for i := len(undo) - 1; i >= 0; i-- {
			f.state.vars.unset(undo[i].name)
			if prev := undo[i].prev; prev != nil {
				_ = f.state.vars.set(context.WithoutCancel(ctx), undo[i].name, prev.value, prev.exported)
			}
		}
	}
	for _, a := range assigns {
		var prev *variable
		if entry, ok := f.state.vars.values[a.name]; ok && entry.set {
			copied := *entry
			prev = &copied
		}
		undo = append(undo, saved{name: a.name, prev: prev})
		if err := f.state.vars.set(ctx, a.name, a.value, true); err != nil {
			restore()
			return nil, err
		}
	}
	return restore, nil
}
