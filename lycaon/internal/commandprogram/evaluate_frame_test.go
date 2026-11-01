package commandprogram

import (
	"context"
	"errors"
	"testing"

	"github.com/lycaon/lycaon/internal/commandsurface"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/toolcontract"
)

func TestIsolatedFrameRefusesPrefixAdmissionBeforeDispatchFork(t *testing.T) {
	const entryBytes = int64(variableBytes + len("NAME") + len("value"))
	for _, tc := range []struct {
		name     string
		capacity int64
	}{
		{"assignment", entryBytes - 1},
		{"export replacement", entryBytes},
	} {
		t.Run(tc.name, func(t *testing.T) {
			fixture := newEvalFixture(t)
			memory, err := toolcontract.NewProgramMemory(toolcontract.ProgramMemoryLimits{Working: 1, State: tc.capacity})
			testutil.FailErr(t, "create prefix admission memory", err)
			fixture.ports.Memory = memory
			evaluation := &evaluation{ports: fixture.ports, programName: "prefix"}
			evaluation.waits = newExecutionWaits(t.Context(), SystemClock(), nil, func(error) {})
			parent, closeParent, err := evaluation.rootFrame(t.Context())
			testutil.FailErr(t, "create parent frame", err)
			defer closeParent()
			parent.prefix = map[string]string{"NAME": "value"}
			child, release, err := parent.isolate(t.Context(), nil)
			if release != nil {
				defer release()
			}
			var refusal *toolcontract.ProgramMemoryLimitError
			if !errors.As(err, &refusal) || refusal.Account != "state" || child != nil || release != nil {
				t.Fatalf("prefix %s admitted a frame: child=%v release=%t err=%v", tc.name, child, release != nil, err)
			}
			if fixture.log.forks != 0 || memory.InUse().State != 0 {
				t.Fatalf("refused prefix escaped admission: forks=%d memory=%+v", fixture.log.forks, memory.InUse())
			}
		})
	}
}

// capturedSettlementRecorder observes the context at the actual execution port.
type capturedSettlementRecorder struct {
	admitted, settled context.Context
	identity          toolcontract.ExecutionIdentity
	settlements       int
}

func (r *capturedSettlementRecorder) BeginExecution(ctx context.Context, invocation toolcontract.OperationInvocation, _ string) error {
	r.admitted, r.identity = ctx, invocation.Identity
	return nil
}
func (r *capturedSettlementRecorder) SettleExecution(ctx context.Context, invocation toolcontract.OperationInvocation, record ChildRecord) error {
	r.settled = ctx
	if invocation.Identity != r.identity || record.Identity != r.identity {
		return errors.New("settlement identity changed")
	}
	r.settlements++
	return nil
}

func TestRecordedSettlementKeepsAdmissionMetadataAfterCancellation(t *testing.T) {
	fixture := newEvalFixture(t)
	recorder := &capturedSettlementRecorder{}
	fixture.ports.Executions = recorder
	limits, err := catalogEvaluationLimits()
	testutil.FailErr(t, "load execution settlement bounds", err)
	evaluation := &evaluation{ports: fixture.ports, limits: limits, programName: "recorded"}
	type operationKey struct{}
	ctx, cancel := context.WithCancel(context.WithValue(t.Context(), operationKey{}, "admitted-operation"))
	defer cancel()
	evaluation.waits = newExecutionWaits(ctx, SystemClock(), nil, func(error) {})
	frame, release, err := evaluation.rootFrame(ctx)
	testutil.FailErr(t, "create recorded frame", err)
	defer release()
	child, err := frame.beginRecorded(ctx, commandsurface.SyntaxLocation{ID: "node", Span: toolcontract.SourceSpan{Line: 1, Column: 1}}, ":")
	testutil.FailErr(t, "admit recorded execution", err)
	cancel()
	child.settle(t.Context(), ordinaryResult(0), nil)
	child.close(t.Context())
	if recorder.settlements != 1 || recorder.admitted.Err() == nil || recorder.settled == nil || recorder.settled.Err() != nil || recorder.settled.Done() != nil || recorder.settled.Value(operationKey{}) != "admitted-operation" {
		t.Fatalf("settlement lost captured context or once-only physical completion: settlements=%d admitted=%v settled=%v", recorder.settlements, recorder.admitted, recorder.settled)
	}
}
