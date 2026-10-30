package commandprogram

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"sync"

	"github.com/google/uuid"
	"github.com/lycaon/lycaon/internal/commandsurface"
	"github.com/lycaon/lycaon/internal/toolcontract"
)

// EvaluatorBuiltin reports names the evaluator runs itself, before any
// dispatch: control, status, test, variable and source built-ins.
func EvaluatorBuiltin(name string) bool {
	switch name {
	case "true", "false", ":", "test", "[", "unset", "set", "source", "export", "read":
		return true
	}
	return controlBuiltins[name]
}

// controlBuiltins change evaluator state rather than invoking an owner. They are
// reserved: a function cannot shadow them.
var controlBuiltins = map[string]bool{"exit": true, "return": true, "break": true, "continue": true}

var catalogCommands = sync.OnceValues(func() ([]toolcontract.CommandSpecification, error) {
	catalog, err := toolcontract.LanguageV1()
	return catalog.Commands, err
})

// ResolveCommand binds argv to the longest catalog command whose words prefix it
// ("git status" before "git"). Native names bind first, including unsupported
// option spellings, so a native word never falls back to an installed binary.
func ResolveCommand(argv Fields) (toolcontract.CommandSpecification, bool, error) {
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
		fields, err := f.e.expander.ExpandFields(ctx, f, word)
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
		value, err := f.e.expander.ExpandScalar(ctx, f, item.Value)
		if err != nil {
			return nil, err
		}
		out = append(out, assignment{loc: item.SyntaxLocation, name: item.Name, value: value})
	}
	return out, nil
}

// expanding runs one command's expansion with the statuses of the
// substitutions it reaches retained. A nonzero unit prevents the containing
// command and becomes its status; prevented reports that the command must not
// run. Expansion refusals latch at the word.
func (f *frame) expanding(loc commandsurface.SyntaxLocation, expand func(scope *frame) error) (prevented bool, err error) {
	g := *f
	tracker := &substitutionStatus{}
	g.substs = tracker
	if err := expand(&g); err != nil {
		return true, f.expansionRefusal(loc, err)
	}
	if !tracker.hasFailure {
		return false, nil
	}
	admitted := f.state.control.AdmitSubstitution(tracker.failedID, PipelineOutcome{Status: tracker.failed, Complete: true})
	var failure *SubstitutionFailure
	if errors.As(admitted, &failure) {
		return true, f.builtinStatus(loc, "substitution", failure.Status)
	}
	return true, admitted
}

// expansionRefusal latches a typed expansion or variable refusal at its word.
// A failed substitution unit has already latched its own host stop.
func (f *frame) expansionRefusal(loc commandsurface.SyntaxLocation, err error) error {
	var substitution *SubstitutionError
	if errors.As(err, &substitution) {
		if _, stopped := f.state.latch.Stop(); !stopped {
			_ = f.state.latch.Record(uuid.NewString(), ownerResult(err), true)
		}
		return err
	}
	return f.stateRefusal(loc, err)
}

// simple expands one reached simple command and selects its owner. A failed
// substitution prevents the dispatch and leaves its ordinary status.
func (f *frame) simple(ctx context.Context, s commandsurface.Statement, c commandsurface.SimpleCommand) (signal, error) {
	var fields Fields
	var assigns []assignment
	prevented, err := f.expanding(s.SyntaxLocation, func(g *frame) error {
		var err error
		if fields, err = g.expandWords(ctx, c.Words); err == nil {
			assigns, err = g.expandAssignments(ctx, c.Assignments)
		}
		return err
	})
	if prevented || err != nil {
		return signal{}, err
	}
	if len(fields) == 0 {
		for _, a := range assigns {
			if err := f.state.vars.Assign(ctx, a.name, a.value); err != nil {
				return signal{}, f.expansionRefusal(a.loc, err)
			}
		}
		return signal{}, f.builtinStatus(s.SyntaxLocation, "assignment", 0)
	}
	name := fields[0].Value
	if controlBuiltins[name] {
		return f.controlBuiltin(s, name, fields)
	}
	if fn, ok := f.state.functions[name]; ok {
		return signal{}, f.call(ctx, s, fn, fields, assigns)
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
	case "set":
		return signal{}, f.setOptions(s, fields)
	case "read":
		return signal{}, f.readBuiltin(ctx, s, fields)
	}
	return signal{}, f.dispatch(ctx, s, fields, assigns)
}

