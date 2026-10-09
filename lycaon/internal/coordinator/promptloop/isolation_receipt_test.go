package promptloop

import (
	"github.com/lycaon/lycaon/internal/toolrejection"
	"testing"
	"time"

	"github.com/lycaon/lycaon/internal/guidance"
	"github.com/lycaon/lycaon/internal/isolation"
	"github.com/lycaon/lycaon/internal/toolcontract"
	"github.com/lycaon/lycaon/internal/tools"
	"github.com/lycaon/lycaon/pkg/api"
)

func TestInvocationIsolationOfBindsPreOwnerAndAttemptedEffectOutcomes(t *testing.T) {
	tests := []struct {
		name        string
		run         toolInvocation
		code        string
		disposition isolation.Disposition
	}{
		{
			name: "pre-owner failure",
			run: toolInvocation{failure: &api.InvocationFailure{
				Code: isolation.CodeWriteRootDenied, Class: api.FailureClassIsolationRejection,
			}},
			code: isolation.CodeWriteRootDenied, disposition: isolation.DispositionHumanDecision,
		},
		{
			name: "attempted effect",
			run:  toolInvocation{facts: guidance.ToolResultFacts{}.WithCode(isolation.CodeTryWriteRoot)},
			code: isolation.CodeTryWriteRoot, disposition: isolation.DispositionRetry,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got := invocationIsolationOf(test.run)
			if got == nil || got.Code != test.code || got.Disposition != string(test.disposition) {
				t.Fatalf("isolation outcome = %+v, want %s/%s", got, test.code, test.disposition)
			}
		})
	}
}

// A review raised at the owner's effect seam refuses an operation whose owner
// already ran: the refusal stays a terminal isolation rejection with invoked set.
func TestOwnerSeamTerminalRefusalKeepsInvokedIsolationRejection(t *testing.T) {
	contract, ok := toolcontract.Lookup("write")
	if !ok {
		t.Fatal("missing contract for write")
	}
	loop := NewPromptLoopForTest(PromptLoopDeps{})
	runErr := toolrejection.RenderReject(&toolrejection.ToolReject{
		Code: isolation.CodeControlPlaneDenied, Data: map[string]any{"path": "/state/approvals.yaml", "tool": "write"},
	}, nil)
	run := loop.Tools.finalizeToolRun(t.Context(), &api.Session{ID: "session"}, completedToolRun{
		sessionID: "session", call: api.ToolCall{ID: "call", Name: "write"}, contract: contract,
		toolCtx: tools.ToolContext{
			Effects: tools.InvocationEffects{Out: &tools.ToolInvocationOut{OwnerInvoked: true}},
		},
		runErr: runErr, startedAt: time.Now(),
	})
	if run.reject == nil || !run.invoked || run.failure == nil ||
		run.failure.Code != isolation.CodeControlPlaneDenied ||
		run.failure.Class != api.FailureClassIsolationRejection || run.failure.Retryable {
		t.Fatalf("owner-seam refusal = invoked %v failure %+v", run.invoked, run.failure)
	}
	got := invocationIsolationOf(run)
	if got == nil || got.Code != isolation.CodeControlPlaneDenied ||
		got.Disposition != string(isolation.DispositionControlPlane) {
		t.Fatalf("isolation outcome = %+v", got)
	}
}
