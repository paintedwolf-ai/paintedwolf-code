package command

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	lycexec "github.com/lycaon/lycaon/internal/exec"
	"sort"
	"strings"
	"sync"

	"github.com/lycaon/lycaon/internal/commandprogram"
	"github.com/lycaon/lycaon/internal/invocation"
	"github.com/lycaon/lycaon/internal/toolcontract"
	"github.com/lycaon/lycaon/internal/toolexecution"
	"github.com/lycaon/lycaon/internal/tooloutput"
	"github.com/lycaon/lycaon/internal/toolrejection"
	"github.com/lycaon/lycaon/internal/tools"
	"github.com/lycaon/lycaon/internal/tools/projectpaths"
)

// NativeOperation binds one catalog command to its existing typed owner under
// the catalog operation identity Tool. Prepare binds argv from the current
// directory frame and returns the reviewed form; it runs nothing.
type NativeOperation struct {
	Tool string
	// Prepare binds a reached request in its directory frame; dispatch is that
	// frame, through which owners such as find -exec run their children.
	Prepare func(context.Context, *projectpaths.ProgramPaths, commandprogram.Dispatch, commandprogram.OperationRequest) (NativePrepared, error)
}

// NativePrepared is one bound owner invocation: its review record and the
// single owner call admission reaches.
type NativePrepared struct {
	// Mode is the catalog effect mode the bound form selects; empty uses the
	// evaluator's single declared mode.
	Mode   string
	Review commandprogram.ArgvReview
	Invoke func(context.Context, tools.ToolContext, commandprogram.Streams) (toolcontract.ProducerResult, error)
}

// ProgramOwners is one program's host owner set. Publication and Input serve
// redirections; AdmitLaunch runs before every external launch so pending
// publications land first.
type ProgramOwners struct {
	Natives     map[string]NativeOperation
	Publication commandprogram.OutputPublication
	Input       commandprogram.InputRedirection
	// Redirections binds redirect owners to a directory frame after cd.
	Redirections func(*projectpaths.ProgramPaths) (commandprogram.InputRedirection, commandprogram.OutputPublication)
	AdmitLaunch  func(context.Context, tools.ToolContext) error
	// Builtins are in-process utility owners bound to this program's ports;
	// release runs once the program and its jobs have settled.
	Builtins func(BuiltinPorts) (builtins map[string]commandprogram.OperationDispatcher, release func(), err error)
}

// BuiltinPorts are one program's resources for in-process utility owners.
type BuiltinPorts struct {
	ToolContext tools.ToolContext
	Memory      *toolcontract.ProgramMemory
	ProgramID   string
	Nested      commandprogram.NestedCommand
	Publication commandprogram.OutputPublication
	Admission   tooloutput.SpillAdmission
}

// ProgramDispatcher routes each reached simple command: catalog natives bound
// to typed owners go through Executor.InvokeNative, host builtins run in
// process, and every other name launches confined. Each child gets its own
// receipt before admission and keeps its owner's settlement.
type ProgramDispatcher struct {
	run      *programRun
	builtins map[string]commandprogram.OperationDispatcher
	natives  map[string]NativeOperation
	binder   commandprogram.ArgvBinder
	// redirections binds redirect owners to this frame's directory.
	redirections func(*projectpaths.ProgramPaths) (commandprogram.InputRedirection, commandprogram.OutputPublication)
	// admitLaunch is the program's publication barrier for external launches.
	admitLaunch func(context.Context, tools.ToolContext) error
	// paths is this directory frame; cd moves it and Fork copies it.
	paths  *projectpaths.ProgramPaths
	shared *dispatchShared
}

// dispatchShared is per-program state every forked frame shares.
type dispatchShared struct {
	mu          sync.Mutex
	definitions map[string]toolcontract.OperationDefinition
	rejected    *nativeRejection
	evaluated   map[string]evaluatedChild
}

// evaluatedChild is a recorded execution the evaluator runs itself.
type evaluatedChild struct {
	child        programChild
	ownerInvoked bool
}

// pathCommands run on the frame's own directory state.
var pathCommands = map[string]bool{"cd": true, "basename": true, "dirname": true, "realpath": true, "readlink": true, "native_path": true}

