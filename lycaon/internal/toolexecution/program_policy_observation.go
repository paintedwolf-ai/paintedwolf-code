package toolexecution

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"strings"

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
	model, err := frame.ModelInvocation(ctx)
	if err != nil {
		return nil, nil, err
	}
	if model.ToolName != "command" || model.ToolCallID != tc.Identity.ToolCallID {
		return nil, nil, fmt.Errorf("program observation model call changed")
	}
	if int64(len(model.CanonicalArguments)) > (math.MaxInt64-4096)/32 {
		return nil, nil, fmt.Errorf("program observation state bound overflow")
	}
	release, err := admission.ReserveProgramState(ctx, int64(len(model.CanonicalArguments))*32+4096)
	if err != nil {
		return nil, nil, err
	}
	var submitted map[string]any
	decoder := json.NewDecoder(strings.NewReader(model.CanonicalArguments))
	decoder.UseNumber()
	if err := decoder.Decode(&submitted); err != nil || submitted == nil {
		release()
		return nil, nil, fmt.Errorf("captured model invocation is not an argument object")
	}
	return submitted, release, nil
}

func (p *programPolicyObservation) evaluate(ctx context.Context, bp *toolfeedback.BlockPlane, anchor, profile string) error {
	return bp.EvaluateProgram(ctx, anchor, profile, p.submitted, p.operation, p.frame.retainProgramFacts)
}
