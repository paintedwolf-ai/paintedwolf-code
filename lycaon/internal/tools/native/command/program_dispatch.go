package command

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	lycexec "github.com/lycaon/lycaon/internal/exec"
	"path/filepath"
	"reflect"
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
	// ClosedForms keeps unsupported owner forms as typed refusals instead of
	// launching an installed program with different authority.
	ClosedForms bool
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
	Mode string
	// DeferredSource marks an owner-bound source whose contents can only be
	// classified after the source path's first typed read approval.
	DeferredSource   bool
	RecordSourceRead func(context.Context) error
	Review           commandprogram.ArgvReview
	LoopbackPorts    []uint16
	Invoke           func(context.Context, tools.ToolContext, commandprogram.Streams) (toolcontract.ProducerResult, error)
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
	// stopWritten records that a stopped statement's stderr already carries
	// its resource refusal line.
	stopWritten bool
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

// claimStopLine reports whether this caller writes the program's resource
// stop line; a stop is written once, by whichever statement reports it first.
func (d *ProgramDispatcher) claimStopLine() bool {
	if d.shared == nil {
		return true
	}
	d.shared.mu.Lock()
	defer d.shared.mu.Unlock()
	claimed := !d.shared.stopWritten
	d.shared.stopWritten = true
	return claimed
}

// writeStatementRefusal writes a refused statement's shell-voice line.
func (d *ProgramDispatcher) writeStatementRefusal(streams commandprogram.Streams, command string, cause error) {
	if reject := toolrejection.AsToolReject(cause); programResourceRefusal(reject) {
		if streams.Error != nil && d.claimStopLine() {
			writeLimitRefusal(streams.Error, command, "", reject)
		}
		return
	}
	writeRejectRefusal(streams, command, cause)
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
			request.OperationID = resolved.Name
			return d.invokeBuiltin(ctx, resolved, commandprogram.HelpDispatcher(resolved), request, streams)
		}
	}
	spec, native := d.run.commands[request.Argv[0].Value]
	if native && spec.Name != "external" && !prefixCommands[spec.Name] {
		// An in-process owner's processor time is the host's, not its own.
		request.Process.Times.Unmeasured()
	}
	switch {
	case !native || spec.Name == "external":
		return d.invokeExternal(ctx, request, streams)
	case d.natives[spec.Name].Prepare != nil:
		return d.invokeNative(ctx, spec, d.natives[spec.Name], request, streams)
	case pathCommands[spec.Name]:
		if spec.Name == "cd" || spec.Name == "realpath" || spec.Name == "readlink" || spec.Name == "native_path" {
			return d.invokeNative(ctx, spec, d.pathOperation(spec), request, streams)
		}
		return d.invokeBuiltin(ctx, spec, d.paths, request, streams)
	case d.builtins[spec.Name] != nil:
		// Host builtins read the frame's directory from the invocation scope.
		request.Invocation.Scope.Directory = d.paths.Cwd().DisplayPath
		return d.invokeBuiltin(ctx, spec, d.builtins[spec.Name], request, streams)
	}
	return d.refuseUnimplemented(ctx, spec, request, streams)
}