// Fork captures this frame's directory for an isolated frame.
func (d *ProgramDispatcher) Fork(context.Context) (commandprogram.Dispatch, func(), error) {
	fork := *d
	fork.paths = d.paths.Fork()
	return &fork, func() {}, nil
}

// Launch runs a confined external program from this frame's directory.
func (d *ProgramDispatcher) Launch(ctx context.Context, request commandprogram.OperationRequest, streams commandprogram.Streams) (toolcontract.ProducerResult, error) {
	if err := request.Validate(); err != nil {
		return refusedResult("COMMAND_OPERAND_INVALID"), err
	}
	if err := d.run.supervisor.AdmitChild(ctx); err != nil {
		return toolcontract.ProducerResult{Status: 130, Terminal: toolcontract.ProducerInterrupted, StopCode: toolrejection.ToolOwnerInterruptedCode, EffectCertainty: toolcontract.NoEffect}, err
	}
	if d.admitLaunch != nil {
		if err := d.admitLaunch(ctx, d.run.tc); err != nil {
			return ownerFailure(err), err
		}
	}
	return d.invokeExternal(ctx, request, streams)
}

// nativeRejection is the first typed owner refusal, kept for program feedback.
type nativeRejection struct {
	operation toolcontract.BoundOperation
	cause     error
}

func (d *ProgramDispatcher) firstRejection() *nativeRejection {
	d.shared.mu.Lock()
	defer d.shared.mu.Unlock()
	return d.shared.rejected
}

var _ commandprogram.Dispatch = (*ProgramDispatcher)(nil)
var _ commandprogram.NativeGaps = (*ProgramDispatcher)(nil)

// Invoke admits, records, dispatches and settles one child.
func (d *ProgramDispatcher) Invoke(ctx context.Context, request commandprogram.OperationRequest, streams commandprogram.Streams) (toolcontract.ProducerResult, error) {
	if err := request.Validate(); err != nil {
		return refusedResult("COMMAND_OPERAND_INVALID"), err
	}
	if err := d.run.supervisor.AdmitChild(ctx); err != nil {
		return toolcontract.ProducerResult{Status: 130, Terminal: toolcontract.ProducerInterrupted, StopCode: toolrejection.ToolOwnerInterruptedCode, EffectCertainty: toolcontract.NoEffect}, err
	}
	if resolved, ok, err := commandprogram.ResolveCommand(request.Argv); err == nil && ok && resolved.Name != "external" {
		if bound, err := d.binder.Bind(resolved, request.Argv); err == nil && bound.Help {
			return d.invokeBuiltin(ctx, resolved, commandprogram.HelpDispatcher(resolved), request, streams)
		}
	}
	spec, native := d.run.commands[request.Argv[0].Value]
	switch {
	case !native || spec.Name == "external":
		return d.invokeExternal(ctx, request, streams)
	case d.natives[spec.Name].Prepare != nil:
		return d.invokeNative(ctx, spec, d.natives[spec.Name], request, streams)
	case pathCommands[spec.Name]:
		return d.invokeBuiltin(ctx, spec, d.paths, request, streams)
	case d.builtins[spec.Name] != nil:
		// Host builtins read the frame's directory from the invocation scope.
		request.Invocation.Scope.Directory = d.paths.Cwd().DisplayPath
		return d.invokeBuiltin(ctx, spec, d.builtins[spec.Name], request, streams)
	}
	return d.refuseUnimplemented(ctx, spec, request, streams)
}

func (d *ProgramDispatcher) invokeBuiltin(ctx context.Context, spec toolcontract.CommandSpecification, builtin commandprogram.OperationDispatcher, request commandprogram.OperationRequest, streams commandprogram.Streams) (toolcontract.ProducerResult, error) {
	child, err := d.prepare(ctx, spec, request, nil)
	if err != nil {
		return refusedResult("COMMAND_OPERAND_INVALID"), err
	}
	defer child.release()
	if err := d.start(ctx, child); err != nil {
		result := refusedResult("TOOL_OWNER_FAILED")
		return result, errors.Join(err, d.settle(ctx, child, result, false, ""))
	}
	result, runErr := builtin.Invoke(ctx, request, streams)
	if result.Terminal == "" {
		result = ownerFailure(runErr)
	}
	return result, errors.Join(runErr, d.settle(ctx, child, result, true, ""))
}

