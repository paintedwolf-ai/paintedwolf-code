package contract

import (
	"testing"

	sessiondecisions "github.com/lycaon/lycaon/internal/session/decisions"
	"github.com/lycaon/lycaon/internal/tools"
	"github.com/lycaon/lycaon/internal/worker"
	contractcheck "github.com/lycaon/lycaon/test/contract/internal/check"
)

// registerContractWorkerSurfaceTools registers the coordinator worker tools.
func registerContractWorkerSurfaceTools(t *testing.T, reg *tools.DefaultRegistry) {
	t.Helper()
	if err := worker.RegisterWorkerCancelTool(reg, worker.CancelToolDeps{Cancel: &worker.CancelService{}}); err != nil {
		contractcheck.FailErr(t, "worker.RegisterWorkerCancelTool", err)
	}
	if err := worker.RegisterOverlayTools(reg, worker.OverlayToolDeps{
		Merge: &worker.MergeService{},
	}); err != nil {
		contractcheck.FailErr(t, "worker.RegisterOverlayTools", err)
	}
	if err := worker.RegisterAnswerDecisionTool(reg, worker.AnswerDecisionToolDeps{
		Answer: &worker.AnswerDecisionService{
			Queue:     worker.NewInMemoryQueue(1),
			Decisions: sessiondecisions.NewMemory(),
		},
	}); err != nil {
		contractcheck.FailErr(t, "worker.RegisterAnswerDecisionTool", err)
	}
}
