package coordinatorcontrol

import (
	"context"
	"errors"
	"testing"

	"github.com/lycaon/lycaon/internal/coordinator/kick"
)

func TestRoundNoticeIsAddressedByNameAndLatestMessage(t *testing.T) {
	names := func(_ context.Context, jobID string) (string, bool, error) { return "parser", jobID == "job-1", nil }
	for _, tc := range []struct {
		name    string
		latest  func(context.Context, string) (int64, error)
		wantSeq int64
	}{
		{"sent", func(context.Context, string) (int64, error) { return 6, nil }, 6},
		{"unreadable", func(context.Context, string) (int64, error) { return 9, errors.New("locked") }, 0},
		{"no source", nil, 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			w := &Workers{Names: names, LatestSent: tc.latest}
			got := w.addressed(t.Context(), kick.WorkerRoundNoticeFacts{JobID: "job-1"})
			if got.Worker != "parser" || got.LatestMessageSeq != tc.wantSeq {
				t.Fatalf("facts = %+v, want parser at #%d", got, tc.wantSeq)
			}
		})
	}

	w := &Workers{
		RoundProvenance: func(_ context.Context, jobID string) (string, int64, string, int64, bool, error) {
			return "builder", 4, "work-auth", 9, jobID == "job-4", nil
		},
	}
	got := w.addressed(t.Context(), kick.WorkerRoundNoticeFacts{JobID: "job-4"})
	if got.Worker != "builder" || got.RoundOrdinal != 4 || got.WorkflowWorkID != "work-auth" || got.LatestMessageSeq != 9 {
		t.Fatalf("provenance facts = %+v", got)
	}
}