// invokeExternal admits one confined launch as its own child through the
// executor's existing approval, detection-pack and policy evaluation, over
// the literal argv and directory it will run with, then launches it once.
func (d *ProgramDispatcher) invokeExternal(ctx context.Context, request commandprogram.OperationRequest, streams commandprogram.Streams) (toolcontract.ProducerResult, error) {
	spec := d.run.commands["external"]
	argv := externalArgv(request.Argv.Values())
	if len(argv) == 0 {
		return refusedResult("COMMAND_OPERAND_INVALID"), toolrejection.RejectInvalidArguments("COMMAND_OPERAND_INVALID", map[string]any{"command": "external", "reason": "requires a program"})
	}
	definition, err := d.run.externalDefinition()
	if err != nil {
		return refusedResult("TOOL_OWNER_FAILED"), err
	}
	bound, err := toolcontract.BindOperation(definition, request.Invocation, spec.Name, spec.Effects[0].Mode, nil)
	if err != nil {
		return refusedResult("COMMAND_NATIVE_FORM_UNSUPPORTED"), err
	}
	cwd, display := d.paths.Cwd().Abs, d.paths.Cwd().DisplayPath
	line := (lycexec.Stage{Name: argv[0], Args: argv[1:]}).CommandLine()
	child, err := d.prepareDefinition(ctx, definition, request, map[string]any{"command": line, "cwd": display})
	if err != nil {
		return refusedResult("COMMAND_OPERAND_INVALID"), err
	}
	defer child.release()
	if d.run.externalHidden && request.Argv[0].Value == "external" {
		writeHostRefusal(streams, "external", "", "COMMAND_EXTERNAL_HIDDEN")
		reject := toolrejection.RejectInvalidArguments("COMMAND_EXTERNAL_HIDDEN", map[string]any{"command": "external", "program": argv[0], "source_start_byte": request.Invocation.Span.StartByte, "source_end_byte": request.Invocation.Span.EndByte, "source_line": request.Invocation.Span.Line, "source_column": request.Invocation.Span.Column})
		d.shared.mu.Lock()
		if d.shared.rejected == nil {
			d.shared.rejected = &nativeRejection{operation: bound, cause: reject}
		}
		d.shared.mu.Unlock()
		result := refusedResult("COMMAND_EXTERNAL_HIDDEN")
		return result, errors.Join(reject, d.settle(ctx, child, result, false, ""))
	}
	if reject := desktopCaptureRefusal(argv[0], line); reject != nil {
		d.shared.mu.Lock()
		if d.shared.rejected == nil {
			d.shared.rejected = &nativeRejection{operation: bound, cause: reject}
		}
		d.shared.mu.Unlock()
		result := refusedResult("DESKTOP_CAPTURE_DISALLOWED")
		return result, errors.Join(reject, d.settle(ctx, child, result, false, ""))
	}
	effect := toolexecution.NativeEffect{
		Operation: bound, Streams: streams, Inspection: d.run.capture,
		// The existing command review, detection and policy read these fields.
		Arguments: d.run.launchArguments(line, display),
		Stage:     toolcontract.ExecutionStage{ExecutionID: request.Invocation.Identity.ExecutionID, OperationID: definition.Facts().Identity, Argv: argv, CommandLine: line, PipelineCommandLine: line},
		PrepareReviewed: func(context.Context, map[string]any, tools.ToolContext) (toolexecution.PreparedNativeEffect, error) {
			return toolexecution.PreparedNativeEffect{Operation: bound, InvokeOwner: func(ctx context.Context, admitted tools.ToolContext, streams commandprogram.Streams) (toolcontract.ProducerResult, error) {
				return ExternalStages{ToolContext: admitted, Dir: cwd}.Launch(ctx, request, streams)
			}}, nil
		},
	}
	marker := invocation.ProgramAdmissionRecorder{Recorder: d.run.recorder, Inputs: d.run.capture}
	launched, runErr := d.run.executor.InvokeNative(ctx, d.run.frame, d.run.surface, d.run.memory, d.run.progress, marker, effect, d.run.tc)
	if toolrejection.AsToolReject(runErr) != nil {
		d.shared.mu.Lock()
		if d.shared.rejected == nil {
			d.shared.rejected = &nativeRejection{operation: bound, cause: runErr}
		}
		d.shared.mu.Unlock()
	}
	return launched.Producer, errors.Join(runErr, d.settle(ctx, child, launched.Producer, launched.OwnerInvoked, launched.HostStopCode))
}