func (d *ProgramDispatcher) pathOperation(spec toolcontract.CommandSpecification) NativeOperation {
	return NativeOperation{Tool: spec.Name, Prepare: func(_ context.Context, frame *projectpaths.ProgramPaths, _ commandprogram.Dispatch, request commandprogram.OperationRequest) (NativePrepared, error) {
		bound, err := d.binder.Bind(spec, request.Argv)
		if err != nil {
			return NativePrepared{}, err
		}
		review := bound.Review(spec.Name)
		for _, path := range bound.Operands().Values() {
			if spec.Name == "cd" && path == "-" {
				previous, ok := frame.PreviousDirectory()
				if !ok {
					continue
				}
				path = previous
			}
			review.PathTargets = append(review.PathTargets, commandprogram.PathTarget{Path: path})
		}
		return NativePrepared{Review: review, Tool: spec.Name, Invoke: func(ctx context.Context, _ tools.ToolContext, streams commandprogram.Streams) (toolcontract.ProducerResult, error) {
			result, err := frame.Invoke(ctx, request, streams)
			if err == nil && spec.Name == "cd" {
				d.paths.AdoptCwd(frame)
			}
			return result, err
		}}, nil
	}}
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
		if !prefixCommands[spec.Name] {
			// A prefix's nested command notes the job's own process.
			noteJobNative(ctx, request.Argv.Values())
		}
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
		return d.prepareChildFailure(streams, "external", request, err)
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
		Stage:     toolcontract.ExecutionStage{ExecutionID: request.Invocation.Identity.ExecutionID, OperationID: definition.ReadOnlyFacts().Identity, Argv: argv, CommandLine: line, PipelineCommandLine: line},
		PrepareReviewed: func(context.Context, map[string]any, tools.ToolContext) (toolexecution.PreparedNativeEffect, error) {
			return toolexecution.PreparedNativeEffect{Operation: bound, InvokeOwner: func(ctx context.Context, admitted tools.ToolContext, streams commandprogram.Streams) (toolcontract.ProducerResult, error) {
				return d.runProducer(ctx, request, streams, func(ctx context.Context, streams commandprogram.Streams) (toolcontract.ProducerResult, error) {
					return ExternalStages{ToolContext: admitted, Dir: cwd, BaseEnvironment: d.run.baseEnv, ObserveStage: d.recordExternalStage}.Launch(ctx, request, streams)
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
		if gap := unsupportedForm(spec, err); gap != nil && !operation.ClosedForms && (operation.Tool != "" || len(operation.Tools) > 0) && d.GapLaunchable(request) {
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
	var operationDef toolcontract.OperationDefinition
	if definition, ok := d.run.registry.Operation(tool); ok && definitionHasCommand(definition.Operation, spec.Name) {
		operationDef = offeredProgramDefinition(tool, definition.Operation)
	} else if tool == spec.Name {
		var err error
		operationDef, err = d.catalogDefinition(spec)
		if err != nil {
			return refusedResult("COMMAND_PLATFORM_UNAVAILABLE"), err
		}
	} else {
		return d.invokeHosted(ctx, spec, prepared, request, streams)
	}
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
	review.PathTargets = absolutePathTargets(d.paths.Cwd().Abs, d.run.tc.Host.SessionScratchDir, review.PathTargets)
	tracker := &trackingWriter{w: nonNilWriter(streams.Error)}
	runStreams := streams
	runStreams.Error = tracker
	autoLoopback := prepared.LoopbackPorts
	inputs := d.run.nativeArguments(review.Arguments(), operationDef.Facts().Contract, autoLoopback)
	expectedMode := prepared.Mode
	var disclosed *NativePrepared
	defer func() {
		if disclosed != nil && disclosed.Release != nil {
			disclosed.Release()
		}
	}()
	effect := toolexecution.NativeEffect{
		Operation: bound, Streams: runStreams, Arguments: inputs, Argv: &review, Inspection: d.run.capture,
		Stage: toolcontract.ExecutionStage{ExecutionID: request.Invocation.Identity.ExecutionID, OperationID: operationDef.Facts().Identity, Argv: request.Argv.Values()},
		PrepareReviewed: func(ctx context.Context, _ map[string]any, admitted tools.ToolContext) (toolexecution.PreparedNativeEffect, error) {
			// The program's directory frame predates this child's approval. Bind
			// the owner again with the admitted context so exact file access is
			// available to the path authority used by the native operation.
			frame, err := d.paths.Rebind(ctx, admitted)
			if err != nil {
				return toolexecution.PreparedNativeEffect{}, err
			}
			var actual NativePrepared
			if disclosed != nil {
				actual = *disclosed
			} else {
				actual, err = operation.Prepare(ctx, frame, d, prepareReq)
				if err != nil {
					return toolexecution.PreparedNativeEffect{}, err
				}
				actual.Review.OperationID = operationDef.Facts().Identity
				actual.Review.PathTargets = absolutePathTargets(frame.Cwd().Abs, admitted.Host.SessionScratchDir, actual.Review.PathTargets)
			}
			if prepared.DeferredSource && disclosed == nil && !reflect.DeepEqual(actual.Review.PathTargets, review.PathTargets) {
				prior := actual.Review
				prior.PathTargets = review.PathTargets
				if !reflect.DeepEqual(prior, review) || !slices.Equal(actual.LoopbackPorts, prepared.LoopbackPorts) || actual.Tool != prepared.Tool || actual.Command != prepared.Command {
					if actual.Release != nil {
						actual.Release()
					}
					return toolexecution.PreparedNativeEffect{}, fmt.Errorf("native source binding changed after approval")
				}
				for _, target := range review.PathTargets {
					if !slices.Contains(actual.Review.PathTargets, target) {
						if actual.Release != nil {
							actual.Release()
						}
						return toolexecution.PreparedNativeEffect{}, fmt.Errorf("native source removed an approved path target")
					}
				}
				newMode := selectEffectMode(spec, actual.Mode)
				newBound, bindErr := toolcontract.BindOperation(operationDef, request.Invocation, spec.Name, newMode, d.childEffectSet(spec, newMode, request))
				if bindErr != nil {
					if actual.Release != nil {
						actual.Release()
					}
					return toolexecution.PreparedNativeEffect{}, bindErr
				}
				if actual.RecordSourceRead == nil {
					if actual.Release != nil {
						actual.Release()
					}
					return toolexecution.PreparedNativeEffect{}, fmt.Errorf("deferred native source has no revision recorder")
				}
				if err := actual.RecordSourceRead(ctx); err != nil {
					if actual.Release != nil {
						actual.Release()
					}
					return toolexecution.PreparedNativeEffect{}, err
				}
				disclosed = &actual
				review = actual.Review
				expectedMode = actual.Mode
				bound = newBound
				return toolexecution.PreparedNativeEffect{Operation: newBound, DisclosedArgv: &review}, nil
			}
			if !reflect.DeepEqual(actual.Review, review) || !slices.Equal(actual.LoopbackPorts, prepared.LoopbackPorts) || actual.Mode != expectedMode || actual.Tool != prepared.Tool || actual.Command != prepared.Command {
				return toolexecution.PreparedNativeEffect{}, fmt.Errorf("native binding changed after approval")
			}
			return toolexecution.PreparedNativeEffect{Operation: bound, InvokeOwner: func(ctx context.Context, tc tools.ToolContext, streams commandprogram.Streams) (toolcontract.ProducerResult, error) {
				if disclosed == nil && actual.Release != nil {
					defer actual.Release()
				}
				return d.runProducer(ctx, request, streams, func(ctx context.Context, streams commandprogram.Streams) (toolcontract.ProducerResult, error) {
					tracker.w = nonNilWriter(streams.Error)
					streams.Error = tracker
					noteJobNative(ctx, request.Argv.Values())
					return actual.Invoke(ctx, tc, streams)
				})
			}}, nil
		},
	}
	child, err := d.prepareDefinition(ctx, operationDef, request, inputs)
	if err != nil {
		return d.prepareChildFailure(streams, spec.Name, request, err)
	}
	defer child.release()
	marker := &invocation.ProgramAdmissionRecorder{Recorder: d.run.recorder, Inputs: d.run.capture}
	native, runErr := d.run.executor.InvokeNative(ctx, d.run.frame, d.run.surface, d.run.memory, d.run.progress, marker, effect, d.run.tc)
	if reject := toolrejection.AsToolReject(runErr); reject != nil && (!native.OwnerInvoked || native.Producer.Terminal == toolcontract.ProducerRefused) {
		if !tracker.wrote {
			d.writeStatementRefusal(streams, spec.Name, runErr)
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
		noteJobNative(ctx, request.Argv.Values())
		return prepared.Invoke(ctx, tc, streams)
	})
}

func absolutePathTargets(cwd, scratch string, targets []commandprogram.PathTarget) []commandprogram.PathTarget {
	if targets == nil {
		return nil
	}
	resolved := make([]commandprogram.PathTarget, len(targets))
	for i, target := range targets {
		resolved[i] = target
		if target.Path == "@scratch" || strings.HasPrefix(target.Path, "@scratch/") {
			rel := strings.TrimPrefix(target.Path, "@scratch")
			rel = strings.TrimPrefix(rel, "/")
			clean := filepath.Clean(rel)
			if scratch != "" && clean != ".." && !strings.HasPrefix(clean, ".."+string(filepath.Separator)) {
				resolved[i].Path = filepath.Join(scratch, clean)
			}
			continue
		}
		if target.Path != "" && !filepath.IsAbs(target.Path) {
			resolved[i].Path = filepath.Clean(filepath.Join(cwd, target.Path))
		}
	}
	return resolved
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
	d.writeStatementRefusal(streams, spec.Name, rejection)
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

func (d *ProgramDispatcher) prepareChildFailure(streams commandprogram.Streams, command string, request commandprogram.OperationRequest, cause error) (toolcontract.ProducerResult, error) {
	reject := toolrejection.AsToolReject(cause)
	if reject == nil {
		var limit *toolcontract.ProgramMemoryLimitError
		if errors.As(cause, &limit) {
			limitID := "program_state_bytes"
			switch limit.Account {
			case "working":
				limitID = "evaluation_memory_bytes"
			case "session_output_state":
				limitID = "session_output_state_bytes"
			}
			cause = toolrejection.RejectInvalidArguments("COMMAND_LIMIT_EXCEEDED", map[string]any{"command": command, "limit_id": limitID, "resource_unit": "bytes", "maximum": limit.Maximum, "observed": limit.Observed})
			reject = toolrejection.AsToolReject(cause)
		}
	}
	if reject != nil {
		if reject.Data == nil {
			reject.Data = map[string]any{}
		}
		reject.Data["command"] = command
		span := request.Invocation.Span
		reject.Data["source_start_byte"], reject.Data["source_end_byte"] = span.StartByte, span.EndByte
		reject.Data["source_line"], reject.Data["source_column"] = span.Line, span.Column
		d.writeStatementRefusal(streams, command, cause)
		return refusedResult(reject.Code), cause
	}
	return refusedResult("COMMAND_OPERAND_INVALID"), cause
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
	return d.prepareInvocationWithOwner(ctx, definition, invocation, display, inputs, gap, nil)
}

func (d *ProgramDispatcher) prepareInvocationWithOwner(ctx context.Context, definition toolcontract.OperationDefinition, invocation toolcontract.OperationInvocation, display string, inputs map[string]any, gap *toolcontract.NativeGap, startOwner *toolcontract.BoundOperation) (programChild, error) {
	prepareCtx := context.WithoutCancel(ctx)
	review, release, err := toolcontract.NewReviewedOperationInputs(prepareCtx, d.run.memory, inputs)
	if err != nil {
		return programChild{}, err
	}
	start := toolcontract.ProgramChildStart{ParentReceiptID: d.run.parentReceipt, Invocation: invocation, Definition: definition, ExpandedInputs: review, Canonical: display, NativeGap: gap}
	if startOwner != nil {
		start.StartOwner = startOwner
		start.ModelToolCallID = d.run.tc.Identity.ToolCallID
	}
	reference, err := d.run.capture.PrepareChild(prepareCtx, start)
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

var compiledCatalogDefinitions sync.Map

// catalogDefinition compiles one definition per catalog command without a
// registered tool, attributed to the command's declared owner.
func (d *ProgramDispatcher) catalogDefinition(spec toolcontract.CommandSpecification) (toolcontract.OperationDefinition, error) {
	if cached, ok := compiledCatalogDefinitions.Load(spec.Name); ok {
		return cached.(toolcontract.OperationDefinition), nil
	}
	d.shared.mu.Lock()
	defer d.shared.mu.Unlock()
	if definition, ok := d.shared.definitions[spec.Name]; ok {
		return definition, nil
	}
	definition, err := catalogOperationDefinition(spec, d.run.catalog.Language.Limits)
	if err != nil {
		return toolcontract.OperationDefinition{}, err
	}
	if d.shared.definitions == nil {
		d.shared.definitions = map[string]toolcontract.OperationDefinition{}
	}
	d.shared.definitions[spec.Name] = definition
	return definition, nil
}

func catalogOperationDefinition(spec toolcontract.CommandSpecification, limits []toolcontract.ResourceLimit) (toolcontract.OperationDefinition, error) {
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
		Commands: []toolcontract.CommandSpecification{spec}, Resources: limits,
		Streams:      toolcontract.StreamContract{Input: spec.Encoding, Output: spec.Encoding, Error: "typed owner rejection; prose is diagnostic", Statuses: statuses},
		ProgressGate: toolcontract.ProgressNotRequired, RejectionContract: spec.Name,
	})
	if err != nil {
		return toolcontract.OperationDefinition{}, err
	}
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

// catalogCommand resolves an exact catalog name without accepting aliases.
func (r *programRun) catalogCommand(name string) (toolcontract.CommandSpecification, bool) {
	for _, spec := range r.catalog.Commands {
		if spec.Name == name {
			return spec, true
		}
	}
	return toolcontract.CommandSpecification{}, false
}

// commandIndex maps catalog names and single-word aliases.
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
