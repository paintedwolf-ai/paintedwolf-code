package contract

import (
	"context"
	"testing"

	sessiondecisions "github.com/lycaon/lycaon/internal/session/decisions"
	"github.com/lycaon/lycaon/internal/tools"
	"github.com/lycaon/lycaon/internal/worker"
	"github.com/lycaon/lycaon/pkg/api"
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
	queue := worker.NewInMemoryQueue(1)
	if err := worker.RegisterExtendWorkerBudgetTool(reg, worker.ExtendBudgetToolDeps{Queue: queue, Ledger: memoryBudgetLedger{}}); err != nil {
		contractcheck.FailErr(t, "worker.RegisterExtendWorkerBudgetTool", err)
	}
	if err := worker.RegisterDeclineWorkerBudgetTool(reg, worker.DeclineBudgetToolDeps{Queue: queue, Ledger: memoryBudgetLedger{}}); err != nil {
		contractcheck.FailErr(t, "worker.RegisterDeclineWorkerBudgetTool", err)
	}
	if err := worker.RegisterRequestBudgetTool(reg, worker.RequestBudgetToolDeps{
		Queue: queue, Ledger: memoryBudgetLedger{}, Notify: func(context.Context, api.WorkerTask) {},
	}); err != nil {
		contractcheck.FailErr(t, "worker.RegisterRequestBudgetTool", err)
	}
}

// memoryBudgetLedger accepts every request and answer; the contract checks registration only.
type memoryBudgetLedger struct{}

func (memoryBudgetLedger) Request(context.Context, string, api.WorkerBudgetRequest) (bool, error) {
	return true, nil
}

func (memoryBudgetLedger) Grant(context.Context, string, string, int) error { return nil }

func (memoryBudgetLedger) Decline(context.Context, string) (bool, error) { return true, nil }
