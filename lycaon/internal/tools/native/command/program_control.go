package command

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"strings"
	"sync"

	"github.com/lycaon/lycaon/internal/commandprogram"
	"github.com/lycaon/lycaon/internal/guidance"
	"github.com/lycaon/lycaon/internal/invocation"
	"github.com/lycaon/lycaon/internal/toolcontract"
	"github.com/lycaon/lycaon/internal/toolexecution"
	"github.com/lycaon/lycaon/internal/toolrejection"
	"github.com/lycaon/lycaon/internal/tools"
)

type programControlDefinition struct {
	once        sync.Once
	definitions map[string]toolcontract.OperationDefinition
	available   map[string]bool
	taken       map[string]bool
	err         error
}

type compiledControlCatalog struct {
	direct   map[string]toolcontract.OperationDefinition
	prefixed map[string]toolcontract.OperationDefinition
}

var compiledControlCatalogs = sync.OnceValues(func() (compiledControlCatalog, error) {
	catalog, err := toolcontract.ReadOnlyLanguageV1()
	if err != nil {
		return compiledControlCatalog{}, err
	}
	contract, ok := toolcontract.Lookup("command")
	if !ok {
		return compiledControlCatalog{}, fmt.Errorf("command contract missing")
	}
	direct := make(map[string]toolcontract.OperationDefinition, len(catalog.Commands))
	prefixed := make(map[string]toolcontract.OperationDefinition, len(catalog.Commands))
	for _, spec := range catalog.Commands {
		owned := contract
		owned.Owner = spec.Owner
		defDirect, err := toolcontract.NewOperationDefinition(toolcontract.OperationFacts{Identity: spec.Name, Version: 1, Contract: owned, Arguments: json.RawMessage(`{"type":"object"}`), Commands: []toolcontract.CommandSpecification{spec}, Resources: catalog.Language.Limits, ProgressGate: toolcontract.ProgressNotRequired, RejectionContract: "command", Streams: toolcontract.StreamContract{Input: "complete command input", Output: "complete command output", Error: "typed host refusal", Statuses: map[int]string{0: "completed"}}})
		if err != nil {
			return compiledControlCatalog{}, err
		}
		defPrefixed, err := toolcontract.NewOperationDefinition(toolcontract.OperationFacts{Identity: "command-host:" + spec.Name, Version: 1, Contract: owned, Arguments: json.RawMessage(`{"type":"object"}`), Commands: []toolcontract.CommandSpecification{spec}, Resources: catalog.Language.Limits, ProgressGate: toolcontract.ProgressNotRequired, RejectionContract: "command", Streams: toolcontract.StreamContract{Input: "complete command input", Output: "complete command output", Error: "typed host refusal", Statuses: map[int]string{0: "completed"}}})
		if err != nil {
			return compiledControlCatalog{}, err
		}
		direct[spec.Name] = defDirect
		prefixed[spec.Name] = defPrefixed
	}
	return compiledControlCatalog{direct: direct, prefixed: prefixed}, nil
})

func (r *programRun) controlDefinitions() (map[string]toolcontract.OperationDefinition, error) {
	r.control.once.Do(func() {
		compiled, err := compiledControlCatalogs()
		if err != nil {
			r.control.err = err
			return
		}
		r.control.definitions = make(map[string]toolcontract.OperationDefinition, len(r.catalog.Commands))
		for _, spec := range r.catalog.Commands {
			first, _, _ := strings.Cut(spec.Name, " ")
			if !r.control.available[spec.Name] && !r.control.available[first] && !commandprogram.EvaluatorBuiltin(spec.Name) && spec.Name != "[[" {
				continue
			}
			if r.control.taken[spec.Name] {
				r.control.definitions[spec.Name] = compiled.prefixed[spec.Name]
			} else {
				r.control.definitions[spec.Name] = compiled.direct[spec.Name]
			}
		}
	})
	return r.control.definitions, r.control.err
}

// InvokeEvaluator admits the evaluator's local owner under its real execution
// identity. It does not create a model tool call or grant child effects.
func (d *ProgramDispatcher) InvokeEvaluator(ctx context.Context, request commandprogram.OperationRequest, streams commandprogram.Streams, body func(context.Context, commandprogram.Streams) (toolcontract.ProducerResult, error)) (result toolcontract.ProducerResult, err error) {
	return d.invokeHost(ctx, request, streams, func(ctx context.Context, _ tools.ToolContext, streams commandprogram.Streams) (toolcontract.ProducerResult, error) {
		return body(ctx, streams)
	})
}

