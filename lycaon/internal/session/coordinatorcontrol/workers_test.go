package coordinatorcontrol

import (
	"context"
	"errors"
	"testing"

	"github.com/lycaon/lycaon/internal/coordinator/kick"
)

func TestRoundNoticeIsAddressedByRoundProvenance(t *testing.T) {
	prov := func(_ context.Context, jobID string) (string, int64, string, int64, bool, error) {
		switch jobID {
		case "job-4":
			return "builder", 4, "work-auth", 9, true, nil
		case "job-locked":
			return "builder", 4, "work-auth", 9, true, errors.New("locked")
		}
		return "", 0, "", 0, false, nil
	}
	w := &Workers{RoundProvenance: prov}
	got := w.addressed(t.Context(), kick.WorkerRoundNoticeFacts{JobID: "job-4"})
	if got.Worker != "builder" || got.RoundOrdinal != 4 || got.WorkflowWorkID != "work-auth" || got.LatestMessageSeq != 9 {
		t.Fatalf("provenance facts = %+v", got)
	}
	kept := w.addressed(t.Context(), kick.WorkerRoundNoticeFacts{JobID: "job-4", WorkflowWorkID: "work-own"})
	if kept.WorkflowWorkID != "work-own" {
		t.Fatalf("workflow work id = %q, want the notice's own", kept.WorkflowWorkID)
	}
	for _, job := range []string{"job-locked", "job-none"} {
		if got := w.addressed(t.Context(), kick.WorkerRoundNoticeFacts{JobID: job}); got.Worker != "" || got.LatestMessageSeq != 0 {
			t.Fatalf("%s facts = %+v, want none", job, got)
		}
	}
	if got := (&Workers{}).addressed(t.Context(), kick.WorkerRoundNoticeFacts{JobID: "job-4"}); got.Worker != "" {
		t.Fatalf("no provenance source facts = %+v", got)
	}
}
