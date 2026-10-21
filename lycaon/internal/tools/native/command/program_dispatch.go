package command

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"sync"

	"github.com/lycaon/lycaon/internal/commandprogram"
	"github.com/lycaon/lycaon/internal/invocation"
	"github.com/lycaon/lycaon/internal/toolcontract"
	"github.com/lycaon/lycaon/internal/toolexecution"
	"github.com/lycaon/lycaon/internal/toolrejection"
	"github.com/lycaon/lycaon/internal/tools/projectpaths"
)

// NativeOperation binds one catalog command to its existing typed owner. Bind
// turns the bound argv record into the owner's NativeEffect; it never runs
// the owner itself. Mode is the catalog effect mode the form selects.
type NativeOperation struct {
	Tool string
	Mode func(commandprogram.BoundArgv) string
	Bind func(context.Context, commandprogram.OperationRequest, commandprogram.BoundArgv, toolcontract.BoundOperation) (toolexecution.NativeEffect, error)
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
	// paths is this directory frame; cd moves it and Fork copies it.
	paths  *projectpaths.ProgramPaths
	shared *dispatchShared
}

// dispatchShared is per-program state every forked frame shares.
type dispatchShared struct {
	mu          sync.Mutex
	definitions map[string]toolcontract.OperationDefinition
	rejected    *nativeRejection
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

// Invoke admits, records, dispatches and settles one child.
func (d *ProgramDispatcher) Invoke(ctx context.Context, request commandprogram.OperationRequest, streams commandprogram.Streams) (toolcontract.ProducerResult, error) {
	if err := request.Validate(); err != nil {
		return refusedResult("COMMAND_OPERAND_INVALID"), err
	}
	if err := d.run.supervisor.AdmitChild(ctx); err != nil {
		return toolcontract.ProducerResult{Status: 130, Terminal: toolcontract.ProducerInterrupted, StopCode: toolrejection.ToolOwnerInterruptedCode, EffectCertainty: toolcontract.NoEffect}, err
	}
	spec, native := d.run.commands[request.Argv[0].Value]
	switch {
	case !native || spec.Name == "external":
		return d.invokeExternal(ctx, request, streams)
	case d.natives[spec.Name].Bind != nil:
		return d.invokeNative(ctx, spec, d.natives[spec.Name], request, streams)
	case pathCommands[spec.Name]:
		return d.invokeBuiltin(ctx, spec, d.paths, request, streams)
	case d.builtins[spec.Name] != nil:
		// Host builtins read the frame's directory from the invocation scope.
		request.Invocation.Scope.Directory = d.paths.Cwd().DisplayPath
		return d.invokeBuiltin(ctx, spec, d.builtins[spec.Name], request, streams)
	}
	return d.refuseUnimplemented(ctx, spec, request)
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

func (d *ProgramDispatcher) invokeExternal(ctx context.Context, request commandprogram.OperationRequest, streams commandprogram.Streams) (toolcontract.ProducerResult, error) {
	spec := d.run.commands["external"]
	child, err := d.prepare(ctx, spec, request, map[string]any{"operation_id": "external", "argv": externalArgv(request.Argv.Values())})
	if err != nil {
		return refusedResult("COMMAND_OPERAND_INVALID"), err
	}
	defer child.release()
	if err := d.start(ctx, child); err != nil {
		result := refusedResult("TOOL_OWNER_FAILED")
		return result, errors.Join(err, d.settle(ctx, child, result, false, ""))
	}
	result, runErr := ExternalStages{ToolContext: d.run.tc, Dir: d.paths.Cwd().Abs}.Launch(ctx, request, streams)
	if result.Terminal == "" {
		result = ownerFailure(runErr)
	}
	return result, errors.Join(runErr, d.settle(ctx, child, result, true, ""))
}

// refuseUnimplemented settles a catalogued name with no host owner here. It
// never falls back to an installed binary of the same name.
func (d *ProgramDispatcher) refuseUnimplemented(ctx context.Context, spec toolcontract.CommandSpecification, request commandprogram.OperationRequest) (toolcontract.ProducerResult, error) {
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
	argv, err := d.binder.Bind(spec, request.Argv)
	if err != nil {
		return d.refuseArgv(ctx, spec, request, err)
	}
	definition, ok := d.run.registry.Definition(operation.Tool)
	if !ok {
		return refusedResult("COMMAND_NATIVE_FORM_UNSUPPORTED"), fmt.Errorf("native operation %s is not registered", operation.Tool)
	}
	mode := "default"
	if operation.Mode != nil {
		mode = operation.Mode(argv)
	}
	bound, err := toolcontract.BindOperation(definition.Operation, request.Invocation, spec.Name, mode, nil)
	if err != nil {
		return refusedResult("COMMAND_NATIVE_FORM_UNSUPPORTED"), err
	}
	effect, err := operation.Bind(ctx, request, argv, bound)
	if err != nil {
		return d.refuseArgv(ctx, spec, request, err)
	}
	review := argv.Review(definition.Operation.Facts().Identity)
	effect.Operation, effect.Streams, effect.Argv = bound, streams, &review
	effect.Stage = toolcontract.ExecutionStage{ExecutionID: request.Invocation.Identity.ExecutionID, OperationID: definition.Operation.Facts().Identity, Argv: request.Argv.Values()}
	effect.Inspection = d.run.capture
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

func (d *ProgramDispatcher) refuseArgv(ctx context.Context, spec toolcontract.CommandSpecification, request commandprogram.OperationRequest, cause error) (toolcontract.ProducerResult, error) {
	rejection := argvRejection(spec, cause)
	result := refusedResult(toolrejection.AsToolReject(rejection).Code)
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
		child.bound, err = toolcontract.BindOperation(definition, request.Invocation, spec.Name, spec.Effects[0].Mode, nil)
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
	review, release, err := toolcontract.NewReviewedOperationInputs(ctx, d.run.memory, inputs)
	if err != nil {
		return programChild{}, err
	}
	reference, err := d.run.capture.PrepareChild(ctx, toolcontract.ProgramChildStart{ParentReceiptID: d.run.parentReceipt, Invocation: request.Invocation, Definition: definition, ExpandedInputs: review, Canonical: display})
	if err != nil {
		release()
		return programChild{}, err
	}
	return programChild{id: request.Invocation.Identity.ExecutionID, reference: reference, review: review, release: release}, nil
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
		if strings.Contains(spec.Name, " ") {
			continue
		}
		index[spec.Name] = spec
		for _, alias := range spec.Aliases {
			index[alias] = spec
		}
	}
	return index
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
	spec, native := d.run.commands[argv[0].Value]
	if !native || spec.Name == "external" {
		request.OperationID = "external"
		return d.Launch(ctx, request, streams)
	}
	request.OperationID = spec.Name
	if _, err := d.binder.Bind(spec, argv); err != nil {
		return d.refuseArgv(ctx, spec, request, err)
	}
	return d.Invoke(ctx, request, streams)
}
