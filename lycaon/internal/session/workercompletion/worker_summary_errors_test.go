package workercompletion_test

import (
	"context"
	"errors"
	"testing"

	"github.com/lycaon/lycaon/internal/evidence"
	"github.com/lycaon/lycaon/internal/session/workercompletion"
)

type failedWorkerLedger struct{ err error }

func (r failedWorkerLedger) LoadLedger(context.Context, string) (evidence.Ledger, error) {
	return evidence.Ledger{}, r.err
}

func TestWorkerSummaryLedgerFailureIsNotCitationRejection(t *testing.T) {
	failure := errors.New("evidence read failed")
	result, err := workercompletion.EvaluateWorkerSummary(t.Context(), workerSummaryEval(t, workercompletion.WorkerSummaryEvalInput{
		ChildSessionID: "worker-ledger-failure",
		AgentType:      "path-explorer",
		Report:         workercompletion.WorkerCompletionReport{LegStatus: "complete", Brief: "Completed the survey"},
		Ledger:         failedWorkerLedger{err: failure},
	}))
	if !errors.Is(err, failure) {
		t.Fatalf("worker evidence failure = %v, want original cause", err)
	}
	if result.Status != "" || result.HintCode != "" || result.HintCopy != nil || result.Grounding.Traced {
		t.Fatalf("operational failure became a worker verdict: %+v", result)
	}
}