// refuseUnimplemented runs a catalogued name with no host owner here as its
// installed program, recorded as a native gap; without one it refuses.
func (d *ProgramDispatcher) refuseUnimplemented(ctx context.Context, spec toolcontract.CommandSpecification, request commandprogram.OperationRequest, streams commandprogram.Streams) (toolcontract.ProducerResult, error) {
	if d.GapLaunchable(request) {
		request.Gap = &toolcontract.NativeGap{Command: spec.Name, Code: "COMMAND_NATIVE_FORM_UNSUPPORTED"}
		return d.invokeExternal(ctx, request, streams)
	}
	writeHostRefusal(streams, spec.Name, "", "COMMAND_NATIVE_FORM_UNSUPPORTED")
	cause := toolrejection.RejectInvalidArguments("COMMAND_NATIVE_FORM_UNSUPPORTED", map[string]any{"command": spec.Name, "source_start_byte": request.Invocation.Span.StartByte, "source_end_byte": request.Invocation.Span.EndByte, "source_line": request.Invocation.Span.Line, "source_column": request.Invocation.Span.Column})
	child, err := d.prepare(ctx, spec, request, nil)
	if err != nil {
		return refusedResult("COMMAND_NATIVE_FORM_UNSUPPORTED"), errors.Join(cause, err)
	}
	defer child.release()
	result := refusedResult("COMMAND_NATIVE_FORM_UNSUPPORTED")
	return result, errors.Join(cause, d.settle(ctx, child, result, false, ""))
}

func (d *ProgramDispatcher) invokeNative(ctx context.Context, spec toolcontract.CommandSpecification, operation NativeOperation, request commandprogram.OperationRequest, streams commandprogram.Streams) (toolcontract.ProducerResult, error) {
	definition, ok := d.run.registry.Definition(operation.Tool)
	if !ok {
		return refusedResult("COMMAND_NATIVE_FORM_UNSUPPORTED"), fmt.Errorf("native operation %s is not registered", operation.Tool)
	}
	prepared, err := operation.Prepare(ctx, d.paths, d, request)
	if err != nil {
		if gap := unsupportedForm(spec, err); gap != nil && d.GapLaunchable(request) {
			request.Gap = gap
			return d.invokeExternal(ctx, request, streams)
		}
		return d.refuseArgv(ctx, spec, request, err, streams)
	}
	mode := prepared.Mode
	if mode == "" {
		mode = request.EffectMode
	}
	if mode == "" && len(spec.Effects) > 0 {
		mode = spec.Effects[0].Mode
	}
	bound, err := toolcontract.BindOperation(definition.Operation, request.Invocation, spec.Name, mode, nil)
	if err != nil {
		return refusedResult("COMMAND_NATIVE_FORM_UNSUPPORTED"), err
	}
	review := prepared.Review
	effect := toolexecution.NativeEffect{
		Operation: bound, Streams: streams, Argv: &review, Inspection: d.run.capture,
		Stage: toolcontract.ExecutionStage{ExecutionID: request.Invocation.Identity.ExecutionID, OperationID: definition.Operation.Facts().Identity, Argv: request.Argv.Values()},
		PrepareReviewed: func(context.Context, map[string]any, tools.ToolContext) (toolexecution.PreparedNativeEffect, error) {
			return toolexecution.PreparedNativeEffect{Operation: bound, InvokeOwner: prepared.Invoke}, nil
		},
	}
	child, err := d.prepareDefinition(ctx, definition.Operation, request, review.Arguments())
	if err != nil {
		return refusedResult("COMMAND_OPERAND_INVALID"), err
	}
	defer child.release()
	marker := invocation.ProgramAdmissionRecorder{Recorder: d.run.recorder, Inputs: d.run.capture}
	native, runErr := d.run.executor.InvokeNative(ctx, d.run.frame, d.run.surface, d.run.memory, d.run.progress, marker, effect, d.run.tc)
	if toolrejection.AsToolReject(runErr) != nil {
		d.shared.mu.Lock()
		if d.shared.rejected == nil {
			d.shared.rejected = &nativeRejection{operation: bound, cause: runErr}
		}
		d.shared.mu.Unlock()
	}
	return native.Producer, errors.Join(runErr, d.settle(ctx, child, native.Producer, native.OwnerInvoked, native.HostStopCode))
}