func (d *ProgramDispatcher) invokeHost(ctx context.Context, request commandprogram.OperationRequest, streams commandprogram.Streams, body func(context.Context, tools.ToolContext, commandprogram.Streams) (toolcontract.ProducerResult, error)) (toolcontract.ProducerResult, error) {
	spec, ok := d.run.commands[request.OperationID]
	if !ok {
		return refusedResult("COMMAND_NATIVE_FORM_UNSUPPORTED"), fmt.Errorf("evaluator operation is not catalogued")
	}
	definitions, err := d.run.controlDefinitions()
	if err != nil {
		return ownerFailure(err), err
	}
	definition := definitions[spec.Name]
	mode := selectEffectMode(spec, request.EffectMode)
	bound, err := toolcontract.BindOperation(definition, request.Invocation, spec.Name, mode, d.childEffectSet(spec, mode, request))
	if err != nil {
		return ownerFailure(err), err
	}
	arguments := map[string]any{"argv": request.Argv.Values(), "source_unit_id": request.Invocation.SourceUnitID, "source_start_byte": request.Invocation.Span.StartByte, "source_end_byte": request.Invocation.Span.EndByte}
	child, err := d.prepareInvocation(ctx, definition, request.Invocation, spec.Name, arguments, nil)
	if err != nil {
		return d.prepareChildFailure(streams, spec.Name, request, err)
	}
	defer child.release()
	tracker := &trackingWriter{w: nonNilWriter(streams.Error)}
	runStreams := streams
	runStreams.Error = tracker
	effect := toolexecution.NativeEffect{Operation: bound, Arguments: arguments, Streams: runStreams, Inspection: d.run.capture, Stage: toolcontract.ExecutionStage{ExecutionID: request.Invocation.Identity.ExecutionID, OperationID: definition.ReadOnlyFacts().Identity, Argv: request.Argv.Values()}, PrepareReviewed: func(context.Context, map[string]any, tools.ToolContext) (toolexecution.PreparedNativeEffect, error) {
		return toolexecution.PreparedNativeEffect{Operation: bound, InvokeOwner: func(ctx context.Context, tc tools.ToolContext, streams commandprogram.Streams) (toolcontract.ProducerResult, error) {
			return d.runProducer(ctx, request, streams, func(ctx context.Context, streams commandprogram.Streams) (toolcontract.ProducerResult, error) {
				tracker.w = nonNilWriter(streams.Error)
				streams.Error = tracker
				return body(ctx, tc, streams)
			})
		}}, nil
	}}
	marker := &invocation.ProgramAdmissionRecorder{Recorder: d.run.recorder, Inputs: d.run.capture}
	result, runErr := d.run.executor.InvokeNative(ctx, d.run.frame, d.run.surface, d.run.memory, d.run.progress, marker, effect, d.run.tc)
	if reject := toolrejection.AsToolReject(runErr); reject != nil && (!result.OwnerInvoked || result.Producer.Terminal == toolcontract.ProducerRefused) {
		if !tracker.wrote {
			d.writeStatementRefusal(streams, spec.Name, runErr)
		}
		d.shared.mu.Lock()
		if d.shared.rejected == nil {
			d.shared.rejected = &nativeRejection{operation: bound, cause: runErr}
		}
		d.shared.mu.Unlock()
	}
	return result.Producer, joinSettle(runErr, d.settle(ctx, child, result.Producer, result.OwnerInvoked || marker.OwnerStartRecorded, result.HostStopCode))
}

// runProducer opens the evaluator's descriptor scope only after reached-owner
// admission and accepted-draft preparation. Capture then owns the actual streams.
func (d *ProgramDispatcher) runProducer(ctx context.Context, request commandprogram.OperationRequest, streams commandprogram.Streams, body func(context.Context, commandprogram.Streams) (toolcontract.ProducerResult, error)) (result toolcontract.ProducerResult, err error) {
	if err := d.publishAcceptedDocuments(ctx, request.Invocation); err != nil {
		result := ownerFailure(err)
		// Preparation has its own child receipt; this producer never ran.
		result.EffectCertainty, result.Evidence = toolcontract.NoEffect, toolcontract.OperationEvidence{}
		return result, err
	}
	if request.StreamPreparation != nil {
		var err error
		streams, err = request.StreamPreparation.PrepareStreams(ctx)
		if err != nil {
			return ownerFailure(err), err
		}
	}
	streams, retain := d.run.childOutputs(request, streams)
	defer func() { retain(ctx, result, err) }()
	return body(ctx, streams)
}

// reportRefusal uses the same typed rejection owner as final command feedback.
func (d *ProgramDispatcher) reportRefusal(w io.Writer, report commandprogram.RefusalReport) {
	if w == nil || report.Code == "" {
		return
	}
	reject := toolrejection.AsToolReject(report.Cause)
	if reject == nil {
		reject = &toolrejection.ToolReject{Code: report.Code}
	}
	if programResourceRefusal(reject) {
		if d.claimStopLine() {
			writeLimitRefusal(w, report.Operation, report.Operand, reject)
		}
		return
	}
	var policyCopy map[string]string
	if refusal, ok := guidance.RefusalFromError(report.Cause); ok {
		policyCopy = refusal.Copy
	}
	writeShellRefusal(w, report.Operation, report.Operand, reject.Code, policyCopy, map[string]any{"expected_form": report.Expected})
}
