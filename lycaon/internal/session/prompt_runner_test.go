package session_test

import (
	"context"
	"testing"

	"github.com/lycaon/lycaon/internal/session/workercloseout"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/pkg/api"
)

func TestWorkerFinalizeUsesJobScopedTranscriptRead(t *testing.T) {
	resolver := &stubSummaryResolver{msgs: []api.Message{completeLegToolRow(map[string]any{"leg_status": "complete", "brief": "done"})}}
	outcome, workerEvalErr := workercloseout.FinalizeWorkerSummaryForChild(
		context.Background(), resolver, "reused-child", "survey",
		workercloseout.WorkerSummaryFinalizeOpts{WorkerJobID: "job-current"},
	)
	testutil.FailErr(t, "evaluate worker completion", workerEvalErr)
	if outcome.Status != string(api.WorkerSummaryStatusComplete) {
		t.Fatalf("status = %q want complete", outcome.Status)
	}
	if resolver.workerJobTranscriptReads == 0 {
		t.Fatal("worker finalization did not use the job-scoped transcript query")
	}
	if resolver.fullTranscriptReads != 0 {
		t.Fatalf("full transcript reads = %d want 0", resolver.fullTranscriptReads)
	}
}
