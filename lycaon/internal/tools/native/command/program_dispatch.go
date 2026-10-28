package command

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	lycexec "github.com/lycaon/lycaon/internal/exec"
	"slices"
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
	// Tools are further identities Prepare may select per bound form (one
	// umbrella command whose subcommands have their own tools).
	Tools []string
}

// NativePrepared is one bound owner invocation: its review record and the
// single owner call admission reaches.
type NativePrepared struct {
	// Mode is the catalog effect mode the bound form selects; empty uses the
	// evaluator's single declared mode.
	Mode   string
	Review commandprogram.ArgvReview
	Invoke func(context.Context, tools.ToolContext, commandprogram.Streams) (toolcontract.ProducerResult, error)
	// Command is the catalog command the bound form resolved to (git status
	// for git -C DIR status); empty keeps the dispatched command.
	Command string
	// Tool is the operation identity the bound form selected; empty keeps
	// NativeOperation.Tool. With neither, the owner runs as the catalog
	// command's own host execution, as builtins do.
	Tool string
	// Release frees any admitted resources retained by the prepared invocation
	// across start/approval refusals and owner completion.
	Release func()
}

// ProgramOwners is one program's host owner set. Publication and Input serve
// redirections.
type ProgramOwners struct {
	PublishDocument func(context.Context, tools.ToolContext, *toolcontract.ProgramMemory, tools.EditorDocumentPublication) (tools.EditorDocumentApplied, error)
	Natives         map[string]NativeOperation
	Publication     func(tools.CommandOutputAdmission) commandprogram.OutputPublication
	Input           commandprogram.InputRedirection
	Sources         commandprogram.SourceCapture
	// Redirections binds redirect owners to a directory frame after cd.
	Redirections func(*projectpaths.ProgramPaths, tools.CommandOutputAdmission) (commandprogram.InputRedirection, commandprogram.OutputPublication)
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
	redirections func(*projectpaths.ProgramPaths, tools.CommandOutputAdmission) (commandprogram.InputRedirection, commandprogram.OutputPublication)
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
var pathCommands = map[string]bool{"cd": true, "pwd": true, "basename": true, "dirname": true, "realpath": true, "readlink": true, "native_path": true}

// Fork captures this frame's directory for an isolated frame.
func (d *ProgramDispatcher) Fork(context.Context) (commandprogram.Dispatch, func(), error) {
	fork := *d
	fork.paths = d.paths.Fork()
	return &fork, func() {}, nil
}

// Launch runs a confined external program from this frame's directory.
func (d *ProgramDispatcher) Launch(ctx context.Context, request commandprogram.OperationRequest, streams commandprogram.Streams) (result toolcontract.ProducerResult, err error) {
	if err := request.Validate(); err != nil {
		return refusedResult("COMMAND_OPERAND_INVALID"), err
	}
	if err := d.run.supervisor.AdmitChild(ctx); err != nil {
		return toolcontract.ProducerResult{Status: 130, Terminal: toolcontract.ProducerInterrupted, StopCode: toolrejection.ToolOwnerInterruptedCode, EffectCertainty: toolcontract.NoEffect}, err
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
func (d *ProgramDispatcher) Invoke(ctx context.Context, request commandprogram.OperationRequest, streams commandprogram.Streams) (result toolcontract.ProducerResult, err error) {
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
	if request.EffectMode == "" {
		if m, ok := builtin.(interface {
			EffectMode(context.Context, commandprogram.OperationRequest) string
		}); ok {
			request.EffectMode = m.EffectMode(ctx, request)
		}
	}
	return d.InvokeEvaluator(ctx, request, streams, func(ctx context.Context, streams commandprogram.Streams) (toolcontract.ProducerResult, error) {
		return builtin.Invoke(ctx, request, streams)
	})
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
	bound, err := toolcontract.BindOperation(definition, request.Invocation, spec.Name, spec.Effects[0].Mode, d.childEffectSet(spec, spec.Effects[0].Mode, request))
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
		return result, joinSettle(reject, d.settle(ctx, child, result, false, ""))
	}
	if reject := desktopCaptureRefusal(argv[0], line); reject != nil {
		d.shared.mu.Lock()
		if d.shared.rejected == nil {
			d.shared.rejected = &nativeRejection{operation: bound, cause: reject}
		}
		d.shared.mu.Unlock()
		result := refusedResult("DESKTOP_CAPTURE_DISALLOWED")
		return result, joinSettle(reject, d.settle(ctx, child, result, false, ""))
	}
	effect := toolexecution.NativeEffect{
		Operation: bound, Streams: streams, Inspection: d.run.capture,
		// The existing command review, detection and policy read these fields.
		Arguments: d.run.launchArguments(line, display),
		Stage:     toolcontract.ExecutionStage{ExecutionID: request.Invocation.Identity.ExecutionID, OperationID: definition.Facts().Identity, Argv: argv, CommandLine: line, PipelineCommandLine: line},
		PrepareReviewed: func(context.Context, map[string]any, tools.ToolContext) (toolexecution.PreparedNativeEffect, error) {
			return toolexecution.PreparedNativeEffect{Operation: bound, InvokeOwner: func(ctx context.Context, admitted tools.ToolContext, streams commandprogram.Streams) (toolcontract.ProducerResult, error) {
				return d.runProducer(ctx, request, streams, func(ctx context.Context, streams commandprogram.Streams) (toolcontract.ProducerResult, error) {
					return ExternalStages{ToolContext: admitted, Dir: cwd, BaseEnvironment: d.run.baseEnv}.Launch(ctx, request, streams)
				})
			}}, nil
		},
	}
	marker := &invocation.ProgramAdmissionRecorder{Recorder: d.run.recorder, Inputs: d.run.capture}
	launched, runErr := d.run.executor.InvokeNative(ctx, d.run.frame, d.run.surface, d.run.memory, d.run.progress, marker, effect, d.run.tc)
	if toolrejection.AsToolReject(runErr) != nil {
		d.shared.mu.Lock()
		if d.shared.rejected == nil {
			d.shared.rejected = &nativeRejection{operation: bound, cause: runErr}
		}
		d.shared.mu.Unlock()
	}
	return launched.Producer, joinSettle(runErr, d.settle(ctx, child, launched.Producer, launched.OwnerInvoked || marker.OwnerStartRecorded, launched.HostStopCode))
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
		return refusedResult("COMMAND_NATIVE_FORM_UNSUPPORTED"), joinSettle(cause, err)
	}
	defer child.release()
	result := refusedResult("COMMAND_NATIVE_FORM_UNSUPPORTED")
	settleErr := d.settle(ctx, child, result, false, "")
	d.shared.mu.Lock()
	if d.shared.rejected == nil {
		d.shared.rejected = &nativeRejection{operation: child.bound, cause: cause}
	}
	d.shared.mu.Unlock()
	return result, joinSettle(cause, settleErr)
}

func (d *ProgramDispatcher) invokeNative(ctx context.Context, spec toolcontract.CommandSpecification, operation NativeOperation, request commandprogram.OperationRequest, streams commandprogram.Streams) (toolcontract.ProducerResult, error) {
	prepareReq := request
	if (spec.Name == "git" || spec.Owner == "git") && len(d.run.baseEnv) > 0 && len(request.Environment) > 0 {
		var explicit []string
		for _, entry := range request.Environment {
			if !slices.Contains(d.run.baseEnv, entry) {
				explicit = append(explicit, entry)
			}
		}
		prepareReq.Environment = explicit
	}
	prepared, err := operation.Prepare(ctx, d.paths, d, prepareReq)
	if err != nil {
		if gap := unsupportedForm(spec, err); gap != nil && (operation.Tool != "" || len(operation.Tools) > 0) && d.GapLaunchable(request) {
			request.Gap = gap
			return d.invokeExternal(ctx, request, streams)
		}
		return d.refuseArgv(ctx, spec, request, err, streams)
	}
	if prepared.Release != nil {
		defer prepared.Release()
	}
	if prepared.Command != "" {
		resolved, ok := d.run.catalogCommand(prepared.Command)
		if !ok {
			return refusedResult("COMMAND_NATIVE_FORM_UNSUPPORTED"), fmt.Errorf("native command %s is not catalogued", prepared.Command)
		}
		spec = resolved
	}
	tool := prepared.Tool
	if tool == "" {
		tool = operation.Tool
	}
	if tool == "" {
		return d.invokeHosted(ctx, spec, prepared, request, streams)
	}
	definition, ok := d.run.registry.Definition(tool)
	if !ok || !definitionHasCommand(definition.Operation, spec.Name) {
		return d.invokeHosted(ctx, spec, prepared, request, streams)
	}
	operationDef := definition.Operation
	mode := prepared.Mode
	if mode == "" {
		mode = request.EffectMode
	}
	mode = selectEffectMode(spec, mode)
	bound, err := toolcontract.BindOperation(operationDef, request.Invocation, spec.Name, mode, d.childEffectSet(spec, mode, request))
	if err != nil {
		return refusedResult("COMMAND_NATIVE_FORM_UNSUPPORTED"), err
	}
	review := prepared.Review
	review.OperationID = operationDef.Facts().Identity
	tracker := &trackingWriter{w: nonNilWriter(streams.Error)}
	runStreams := streams
	runStreams.Error = tracker
	inputs := d.run.nativeArguments(review.Arguments(), operationDef.Facts().Contract)
	effect := toolexecution.NativeEffect{
		Operation: bound, Streams: runStreams, Arguments: inputs, Argv: &review, Inspection: d.run.capture,
		Stage: toolcontract.ExecutionStage{ExecutionID: request.Invocation.Identity.ExecutionID, OperationID: operationDef.Facts().Identity, Argv: request.Argv.Values()},
		PrepareReviewed: func(context.Context, map[string]any, tools.ToolContext) (toolexecution.PreparedNativeEffect, error) {
			return toolexecution.PreparedNativeEffect{Operation: bound, InvokeOwner: func(ctx context.Context, tc tools.ToolContext, streams commandprogram.Streams) (toolcontract.ProducerResult, error) {
				return d.runProducer(ctx, request, streams, func(ctx context.Context, streams commandprogram.Streams) (toolcontract.ProducerResult, error) {
					tracker.w = nonNilWriter(streams.Error)
					streams.Error = tracker
					return prepared.Invoke(ctx, tc, streams)
				})
			}}, nil
		},
	}
	child, err := d.prepareDefinition(ctx, operationDef, request, inputs)
	if err != nil {
		return refusedResult("COMMAND_OPERAND_INVALID"), err
	}
	defer child.release()
	marker := &invocation.ProgramAdmissionRecorder{Recorder: d.run.recorder, Inputs: d.run.capture}
	native, runErr := d.run.executor.InvokeNative(ctx, d.run.frame, d.run.surface, d.run.memory, d.run.progress, marker, effect, d.run.tc)
	if reject := toolrejection.AsToolReject(runErr); reject != nil && (!native.OwnerInvoked || native.Producer.Terminal == toolcontract.ProducerRefused) {
		if !tracker.wrote {
			writeRejectRefusal(streams, spec.Name, request, reject)
		}
		d.shared.mu.Lock()
		if d.shared.rejected == nil {
			d.shared.rejected = &nativeRejection{operation: bound, cause: runErr}
		}
		d.shared.mu.Unlock()
	}
	return native.Producer, joinSettle(runErr, d.settleWith(ctx, child, toolcontract.ProgramChildSettlement{Result: native.Producer, OwnerInvoked: native.OwnerInvoked || marker.OwnerStartRecorded, HostStopCode: native.HostStopCode, Conflict: revisionConflict(runErr)}))
}

// invokeHosted runs an owner whose bound form has no structured tool as the
// catalog command's own execution: recorded, started and settled like a
// builtin, on this frame.
func (d *ProgramDispatcher) invokeHosted(ctx context.Context, spec toolcontract.CommandSpecification, prepared NativePrepared, request commandprogram.OperationRequest, streams commandprogram.Streams) (toolcontract.ProducerResult, error) {
	request.OperationID = spec.Name
	if prepared.Mode != "" && request.EffectMode == "" {
		request.EffectMode = prepared.Mode
	}
	return d.invokeHost(ctx, request, streams, func(ctx context.Context, tc tools.ToolContext, streams commandprogram.Streams) (toolcontract.ProducerResult, error) {
		return prepared.Invoke(ctx, tc, streams)
	})
}

func (d *ProgramDispatcher) refuseArgv(ctx context.Context, spec toolcontract.CommandSpecification, request commandprogram.OperationRequest, cause error, streams commandprogram.Streams) (toolcontract.ProducerResult, error) {
	cause = tools.CommandResourceRejection(cause)
	if errors.Is(cause, context.Canceled) || errors.Is(cause, context.DeadlineExceeded) {
		result := ownerFailure(cause)
		child, err := d.prepare(ctx, spec, request, nil)
		if err != nil {
			return result, joinSettle(cause, err)
		}
		defer child.release()
		return result, joinSettle(cause, d.settle(ctx, child, result, false, ""))
	}
	rejection := argvRejection(spec, cause)
	reject := toolrejection.AsToolReject(rejection)
	writeRejectRefusal(streams, spec.Name, request, reject)
	result := refusedResult(reject.Code)
	child, err := d.prepare(ctx, spec, request, nil)
	if err != nil {
		return result, joinSettle(rejection, err)
	}
	defer child.release()
	settleErr := d.settle(ctx, child, result, false, "")
	d.shared.mu.Lock()
	if d.shared.rejected == nil {
		d.shared.rejected = &nativeRejection{operation: child.bound, cause: rejection}
	}
	d.shared.mu.Unlock()
	return result, joinSettle(rejection, settleErr)
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
	var bound toolcontract.BoundOperation
	if len(spec.Effects) > 0 {
		mode := selectEffectMode(spec, request.EffectMode)
		bound, err = toolcontract.BindOperation(definition, request.Invocation, spec.Name, mode, d.childEffectSet(spec, mode, request))
		if err != nil {
			return programChild{}, err
		}
	}
	if inputs == nil {
		inputs = map[string]any{"operation_id": spec.Name, "argv": request.Argv.Values()}
	}
	child, err := d.prepareDefinition(ctx, definition, request, inputs)
	if err != nil {
		return programChild{}, err
	}
	child.bound = bound
	return child, nil
}

// prepareDefinition retains the expanded inputs and the dynamic child row
// together, before the child is admitted or reaches any owner.
func (d *ProgramDispatcher) prepareDefinition(ctx context.Context, definition toolcontract.OperationDefinition, request commandprogram.OperationRequest, inputs map[string]any) (programChild, error) {
	display, err := request.Statement.DisplayText([]byte(d.run.sourceText(request.Invocation.SourceUnitID)))
	if err != nil {
		return programChild{}, err
	}
	return d.prepareInvocation(ctx, definition, request.Invocation, display, inputs, request.Gap)
}

func (d *ProgramDispatcher) prepareInvocation(ctx context.Context, definition toolcontract.OperationDefinition, invocation toolcontract.OperationInvocation, display string, inputs map[string]any, gap *toolcontract.NativeGap) (programChild, error) {
	prepareCtx := context.WithoutCancel(ctx)
	review, release, err := toolcontract.NewReviewedOperationInputs(prepareCtx, d.run.memory, inputs)
	if err != nil {
		return programChild{}, err
	}
	reference, err := d.run.capture.PrepareChild(prepareCtx, toolcontract.ProgramChildStart{ParentReceiptID: d.run.parentReceipt, Invocation: invocation, Definition: definition, ExpandedInputs: review, Canonical: display, NativeGap: gap})
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

type admissionMarker struct {
	invocation.ProgramAdmissionRecorder
	started bool
}

func (m *admissionMarker) BeforeNativeOwner(ctx context.Context, operation toolcontract.BoundOperation, modelToolCallID string, inputs toolcontract.ReviewedOperationInputs) error {
	if err := m.ProgramAdmissionRecorder.BeforeNativeOwner(ctx, operation, modelToolCallID, inputs); err != nil {
		return err
	}
	m.started = true
	return nil
}

func (d *ProgramDispatcher) settle(ctx context.Context, child programChild, result toolcontract.ProducerResult, ownerInvoked bool, hostStop string) error {
	return d.settleWith(ctx, child, toolcontract.ProgramChildSettlement{Result: result, HostStopCode: hostStop, OwnerInvoked: ownerInvoked})
}

// settleWith records a child's settlement with its typed receipt facts: the
// job a & statement started and a destination's revision conflict.
func (d *ProgramDispatcher) settleWith(ctx context.Context, child programChild, settlement toolcontract.ProgramChildSettlement) error {
	if settlement.Result.Evidence.Kind == toolcontract.AcceptedUnpublishedWrite {
		settlement.AcceptedRevision = settlement.Result.Evidence.EvidenceRef
	}
	settlement.OutputPublishable = settlement.Result.PublicationEligible && settlement.HostStopCode == ""
	settlement.DetailReference = child.reference
	return d.run.recorder.SettleChild(context.WithoutCancel(ctx), child.id, settlement)
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
	var domain toolcontract.ProducerFailure
	if errors.As(err, &domain) {
		return domain.ProducerResult()
	}
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
// catalogCommand finds any catalog command by its full name, including
// multi-word subcommand forms the dispatch index leaves out.
func (r *programRun) catalogCommand(name string) (toolcontract.CommandSpecification, bool) {
	for _, spec := range r.catalog.Commands {
		if spec.Name == name {
			return spec, true
		}
	}
	return toolcontract.CommandSpecification{}, false
}

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