func (d *ProgramDispatcher) refuseArgv(ctx context.Context, spec toolcontract.CommandSpecification, request commandprogram.OperationRequest, cause error, streams commandprogram.Streams) (toolcontract.ProducerResult, error) {
	rejection := argvRejection(spec, cause)
	reject := toolrejection.AsToolReject(rejection)
	word, _ := reject.Data["word"].(string)
	writeHostRefusal(streams, spec.Name, word, reject.Code)
	result := refusedResult(reject.Code)
	child, err := d.prepare(ctx, spec, request, nil)
	if err != nil {
		return result, errors.Join(rejection, err)
	}
	defer child.release()
	return result, errors.Join(rejection, d.settle(ctx, child, result, false, ""))
}

type programChild struct {
	id        string
	reference string
	bound     toolcontract.BoundOperation
	review    toolcontract.ReviewedOperationInputs
	release   func()
}

// prepare records a child of a catalog command that has no registered tool.
func (d *ProgramDispatcher) prepare(ctx context.Context, spec toolcontract.CommandSpecification, request commandprogram.OperationRequest, inputs map[string]any) (programChild, error) {
	definition, err := d.catalogDefinition(spec)
	if err != nil {
		return programChild{}, err
	}
	if inputs == nil {
		inputs = map[string]any{"operation_id": spec.Name, "argv": request.Argv.Values()}
	}
	child, err := d.prepareDefinition(ctx, definition, request, inputs)
	if err != nil {
		return programChild{}, err
	}
	if len(spec.Effects) > 0 {
		child.bound, err = bindCatalogOperation(definition, request.Invocation, spec)
	}
	if err != nil {
		child.release()
		return programChild{}, err
	}
	return child, nil
}

// prepareDefinition retains the expanded inputs and the dynamic child row
// together, before the child is admitted or reaches any owner.
func (d *ProgramDispatcher) prepareDefinition(ctx context.Context, definition toolcontract.OperationDefinition, request commandprogram.OperationRequest, inputs map[string]any) (programChild, error) {
	display, err := request.Statement.DisplayText([]byte(d.run.source))
	if err != nil {
		return programChild{}, err
	}
	return d.prepareInvocation(ctx, definition, request.Invocation, display, inputs, request.Gap)
}

func (d *ProgramDispatcher) prepareInvocation(ctx context.Context, definition toolcontract.OperationDefinition, invocation toolcontract.OperationInvocation, display string, inputs map[string]any, gap *toolcontract.NativeGap) (programChild, error) {
	review, release, err := toolcontract.NewReviewedOperationInputs(ctx, d.run.memory, inputs)
	if err != nil {
		return programChild{}, err
	}
	reference, err := d.run.capture.PrepareChild(ctx, toolcontract.ProgramChildStart{ParentReceiptID: d.run.parentReceipt, Invocation: invocation, Definition: definition, ExpandedInputs: review, Canonical: display, NativeGap: gap})
	if err != nil {
		release()
		return programChild{}, err
	}
	return programChild{id: invocation.Identity.ExecutionID, reference: reference, review: review, release: release}, nil
}

// start commits the durable owner-start marker for a host-run child.
func (d *ProgramDispatcher) start(ctx context.Context, child programChild) error {
	marker := invocation.ProgramAdmissionRecorder{Recorder: d.run.recorder, Inputs: d.run.capture}
	return marker.BeforeNativeOwner(ctx, child.bound, d.run.tc.Identity.ToolCallID, child.review)
}

