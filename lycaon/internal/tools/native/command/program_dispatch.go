package command

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"

	"github.com/lycaon/lycaon/internal/commandprogram"
	"github.com/lycaon/lycaon/internal/invocation"
	"github.com/lycaon/lycaon/internal/toolcontract"
	"github.com/lycaon/lycaon/internal/toolexecution"
	"github.com/lycaon/lycaon/internal/toolrejection"
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
	external commandprogram.ExternalLauncher
	binder   commandprogram.ArgvBinder

	mu          sync.Mutex
	definitions map[string]toolcontract.OperationDefinition
	rejected    *nativeRejection
}

// nativeRejection is the first typed owner refusal, kept for program feedback.
type nativeRejection struct {
	operation toolcontract.BoundOperation
	cause     error
}

func (d *ProgramDispatcher) firstRejection() *nativeRejection {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.rejected
}

var _ commandprogram.OperationDispatcher = (*ProgramDispatcher)(nil)

// Invoke admits, records, dispatches and settles one child.
func (d *ProgramDispatcher) Invoke(ctx context.Context, request commandprogram.OperationRequest, streams commandprogram.Streams) (toolcontract.ProducerResult, error) {
	if err := request.Validate(); err != nil {
		return refusedResult("COMMAND_OPERAND_INVALID"), err
	}
	if err := d.run.supervisor.AdmitChild(ctx); err != nil {
		return toolcontract.ProducerResult{Status: 130, Terminal: toolcontract.ProducerInterrupted, StopCode: toolrejection.ToolOwnerInterruptedCode, EffectCertainty: toolcontract.NoEffect}, err
	}
	name := request.Argv[0].Value
	spec, native := d.run.commands[name]
	if native {
		if operation, ok := d.natives[spec.Name]; ok {
			return d.invokeNative(ctx, spec, operation, request, streams)
		}
		if builtin, ok := d.builtins[spec.Name]; ok {
			return d.invokeBuiltin(ctx, spec, builtin, request, streams)
		}
		if spec.Name != "external" {
			return d.refuseUnimplemented(ctx, spec, request)
		}
	}
	return d.invokeExternal(ctx, request, streams)
}

func (d *ProgramDispatcher) invokeBuiltin(ctx context.Context, spec toolcontract.CommandSpecification, builtin commandprogram.OperationDispatcher, request commandprogram.OperationRequest, streams commandprogram.Streams) (toolcontract.ProducerResult, error) {
	child, err := d.prepare(ctx, spec, request, nil)
	if err != nil {
		return refusedResult("COMMAND_OPERAND_INVALID"), err
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
	result, runErr := d.external.Launch(ctx, request, streams)
	if result.Terminal == "" {
		result = ownerFailure(runErr)
	}
	return result, errors.Join(runErr, d.settle(ctx, child, result, result.Terminal != toolcontract.ProducerRefused, ""))
}

// refuseUnimplemented settles a catalogued name with no host owner here. It
// never falls back to an installed binary of the same name.
func (d *ProgramDispatcher) refuseUnimplemented(ctx context.Context, spec toolcontract.CommandSpecification, request commandprogram.OperationRequest) (toolcontract.ProducerResult, error) {
	cause := toolrejection.RejectInvalidArguments("COMMAND_NATIVE_FORM_UNSUPPORTED", map[string]any{"command": spec.Name, "source_start_byte": request.Invocation.Span.StartByte, "source_end_byte": request.Invocation.Span.EndByte, "source_line": request.Invocation.Span.Line, "source_column": request.Invocation.Span.Column})
	child, err := d.prepare(ctx, spec, request, nil)
	if err != nil {
		return refusedResult("COMMAND_NATIVE_FORM_UNSUPPORTED"), errors.Join(cause, err)
	}
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
	marker := invocation.ProgramAdmissionRecorder{Recorder: d.run.recorder, Inputs: d.run.capture}
	native, runErr := d.run.executor.InvokeNative(ctx, d.run.frame, d.run.surface, d.run.memory, d.run.progress, marker, effect, d.run.tc)
	if toolrejection.AsToolReject(runErr) != nil {
		d.mu.Lock()
		if d.rejected == nil {
			d.rejected = &nativeRejection{operation: bound, cause: runErr}
		}
		d.mu.Unlock()
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
	return result, errors.Join(rejection, d.settle(ctx, child, result, false, ""))
}

type programChild struct {
	id        string
	reference string
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
	return d.prepareDefinition(ctx, definition, request, inputs)
}

func (d *ProgramDispatcher) prepareDefinition(ctx context.Context, definition toolcontract.OperationDefinition, request commandprogram.OperationRequest, inputs map[string]any) (programChild, error) {
	review, release, err := toolcontract.NewReviewedOperationInputs(ctx, d.run.memory, inputs)
	if err != nil {
		return programChild{}, err
	}
	defer release()
	reference, err := d.run.capture.PrepareChild(ctx, toolcontract.ProgramChildStart{ParentReceiptID: d.run.parentReceipt, Invocation: request.Invocation, Definition: definition, ExpandedInputs: review, Canonical: d.run.source})
	if err != nil {
		return programChild{}, err
	}
	return programChild{id: request.Invocation.Identity.ExecutionID, reference: reference}, nil
}

func (d *ProgramDispatcher) settle(ctx context.Context, child programChild, result toolcontract.ProducerResult, ownerInvoked bool, hostStop string) error {
	publishable := result.PublicationEligible && hostStop == ""
	return d.run.recorder.SettleChild(context.WithoutCancel(ctx), child.id, toolcontract.ProgramChildSettlement{Result: result, HostStopCode: hostStop, OutputPublishable: publishable, OwnerInvoked: ownerInvoked, DetailReference: child.reference})
}

// catalogDefinition compiles one definition per catalog command without a
// registered tool, attributed to the command's declared owner.
func (d *ProgramDispatcher) catalogDefinition(spec toolcontract.CommandSpecification) (toolcontract.OperationDefinition, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	if definition, ok := d.definitions[spec.Name]; ok {
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
	if d.definitions == nil {
		d.definitions = map[string]toolcontract.OperationDefinition{}
	}
	d.definitions[spec.Name] = definition
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
