package tools

import (
	"testing"

	"github.com/lycaon/lycaon/internal/guidance"
	"github.com/lycaon/lycaon/internal/isolation"
	"github.com/lycaon/lycaon/pkg/api"
)

func TestVerifyRejectionPreservesTheDeclaredCommandAndBoundary(t *testing.T) {
	executor := NewDefaultToolExecutor(nil, nil, "implement")
	executor.SetBlockPlane(stockBlockPlane(t))
	for name, args := range map[string]map[string]any{
		"command":  {"command": "python3 -B check.py"},
		"pipeline": {"pipeline": []any{"python3 -B check.py"}},
	} {
		t.Run(name, func(t *testing.T) {
			reject := &ToolReject{Code: isolation.CodeDirectIPDenied}
			err := executor.rejectBeforeInvoke(t.Context(), "verify", "coordinator", args, reject)
			refusal, ok := guidance.RefusalFromError(err)
			if !ok || refusal.Code() != isolation.CodeDirectIPDenied {
				t.Fatalf("verify boundary was replaced: %v", err)
			}
			failure := CompleteFailureMetadata(AsToolReject(err), "verify", "verification")
			if failure == nil || failure.Code != refusal.Code() || failure.FailureClass != api.FailureClassIsolationRejection {
				t.Fatalf("refusal and invocation failure disagree: %+v", failure)
			}
		})
	}
}
