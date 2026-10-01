package session_test

import (
	"context"
	"errors"
	"testing"

	"github.com/lycaon/lycaon/internal/evidence"
	"github.com/lycaon/lycaon/internal/session/workercloseout"
	"github.com/lycaon/lycaon/pkg/api"
)

type failedFinalizationLedger struct{ err error }

func (r failedFinalizationLedger) LoadLedger(context.Context, string) (evidence.Ledger, error) {
	return evidence.Ledger{}, r.err
}

type failedFinalizationTranscript struct {
	*stubSummaryResolver
	err error
}

func (r failedFinalizationTranscript) GetWorkerJobMessages(context.Context, string, string) ([]api.Message, error) {
	return nil, r.err
}

func TestWorkerFinalizationStorageFailureDoesNotBecomeCompletion(t *testing.T) {
	for _, failureAt := range []string{"ledger", "transcript"} {
		for _, canceled := range []bool{false, true} {
			t.Run(failureAt+map[bool]string{false: "/normal", true: "/canceled"}[canceled], func(t *testing.T) {
				failure := errors.New("worker storage read failed")
				base := &stubSummaryResolver{msgs: []api.Message{completeLegToolRow(map[string]any{
					"leg_status": "complete", "brief": "The assignment is complete",
				})}}
				opts := finalizeOpts(base, "child", workercloseout.WorkerSummaryFinalizeOpts{})
				var resolver workercloseout.WorkerSummaryResolver = base
				if failureAt == "ledger" {
					opts.Ledger = failedFinalizationLedger{err: failure}
				} else {
					resolver = failedFinalizationTranscript{stubSummaryResolver: base, err: failure}
				}
				var result workercloseout.WorkerSummaryOutcome
				var err error
				if canceled {
					result, err = workercloseout.FinalizeWorkerSummaryForCanceled(t.Context(), resolver, "child", "path-explorer", "canceled", opts)
				} else {
					result, err = workercloseout.FinalizeWorkerSummaryForChild(t.Context(), resolver, "child", "path-explorer", opts)
				}
				if !errors.Is(err, failure) {
					t.Fatalf("finalization failure = %v, want original cause", err)
				}
				if result.Summary != "" || result.HintCode != "" || result.HostAssembled {
					t.Fatalf("storage failure produced deliverable completion: %+v", result)
				}
			})
		}
	}
}
