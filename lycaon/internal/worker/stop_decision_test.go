package worker

import (
	"testing"

	"github.com/lycaon/lycaon/internal/db"
	"github.com/lycaon/lycaon/internal/testutil"
)

// Stopping a worker that is asking removes its unanswered question, so its
// next round does not suspend on it.
func TestStopRemovesTheWorkersQuestion(t *testing.T) {
	f := newWorkerToolsFixture(t)
	f.claimRound(t, "job-1")
	q := db.New(f.db)
	testutil.FailErr(t, "ask", q.UpsertWorkerDecision(t.Context(), db.UpsertWorkerDecisionParams{
		ChildSessionID: "child", JobID: "job-1", DecisionJson: `{"question":"which parser?"}`, CreatedAt: "2026-11-14T00:00:00Z",
	}))
	won, err := f.queue.store.CancelJob(t.Context(), "job-1", nil, "")
	if err != nil || !won {
		t.Fatalf("cancel: won=%v err=%v", won, err)
	}
	if _, err := q.GetWorkerDecisionByJob(t.Context(), "job-1"); !db.IsNoRows(err) {
		t.Fatalf("question survived the stop: %v", err)
	}
}
