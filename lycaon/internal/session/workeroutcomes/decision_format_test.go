package workeroutcomes

import (
	"strings"
	"testing"

	"github.com/lycaon/lycaon/pkg/api"
)

func TestFormatWorkerDecision(t *testing.T) {
	sum, body := FormatWorkerDecision("implementer", api.WorkerDecisionRequest{
		WorkerID: "job-9", Question: "Refactor the shared type or work around it?",
		Options: []string{"refactor", "work around"},
	})
	if !strings.Contains(sum, "needs a decision") {
		t.Fatalf("summary = %q", sum)
	}
	for _, w := range []string{"NEEDS DECISION", "Refactor the shared type", "1. refactor", "2. work around", `answer_decision(job_id="job-9"`} {
		if !strings.Contains(body, w) {
			t.Fatalf("body missing %q: %q", w, body)
		}
	}
}