func (d *ProgramDispatcher) settle(ctx context.Context, child programChild, result toolcontract.ProducerResult, ownerInvoked bool, hostStop string) error {
	publishable := result.PublicationEligible && hostStop == ""
	return d.run.recorder.SettleChild(context.WithoutCancel(ctx), child.id, toolcontract.ProgramChildSettlement{Result: result, HostStopCode: hostStop, OutputPublishable: publishable, OwnerInvoked: ownerInvoked, DetailReference: child.reference})
}

// catalogDefinition compiles one definition per catalog command without a
// registered tool, attributed to the command's declared owner.
func (d *ProgramDispatcher) catalogDefinition(spec toolcontract.CommandSpecification) (toolcontract.OperationDefinition, error) {
	d.shared.mu.Lock()
	defer d.shared.mu.Unlock()
	if definition, ok := d.shared.definitions[spec.Name]; ok {
		return definition, nil
	}
	contract, ok := toolcontract.Lookup("command")
	if !ok {
		return toolcontract.OperationDefinition{}, fmt.Errorf("command contract missing")
	}
	contract.Owner = spec.Owner
	statuses := map[int]string{}
	for status, meaning := range spec.Statuses {
		var code int
		if _, err := fmt.Sscanf(status, "%d", &code); err == nil {
			statuses[code] = meaning
		}
	}
	if len(statuses) == 0 {
		statuses[0] = "owner terminal outcome"
	}
	definition, err := toolcontract.NewOperationDefinition(toolcontract.OperationFacts{
		Identity: spec.Name, Version: 1, Contract: contract, Arguments: json.RawMessage(`{"type":"object"}`),
		Commands: []toolcontract.CommandSpecification{spec}, Resources: d.run.catalog.Language.Limits,
		Streams:      toolcontract.StreamContract{Input: spec.Encoding, Output: spec.Encoding, Error: "typed owner rejection; prose is diagnostic", Statuses: statuses},
		ProgressGate: toolcontract.ProgressNotRequired, RejectionContract: spec.Name,
	})
	if err != nil {
		return toolcontract.OperationDefinition{}, err
	}
	if d.shared.definitions == nil {
		d.shared.definitions = map[string]toolcontract.OperationDefinition{}
	}
	d.shared.definitions[spec.Name] = definition
	return definition, nil
}

func ownerFailure(err error) toolcontract.ProducerResult {
	result := toolcontract.ProducerResult{Status: 1, Terminal: toolcontract.ProducerFailed, EffectCertainty: toolcontract.EffectUnknown}
	if rejected := toolrejection.AsToolReject(err); rejected != nil {
		result.Status, result.Terminal, result.StopCode, result.EffectCertainty = 2, toolcontract.ProducerRefused, rejected.Code, toolcontract.NoEffect
	}
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		result.Terminal = toolcontract.ProducerInterrupted
	}
	return result
}

// commandIndex maps every catalog name and alias to its specification.
func commandIndex(catalog toolcontract.LanguageCatalog) map[string]toolcontract.CommandSpecification {
	index := map[string]toolcontract.CommandSpecification{}
	for _, spec := range catalog.Commands {
		index[spec.Name] = spec
		if strings.Contains(spec.Name, " ") {
			continue
		}
		for _, alias := range spec.Aliases {
			index[alias] = spec
		}
	}
	return index
}

func bindCatalogOperation(definition toolcontract.OperationDefinition, invocation toolcontract.OperationInvocation, spec toolcontract.CommandSpecification) (toolcontract.BoundOperation, error) {
	var child *toolcontract.CommandEffects
	for _, req := range spec.Effects[0].Requires {
		if req == "bound-child-effect-set" {
			readOnly, _ := toolcontract.NewCommandEffects(toolcontract.CommandReadOnly)
			child = &readOnly
			break
		}
	}
	return toolcontract.BindOperation(definition, invocation, spec.Name, spec.Effects[0].Mode, child)
}

var _ commandprogram.NestedCommand = (*ProgramDispatcher)(nil)

