package toolexecution

import (
	"context"
	"fmt"

	"github.com/lycaon/lycaon/internal/toolcontract"
	"github.com/lycaon/lycaon/internal/toolfeedback"
	"github.com/lycaon/lycaon/internal/tools"
)

// Program policy observes the initiating model envelope. Domain review and
// execution keep their separate, lossless operation inputs.
type programPolicyObservation struct {
	submitted map[string]any
	operation *toolcontract.BoundOperation
	frame     ProgramFrame
}

type deferredProgramRejectionKey struct{}

func prepareProgramPolicyObservation(ctx context.Context, frame ProgramFrame, admission *toolcontract.ProgramMemory, tc tools.ToolContext, operation *toolcontract.BoundOperation) (context.Context, *programPolicyObservation, func(), error) {
	submitted, release, err := programModelArguments(ctx, frame, admission, tc)
	if err != nil {
		return ctx, nil, nil, err
	}
	// Intrinsic failures remain typed until their actual recorder settles them.
	// This marker changes feedback timing, never admission or authority.
	ctx = context.WithValue(ctx, deferredProgramRejectionKey{}, true)
	return ctx, &programPolicyObservation{submitted: submitted, operation: operation, frame: frame}, release, nil
}

func programModelArguments(ctx context.Context, frame ProgramFrame, admission *toolcontract.ProgramMemory, tc tools.ToolContext) (map[string]any, func(), error) {
	if admission == nil {
		return nil, nil, fmt.Errorf("program observation requires state admission")
	}
	end, err := frame.beginUse()
	if err != nil {
		return nil, nil, err
	}
	defer func() {
		if end != nil {
			end()
		}
	}()
	end, err := frame.beginUse()
	if err != nil {
		return nil, nil, err
	}
	defer func() {
		if end != nil {
			end()
		}
	}()
	model, err := frame.ModelInvocation(ctx)
	if err != nil {
		return nil, nil, err
	}
	if model.ToolName != "command" || model.ToolCallID != tc.Identity.ToolCallID {
		return nil, nil, fmt.Errorf("program observation model call changed")
	}
	frame.lifetime.mu.Lock()
	values := frame.lifetime.arguments.values
	frame.lifetime.mu.Unlock()
	snapshot, err := prepareNativeArguments(ctx, admission, values)
	if err != nil {
		return nil, nil, err
	}
	finish := end
	end = nil
	return snapshot.values, func() { snapshot.close(); finish() }, nil
}

func (p *programPolicyObservation) evaluate(ctx context.Context, bp *toolfeedback.BlockPlane, anchor, profile string) error {
	return bp.EvaluateProgram(ctx, anchor, profile, p.submitted, p.operation, p.frame.retainProgramFacts)
}
