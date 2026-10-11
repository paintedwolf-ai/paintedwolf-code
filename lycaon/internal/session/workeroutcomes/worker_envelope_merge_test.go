package workeroutcomes

import (
	"testing"

	"github.com/lycaon/lycaon/internal/session/workercompletion"
	"github.com/lycaon/lycaon/pkg/api"
)

func TestMergedWorkerBytesSince(t *testing.T) {
	envelope := workercompletion.FormatWorkerCompletionEnvelope(workercompletion.WorkerCompletionEnvelope{
		JobID:     "j1",
		AgentType: "scout",
		State:     "complete",
		Body:      "hello",
	})
	msg := api.Message{
		Role:    api.MessageRoleTool,
		Content: `{"job_id":"j1","status":"enqueued"}`,
		WorkerSummary: &api.WorkerSummaryMeta{
			WorkerID:  "j1",
			AgentType: "scout",
			Status:    api.WorkerSummaryStatusComplete,
			Envelope:  envelope,
		},
	}
	bytes, count := MergedWorkerBytesSince([]api.Message{msg}, 0)
	if count != 1 || bytes != len("hello") {
		t.Fatalf("bytes=%d count=%d", bytes, count)
	}
	envs := TerminalWorkerEnvelopesSince([]api.Message{msg}, 0)
	if len(envs) != 1 {
		t.Fatalf("envelopes = %d want 1", len(envs))
	}
}