// RunNested runs a utility's command operand (env COMMAND) as its own child
// of the parent statement, with exactly the given environment, through the
// same admission and receipts as any reached command.
func (d *ProgramDispatcher) RunNested(ctx context.Context, parent commandprogram.OperationRequest, argv commandprogram.Fields, environment map[string]string, streams commandprogram.Streams) (toolcontract.ProducerResult, error) {
	if len(argv) == 0 {
		return refusedResult("COMMAND_OPERAND_INVALID"), fmt.Errorf("nested command requires a program")
	}
	entries := make([]string, 0, len(environment))
	for name, value := range environment {
		entries = append(entries, name+"="+value)
	}
	sort.Strings(entries)
	child := commandprogram.NextExecution(parent.Invocation, parent.Invocation.Identity.NodeID, parent.Invocation.Span)
	child.Scope = parent.Invocation.Scope
	request := commandprogram.OperationRequest{Invocation: child, OperationID: argv[0].Value, Statement: parent.Statement, Argv: argv, Environment: entries}
	if resolved, ok, err := commandprogram.ResolveCommand(argv); err == nil && ok && resolved.Name != "external" {
		if bound, err := d.binder.Bind(resolved, argv); err == nil && bound.Help {
			request.OperationID = resolved.Name
			return d.Invoke(ctx, request, streams)
		}
	}
	spec, native := d.run.commands[argv[0].Value]
	if !native || spec.Name == "external" {
		request.OperationID = "external"
		return d.Launch(ctx, request, streams)
	}
	request.OperationID = spec.Name
	if _, err := d.binder.Bind(spec, argv); err != nil {
		if gap := unsupportedForm(spec, err); gap != nil && d.GapLaunchable(request) {
			request.Gap = gap
			return d.Launch(ctx, request, streams)
		}
		return d.refuseArgv(ctx, spec, request, err, streams)
	}
	return d.Invoke(ctx, request, streams)
}

var _ commandprogram.ExecutionRecorder = (*ProgramDispatcher)(nil)

// BeginExecution records an execution the evaluator runs itself. A catalog
// builtin (true, test, [[, export) also commits its owner-start marker; a
// structural execution (pipeline, group, function, substitution, job,
// redirection) owns no effect of its own.
func (d *ProgramDispatcher) BeginExecution(ctx context.Context, invocation toolcontract.OperationInvocation, command string) error {
	spec, native := d.run.commands[command]
	definition, err := d.structuralDefinition(command)
	if native {
		definition, err = d.catalogDefinition(spec)
	}
	if err != nil {
		return err
	}
	display := d.run.source
	if span := invocation.Span; span.StartByte >= 0 && span.EndByte <= len(display) && span.StartByte <= span.EndByte {
		display = display[span.StartByte:span.EndByte]
	}
	child, err := d.prepareInvocation(ctx, definition, invocation, display, map[string]any{"operation_id": command}, nil)
	if err != nil {
		return err
	}
	invoked := false
	if native && len(spec.Effects) > 0 {
		if child.bound, err = bindCatalogOperation(definition, invocation, spec); err == nil {
			err = d.start(ctx, child)
		}
		if err != nil {
			child.release()
			return err
		}
		invoked = true
	}
	d.shared.mu.Lock()
	if d.shared.evaluated == nil {
		d.shared.evaluated = map[string]evaluatedChild{}
	}
	d.shared.evaluated[invocation.Identity.ExecutionID] = evaluatedChild{child: child, ownerInvoked: invoked}
	d.shared.mu.Unlock()
	return nil
}

// SettleExecution settles a recorded execution once with the evaluator's record.
func (d *ProgramDispatcher) SettleExecution(ctx context.Context, invocation toolcontract.OperationInvocation, record commandprogram.ChildRecord) error {
	d.shared.mu.Lock()
	entry, ok := d.shared.evaluated[invocation.Identity.ExecutionID]
	delete(d.shared.evaluated, invocation.Identity.ExecutionID)
	d.shared.mu.Unlock()
	if !ok {
		return fmt.Errorf("execution %s was not recorded", invocation.Identity.ExecutionID)
	}
	defer entry.child.release()
	result := record.Result
	if !record.Executed || result.Validate() != nil {
		result = toolcontract.ProducerResult{Status: 1, Terminal: toolcontract.ProducerFailed, EffectCertainty: toolcontract.EffectUnknown}
	}
	if !entry.ownerInvoked && result.EffectCertainty == toolcontract.EffectCompleted {
		// Descendants own their effects; a structural node only aggregates them.
		result.EffectCertainty = toolcontract.EffectUnknown
	}
	return d.settle(ctx, entry.child, result, entry.ownerInvoked, "")
}

