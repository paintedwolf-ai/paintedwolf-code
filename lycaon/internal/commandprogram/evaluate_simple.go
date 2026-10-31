package commandprogram

import (
	"context"
	"errors"
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
	case "true", "false", ":", "test", "[", "unset", "set", "source", "export", "read", "eval", "ulimit":
		return true
	}
	return controlBuiltins[name]
}

// controlBuiltins change evaluator state rather than invoking an owner. They are
// reserved: a function cannot shadow them.
var controlBuiltins = map[string]bool{"exit": true, "return": true, "break": true, "continue": true}

var catalogCommands = sync.OnceValues(func() ([]toolcontract.CommandSpecification, error) {
	catalog, err := toolcontract.ReadOnlyLanguageV1()
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
		if len(names) > 1 {
			b := argvBinding{spec: spec, argv: argv, index: 1, bound: &BoundArgv{}}
			if b.global(names[1:]) != nil {
				continue
			}
		}
		best, words = spec, len(names)
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

// expanding runs one command's expansion with the statuses of the
// substitutions it reaches retained. A nonzero unit prevents the containing
// command and becomes its status; prevented reports that the command must not
// run. Expansion refusals latch at the word.
func (f *frame) expanding(loc commandsurface.SyntaxLocation, expand func(scope *frame) error) (prevented bool, err error) {
	prev := f.substs
	var tracker substitutionStatus
	f.substs = &tracker
	expandErr := expand(f)
	f.substs = prev
	if expandErr != nil {
		return true, f.expansionRefusal(loc, expandErr)
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
	expansion := f
	if scope := f.io.scope; scope != nil && scope.statementID == s.ID {
		copied := *f
		copied.io = scope.base
		expansion = &copied
	}
	prevented, err := expansion.expanding(s.SyntaxLocation, func(g *frame) error {
		var err error
		if fields, err = g.expandWords(ctx, c.Words); err == nil {
			assigns, err = expandAssignmentList(ctx, g, c.Assignments)
		}
		return err
	})
	if prevented || err != nil {
		if scope := f.io.scope; scope != nil && scope.statementID == s.ID {
			scope.prevented = true
		}
		return signal{}, err
	}
	if len(fields) == 0 || EvaluatorBuiltin(fields[0].Value) {
		return (&evaluatorOperation{frame: f, statement: s, fields: fields, assignments: assigns}).invoke(ctx)
	}
	if fn, ok := f.state.functions[fields[0].Value]; ok {
		return signal{}, f.call(ctx, s, fn, fields, assigns)
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
	child := f.activeHost
	if child != nil && child.inv.Identity.NodeID == string(s.ID) && child.inv.SourceUnitID == f.unit {
		f.activeHost = nil
	} else {
		var err error
		child, err = f.begin(s.SyntaxLocation, name)
		if err != nil {
			return err
		}
	}
	result, err := run(ctx, child.inv)
	if err != nil && result.Validate() != nil {
		result = ownerResult(err)
	}
	return f.settle(ctx, child, result, err)
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
	if err := f.e.charge(f, s.SyntaxLocation, "child_executions", 1); err != nil {
		return err
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
				child.close(ctx)
				return err
			}
			gap = f.nativeGap(ctx, spec, argv, fields, assigns)
			if gap == nil {
				f.hostRefusal(spec.Name, argv.Word, argv.Expected, argv.Code)
				child.settle(ctx, toolcontract.ProducerResult{Status: 2, Terminal: toolcontract.ProducerRefused, StopCode: argv.Code, EffectCertainty: toolcontract.NoEffect}, err)
				refusal := f.refuse(s.SyntaxLocation, argv.Code, argv.Expected, err)
				refusal.Command, refusal.Option, refusal.Form = spec.Name, argv.Word, spec.Argv
				return refusal
			}
		}
	}
	environment, releaseEnvironment, err := f.environment(ctx, assigns)
	if err != nil {
		child.close(ctx)
		return f.expansionRefusal(s.SyntaxLocation, err)
	}
	defer releaseEnvironment()
	request := OperationRequest{Wait: f.wait, Invocation: child.inv, OperationID: operation, Statement: s, Argv: fields, Environment: environment, EffectMode: effectMode(spec, native), Gap: gap, StreamPreparation: f.io,
		Process: ChildProcess{Limits: f.state.limits.pinned(), Times: f.times}}
	var result toolcontract.ProducerResult
	if native && spec.Name != "external" && gap == nil {
		result, err = f.state.dispatch.Invoke(ctx, request, f.io.streams)
		if syncErr := f.syncDirectory(ctx, spec.Name == "cd" && err == nil && result.Status == 0); syncErr != nil {
			child.settle(ctx, result, errors.Join(err, syncErr))
			return f.expansionRefusal(s.SyntaxLocation, syncErr)
		}
	} else {
		if err := f.e.charge(f, s.SyntaxLocation, "concurrent_processes", 1); err != nil {
			child.close(ctx)
			return err
		}
		result, err = f.state.dispatch.Launch(ctx, request, f.io.streams)
		f.e.release("concurrent_processes", 1)
	}
	return f.settle(ctx, child, result, err)
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
func (f *frame) settle(ctx context.Context, child *childRun, result toolcontract.ProducerResult, err error) error {
	var streamLimit *StreamLimitError
	if errors.As(err, &streamLimit) {
		loc := commandsurface.SyntaxLocation{ID: commandsurface.NodeID(child.inv.Identity.NodeID), Span: child.inv.Span}
		refusal := f.refuseLimit(loc, streamLimit.Limit, streamLimit.Observed)
		child.settle(ctx, toolcontract.ProducerResult{Status: 2, Terminal: toolcontract.ProducerFailed, StopCode: limitExceeded, EffectCertainty: result.EffectCertainty}, refusal)
		return refusal
	}
	if invalid := result.Validate(); invalid != nil {
		child.settle(ctx, result, errors.Join(err, invalid))
		_ = f.state.latch.Record(child.inv.Identity.ExecutionID, result, true)
		return errors.Join(err, invalid)
	}
	child.settle(ctx, result, err)
	f.publish.mark(result.PublicationEligible)
	f.state.pipeStatus = []int{result.Status}
	f.state.vars.SetPipeStatus(f.state.pipeStatus)
	recordErr := f.state.control.Record(child.inv.Identity.ExecutionID, result, true, false)
	if stop, stopped := f.state.latch.Stop(); stopped {
		if stop.ExecutionID == child.inv.Identity.ExecutionID && err != nil {
			return err
		}
		return errors.Join(err, &AdmissionStoppedError{Stop: stop})
	}
	if recordErr == nil && f.producerPipe != nil && errors.Is(err, ErrConsumerClosed) && f.producerPipe.Completion().Termination == StreamConsumerClosed {
		return ErrConsumerClosed
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
		if assigns, err = expandAssignmentList(ctx, g, c.Assignments); err != nil {
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
	if len(assigns) == 0 && len(names) == 1 && names[0].name == "--help" {
		if spec, native, err := ResolveCommand(Fields{{Value: "export"}, {Value: "--help"}}); err == nil && native {
			return f.inProcess(ctx, s, spec.Name, func(context.Context, toolcontract.OperationInvocation) (toolcontract.ProducerResult, error) {
				return WriteCatalogHelp(spec, f.io.streams.Output)
			})
		}
	}
	all := append(append([]assignment(nil), assigns...), names...)
	for _, a := range all {
		if !VariableName(a.name) {
			return f.builtinStatus(s.SyntaxLocation, "export", 1)
		}
	}
	for _, a := range assigns {
		if err := a.apply(ctx, f.state.vars); err != nil {
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
	defer child.close(ctx)
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
	child.settle(ctx, result, err)
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
func (f *frame) nativeGap(ctx context.Context, spec toolcontract.CommandSpecification, argv *ArgvError, fields Fields, assigns []assignment) *toolcontract.NativeGap {
	if argv.Code != argvOptionUnsupported && argv.Code != "COMMAND_NATIVE_FORM_UNSUPPORTED" {
		return nil
	}
	gaps, ok := f.state.dispatch.(NativeGaps)
	if !ok {
		return nil
	}
	environment, releaseEnvironment, err := f.environment(ctx, assigns)
	if err != nil {
		return nil
	}
	defer releaseEnvironment()
	gap := &toolcontract.NativeGap{Command: spec.Name, Code: argv.Code, Form: argv.Word}
	if !gaps.GapLaunchable(OperationRequest{OperationID: "external", Argv: fields, Environment: environment, Gap: gap, StreamPreparation: f.io}) {
		return nil
	}
	return gap
}

// hostRefusal writes a host refusal to the statement's stderr in shell voice.
func (f *frame) hostRefusal(command, form, expected, code string) {
	if report := f.e.ports.Refusals; report != nil {
		report(f.io.streams.Error, RefusalReport{Operation: command, Operand: form, Code: code, Expected: expected})
	}
}
