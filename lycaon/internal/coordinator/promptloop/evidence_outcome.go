package promptloop

import (
	"github.com/lycaon/lycaon/internal/evidence"
	"github.com/lycaon/lycaon/internal/toolcontract"
	"github.com/lycaon/lycaon/pkg/api"
)

// toolEvidenceEligible preserves failed effect receipts as observations of the
// outcome, without turning a failed file operation into mutation proof.
func toolEvidenceEligible(run toolInvocation, tool string, args map[string]any) bool {
	if run.hostAnswered || run.reject != nil {
		return false
	}
	if run.succeeded() {
		return true
	}
	if run.receipt == nil || !run.receipt.Invoked ||
		run.receipt.Status != api.InvocationStatusError || run.contract.Evidence() != toolcontract.EvidenceAttempt {
		return false
	}
	binding := evidence.ActiveBinding()
	kind := binding.ToolKindForArgs(tool, args)
	return binding.ShapeForToolKind(tool, kind) == evidence.ShapeCommand
}