// structuralDefinition compiles one definition per evaluator construct.
func (d *ProgramDispatcher) structuralDefinition(construct string) (toolcontract.OperationDefinition, error) {
	d.shared.mu.Lock()
	defer d.shared.mu.Unlock()
	key := "construct " + construct
	if definition, ok := d.shared.definitions[key]; ok {
		return definition, nil
	}
	contract, ok := toolcontract.Lookup("command")
	if !ok {
		return toolcontract.OperationDefinition{}, fmt.Errorf("command contract missing")
	}
	definition, err := toolcontract.NewOperationDefinition(toolcontract.OperationFacts{
		Identity: "program " + construct, Version: 1, Contract: contract, Arguments: json.RawMessage(`{"type":"object"}`),
		Resources: d.run.catalog.Language.Limits, ProgressGate: toolcontract.ProgressNotRequired, RejectionContract: "command",
		Streams: toolcontract.StreamContract{Input: "program streams", Output: "program streams", Error: "typed owner rejection; prose is diagnostic", Statuses: map[int]string{0: "aggregate status of the construct"}},
	})
	if err != nil {
		return toolcontract.OperationDefinition{}, err
	}
	if d.shared.definitions == nil {
		d.shared.definitions = map[string]toolcontract.OperationDefinition{}
	}
	d.shared.definitions[key] = definition
	return definition, nil
}

var _ commandprogram.FrameRedirections = (*ProgramDispatcher)(nil)

// Redirections binds redirect targets from this frame's current directory.
func (d *ProgramDispatcher) Redirections() (commandprogram.InputRedirection, commandprogram.OutputPublication) {
	if d.redirections == nil {
		return nil, nil
	}
	return d.redirections(d.paths)
}

// NativeBindings reports whether a program binds a name to a native owner.
type NativeBindings interface {
	NativeBound(name string) bool
}

// NativeBound reports whether dispatch reaches a native owner for name:
// evaluator built-ins and every catalog command with a wired owner.
func (d *ProgramDispatcher) NativeBound(name string) bool {
	if commandprogram.EvaluatorBuiltin(name) {
		return true
	}
	spec, native := d.run.commands[name]
	if !native || spec.Name == "external" {
		return spec.Name == "external"
	}
	return d.natives[spec.Name].Prepare != nil || pathCommands[spec.Name] || d.builtins[spec.Name] != nil
}

// GapLaunchable reports whether a native name may run as its installed
// program: a request that names one, found by the launch's own lookup.
func (d *ProgramDispatcher) GapLaunchable(request commandprogram.OperationRequest) bool {
	if len(request.Argv) == 0 || request.Argv[0].Value == "external" {
		return false
	}
	return (ExternalStages{ToolContext: d.run.tc, Dir: d.paths.Cwd().Abs}).Installed(request.Argv[0].Value, request.Environment)
}

// unsupportedForm is the native gap a typed unsupported-form refusal names;
// other refusals (operand errors, policy) are not gaps and never fall back.
func unsupportedForm(spec toolcontract.CommandSpecification, cause error) *toolcontract.NativeGap {
	code, form := "", ""
	var argv *commandprogram.ArgvError
	if errors.As(cause, &argv) {
		code, form = argv.Code, argv.Word
	} else if reject := toolrejection.AsToolReject(cause); reject != nil {
		code = reject.Code
		form, _ = reject.Data["word"].(string)
	}
	if code != "COMMAND_OPTION_UNSUPPORTED" && code != "COMMAND_NATIVE_FORM_UNSUPPORTED" {
		return nil
	}
	return &toolcontract.NativeGap{Command: spec.Name, Code: code, Form: form}
}

// writeHostRefusal adds the host's refusal to the stopped statement's stderr
// in shell voice beside the structured refusal.
func writeHostRefusal(streams commandprogram.Streams, command, form, code string) {
	if streams.Error == nil {
		return
	}
	subject := command
	if form != "" {
		subject += ": " + form
	}
	_, _ = fmt.Fprintf(streams.Error, "%s: not supported here [%s]\n", subject, code)
}