func (f *frame) predicates() PredicateRuntime {
	runtime := f.e.ports.Predicates
	runtime.Expansion, runtime.Scope = f.e.expander, f
	if frame, ok := f.state.dispatch.(FramePredicates); ok {
		runtime.Files = frame.PredicateFiles()
	}
	if runtime.Memory == nil && !runtime.LocalBoundsOnly {
		runtime.Memory = f.e.ports.Memory
	}
	if runtime.Patterns == nil {
		runtime.Patterns = NativePredicatePatterns{Memory: runtime.Memory, LocalBoundsOnly: runtime.LocalBoundsOnly}
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
	child, err := f.beginRecorded(ctx, s.SyntaxLocation, name)
	if err != nil {
		return err
	}
	result, err := run(ctx, child.inv)
	if err != nil && result.Validate() != nil {
		result = ownerResult(err)
	}
	//nolint:contextcheck // Settlement uses the captured BeginExecution context, detached only from cancellation to seal earlier effects.
	return f.settle(child, result, err)
}

// dispatch hands one bound native operation, or a confined external program,
// to its owner. Admission and child receipts happen per call inside the owner.
func (f *frame) dispatch(ctx context.Context, s commandsurface.Statement, fields Fields, assigns []assignment) error {
	spec, native, err := ResolveCommand(fields)
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
	var gap *toolcontract.NativeGap
	if native {
		if _, err := f.e.ports.Binder.Bind(spec, fields); err != nil {
			var argv *ArgvError
			if !errors.As(err, &argv) {
				//nolint:contextcheck // Settlement uses the captured BeginExecution context, detached only from cancellation to seal earlier effects.
				child.close()
				return err
			}
			gap = f.nativeGap(spec, argv, fields, assigns)
			if gap == nil {
				f.hostRefusal(spec.Name, argv.Word, argv.Expected, argv.Code)
				//nolint:contextcheck // Settlement uses the captured BeginExecution context, detached only from cancellation to seal earlier effects.
				child.settle(toolcontract.ProducerResult{Status: 2, Terminal: toolcontract.ProducerRefused, StopCode: argv.Code, EffectCertainty: toolcontract.NoEffect}, err)
				refusal := f.refuse(s.SyntaxLocation, argv.Code, argv.Expected, err)
				refusal.Command, refusal.Option, refusal.Form = spec.Name, argv.Word, spec.Argv
				return refusal
			}
		}
	}
	environment, err := f.environment(assigns)
	if err != nil {
		//nolint:contextcheck // Settlement uses the captured BeginExecution context, detached only from cancellation to seal earlier effects.
		child.close()
		return f.expansionRefusal(s.SyntaxLocation, err)
	}
	request := OperationRequest{Invocation: child.inv, OperationID: operation, Statement: s, Argv: fields, Environment: environment, EffectMode: effectMode(spec, native), Gap: gap}
	var result toolcontract.ProducerResult
	if native && spec.Name != "external" && gap == nil {
		result, err = f.state.dispatch.Invoke(ctx, request, f.streams)
	} else {
		if err := f.e.charge(f, s.SyntaxLocation, "concurrent_processes", 1); err != nil {
			//nolint:contextcheck // Settlement uses the captured BeginExecution context, detached only from cancellation to seal earlier effects.
			child.close()
			return err
		}
		result, err = f.state.dispatch.Launch(ctx, request, f.streams)
		f.e.release("concurrent_processes", 1)
	}
	//nolint:contextcheck // Settlement uses the captured BeginExecution context, detached only from cancellation to seal earlier effects.
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
	f.state.pipeStatus = []int{result.Status}
	f.state.vars.SetPipeStatus(f.state.pipeStatus)
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
	args, functions := fields.Values()[1:], false
	if len(args) > 0 && (args[0] == "-v" || args[0] == "-f") {
		functions, args = args[0] == "-f", args[1:]
	}
	for _, name := range args {
		if !VariableName(name) {
			return f.builtinStatus(s.SyntaxLocation, "unset", 2)
		}
	}
	for _, name := range args {
		if functions {
			delete(f.state.functions, name)
		} else {
			f.state.vars.Unset(name)
		}
	}
	return f.builtinStatus(s.SyntaxLocation, "unset", 0)
}

func (f *frame) exportDeclaration(ctx context.Context, s commandsurface.Statement, c commandsurface.EnvironmentDeclaration) error {
	var assigns, names []assignment
	prevented, err := f.expanding(s.SyntaxLocation, func(g *frame) error {
		var err error
		if assigns, err = g.expandAssignments(ctx, c.Assignments); err != nil {
			return err
		}
		for _, word := range c.Words {
			value, err := f.e.expander.ExpandScalar(ctx, g, word)
			if err != nil {
				return err
			}
			if name, val, hasValue := strings.Cut(value, "="); hasValue {
				assigns = append(assigns, assignment{loc: word.SyntaxLocation, name: name, value: val})
			} else {
				names = append(names, assignment{loc: word.SyntaxLocation, name: name})
			}
		}
		return nil
	})
	if prevented || err != nil {
		return err
	}
	all := append(append([]assignment(nil), assigns...), names...)
	for _, a := range all {
		if !VariableName(a.name) {
			return f.builtinStatus(s.SyntaxLocation, "export", 1)
		}
	}
	for _, a := range assigns {
		if err := f.state.vars.Assign(ctx, a.name, a.value); err != nil {
			return f.expansionRefusal(a.loc, err)
		}
	}
	for _, a := range all {
		if err := f.state.vars.Export(ctx, a.name); err != nil {
			return f.expansionRefusal(a.loc, err)
		}
	}
	return f.builtinStatus(s.SyntaxLocation, "export", 0)
}

// call runs a declared function with invocation-local positional arguments.
// The body shares variables, functions and exit control with its caller.
func (f *frame) call(ctx context.Context, s commandsurface.Statement, fn declaredFunction, fields Fields, assigns []assignment) error {
	decl := fn.decl
	if int64(f.functions+1) > f.e.limits["function_depth"] {
		return f.refuseLimit(s.SyntaxLocation, "function_depth", int64(f.functions+1))
	}
	child, err := f.beginRecorded(ctx, s.SyntaxLocation, decl.Name)
	if err != nil {
		return err
	}
	//nolint:contextcheck // Settlement uses the captured BeginExecution context, detached only from cancellation to seal earlier effects.
	defer child.close()
	g := *f
	g.parent, g.positional, g.functions, g.loops, g.returnable = child.inv, fields.Values()[1:], f.functions+1, 0, true
	if fn.sourceUnit != "" {
		g.parent.SourceUnitID = fn.sourceUnit
	}
	if fn.unit != "" {
		g.unit = fn.unit
	}
	if len(assigns) > 0 {
		g.prefix = make(map[string]string, len(f.prefix)+len(assigns))
		for name, value := range f.prefix {
			g.prefix[name] = value
		}
		for _, a := range assigns {
			g.prefix[a.name] = a.value
		}
	}
	_, err = g.statement(ctx, decl.Body)
	result := ordinaryResult(f.state.control.Status())
	if stop, ok := f.state.latch.Stop(); ok {
		result = stop.Result
	}
	//nolint:contextcheck // Settlement uses the captured BeginExecution context, detached only from cancellation to seal earlier effects.
	child.settle(result, err)
	return err
}

// setOptions applies set -e/-u/-o errexit/-o nounset and their + forms;
// -o pipefail is the fixed pipeline semantics. Other operands are usage 2.
func (f *frame) setOptions(s commandsurface.Statement, fields Fields) error {
	options := f.state.options
	args := fields.Values()[1:]
	for i := 0; i < len(args); i++ {
		flags, enable := strings.CutPrefix(args[i], "-")
		if !enable {
			flags, _ = strings.CutPrefix(args[i], "+")
		}
		if flags == "" || flags == args[i] {
			return f.builtinStatus(s.SyntaxLocation, "set", 2)
		}
		for _, flag := range flags {
			name := ""
			switch flag {
			case 'e':
				name = "errexit"
			case 'u':
				name = "nounset"
			case 'o':
				if i+1 >= len(args) {
					return f.builtinStatus(s.SyntaxLocation, "set", 2)
				}
				i++
				name = args[i]
			}
			switch {
			case name == "errexit":
				options.errexit = enable
			case name == "nounset":
				options.nounset = enable
			case name == "pipefail" && enable:
			default:
				return f.builtinStatus(s.SyntaxLocation, "set", 2)
			}
		}
	}
	f.state.options = options
	return f.builtinStatus(s.SyntaxLocation, "set", 0)
}

// nativeGap is the gap an unsupported native form becomes when dispatch can
// run the installed program instead; nil keeps the refusal.
func (f *frame) nativeGap(spec toolcontract.CommandSpecification, argv *ArgvError, fields Fields, assigns []assignment) *toolcontract.NativeGap {
	if argv.Code != argvOptionUnsupported && argv.Code != "COMMAND_NATIVE_FORM_UNSUPPORTED" {
		return nil
	}
	gaps, ok := f.state.dispatch.(NativeGaps)
	if !ok {
		return nil
	}
	environment, err := f.environment(assigns)
	if err != nil {
		return nil
	}
	gap := &toolcontract.NativeGap{Command: spec.Name, Code: argv.Code, Form: argv.Word}
	if !gaps.GapLaunchable(OperationRequest{OperationID: "external", Argv: fields, Environment: environment, Gap: gap}) {
		return nil
	}
	return gap
}

// hostRefusal writes a host refusal to the statement's stderr in shell voice.
func (f *frame) hostRefusal(command, form, expected, code string) {
	if f.streams.Error == nil {
		return
	}
	subject := command
	if form != "" {
		subject += ": " + form
	}
	if expected != "" {
		_, _ = fmt.Fprintf(f.streams.Error, "%s: not supported here (expected %s) [%s]\n", subject, expected, code)
		return
	}
	_, _ = fmt.Fprintf(f.streams.Error, "%s: not supported here [%s]\n", subject, code)
}
