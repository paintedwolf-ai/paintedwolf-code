package command

import (
	"context"
	"errors"
	"fmt"
	"sync"

	"github.com/lycaon/lycaon/internal/commandprogram"
	"github.com/lycaon/lycaon/internal/invocation"
	"github.com/lycaon/lycaon/internal/toolcontract"
	"github.com/lycaon/lycaon/internal/toolexecution"
	"github.com/lycaon/lycaon/internal/toolrejection"
	"github.com/lycaon/lycaon/internal/tools"
)

// beginOutput records the actual redirect, not a fabricated utility invocation.
// Its owner admission waits for the staged bytes; its original pin never refreshes.
func (d *ProgramDispatcher) beginOutput(ctx context.Context, destination commandprogram.OutputDestination, path string) (tools.CommandOutputWrite, error) {
	definition, ok := d.run.registry.Operation("write")
	if !ok {
		return tools.CommandOutputWrite{}, fmt.Errorf("output publication requires write owner")
	}
	operation := definition.Operation
	bound, err := toolcontract.BindPublication(operation, destination.Invocation, toolcontract.RedirectPublication, destination.Redirect.Operator)
	if err != nil {
		return tools.CommandOutputWrite{}, err
	}
	review := commandprogram.ArgvReview{OperationID: operation.Facts().Identity, Command: destination.Redirect.Operator, Operands: []string{path}, Options: []commandprogram.ReviewedOption{}}
	child, err := d.prepareInvocation(ctx, operation, destination.Invocation, destination.Redirect.Operator+" "+path, review.Arguments(), nil)
	if err != nil {
		return tools.CommandOutputWrite{}, err
	}
	var once sync.Once
	var published commandprogram.OutputPublished
	var finalErr error
	settle := func(ctx context.Context, apply func(context.Context, tools.ToolContext) (commandprogram.OutputPublished, error), cause error) {
		defer child.release()
		if apply == nil {
			result := toolcontract.ProducerResult{Status: 1, Terminal: toolcontract.ProducerFailed, EffectCertainty: toolcontract.NoEffect}
			if cause != nil {
				result = ownerFailure(cause)
			}
			finalErr = d.settleWith(ctx, child, toolcontract.ProgramChildSettlement{Result: result, HostStopCode: result.StopCode, Conflict: revisionConflict(cause)})
			return
		}
		effect := toolexecution.NativeEffect{Operation: bound, Argv: &review, Inspection: d.run.capture,
			Stage: toolcontract.ExecutionStage{ExecutionID: destination.Invocation.Identity.ExecutionID, OperationID: definition.Operation.Facts().Identity, Argv: []string{destination.Redirect.Operator, path}},
			PrepareReviewed: func(ctx context.Context, values map[string]any, tc tools.ToolContext) (toolexecution.PreparedNativeEffect, error) {
				reviewed, err := tc.Invocation.NativeInputs.Reviewed()
				if err != nil {
					return toolexecution.PreparedNativeEffect{}, err
				}
				if reviewed.ArgumentsDigest() != child.review.ArgumentsDigest() {
					return toolexecution.PreparedNativeEffect{}, &toolrejection.ToolReject{Code: toolrejection.ToolOwnerFailedCode, FailureClass: "owner_error", OwnerRef: definition.Operation.Facts().Contract.Owner, Data: map[string]any{"tool": definition.Operation.Facts().Identity, "path": path, "reviewed_inputs_changed": true}}
				}
				resolved, release, err := toolcontract.NewReviewedOperationInputs(ctx, d.run.memory, values)
				if err != nil {
					return toolexecution.PreparedNativeEffect{}, err
				}
				defer release()
				if resolved.ArgumentsDigest() != child.review.ArgumentsDigest() {
					return toolexecution.PreparedNativeEffect{}, &toolrejection.ToolReject{Code: toolrejection.ToolOwnerFailedCode, FailureClass: "owner_error", OwnerRef: definition.Operation.Facts().Contract.Owner, Data: map[string]any{"tool": definition.Operation.Facts().Identity, "path": path, "reviewed_inputs_changed": true}}
				}
				return toolexecution.PreparedNativeEffect{Operation: bound, InvokeOwner: func(ctx context.Context, tc tools.ToolContext, _ commandprogram.Streams) (toolcontract.ProducerResult, error) {
					var err error
					published, err = apply(ctx, tc)
					if err != nil {
						return ownerFailure(err), err
					}
					return toolcontract.ProducerResult{Status: 0, Terminal: toolcontract.ProducerCompleted, Complete: true, PublicationEligible: true, EffectCertainty: toolcontract.EffectCompleted}, nil
				}}, nil
			},
		}
		marker := &invocation.ProgramAdmissionRecorder{Recorder: d.run.recorder, Inputs: d.run.capture}
		result, err := d.run.executor.InvokeNative(ctx, d.run.frame, d.run.surface, d.run.memory, d.run.progress, marker, effect, d.run.tc)
		if toolrejection.AsToolReject(err) != nil {
			d.shared.mu.Lock()
			if d.shared.rejected == nil {
				d.shared.rejected = &nativeRejection{operation: bound, cause: err}
			}
			d.shared.mu.Unlock()
		}
		finalErr = errors.Join(err, d.settleWith(ctx, child, toolcontract.ProgramChildSettlement{Result: result.Producer, OwnerInvoked: result.OwnerInvoked || marker.OwnerStartRecorded, HostStopCode: result.HostStopCode, AcceptedRevision: published.AcceptedRevision, PublishedRevision: published.PublishedRevision, Conflict: revisionConflict(err)}))
	}
	return tools.CommandOutputWrite{Publish: func(ctx context.Context, apply func(context.Context, tools.ToolContext) (commandprogram.OutputPublished, error)) (commandprogram.OutputPublished, error) {
		once.Do(func() { settle(ctx, apply, nil) })
		return published, finalErr
	}, Abandon: func(ctx context.Context, cause error) error {
		once.Do(func() { settle(ctx, nil, cause) })
		return finalErr
	}}, nil
}
