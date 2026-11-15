package turnguards

import (
	"context"
	"errors"
	"testing"

	"github.com/lycaon/lycaon/internal/coordinator/guard"
	"github.com/lycaon/lycaon/internal/workerrecords"
	"github.com/lycaon/lycaon/pkg/api"
)

// Only a call that accounts for the result the latch holds is exempt: an
// instruction to the worker that delivered it, or a review of it. Another
// worker or result, a lookup failure or another tool stays under the latch.
func TestLatchExemptsOnlyCallsThatAccountForTheLatchedResult(t *testing.T) {
	latest := map[string]string{"fixer": "job-held", "scout": "job-other"}
	handles := map[string]string{"result-held": "job-held", "result-other": "job-other"}
	m := &Service{}
	m.SetWorkerResults(func(_ context.Context, sessionID, name string) (string, bool, error) {
		if sessionID != "coord" {
			t.Fatalf("resolved in session %q", sessionID)
		}
		if name == "broken" {
			return "", false, errors.New("store closed")
		}
		job, ok := latest[name]
		return job, ok, nil
	})
	m.SetResultJobs(func(_ context.Context, _, handle string) (string, bool, error) {
		job, ok := handles[handle]
		return job, ok, nil
	})
	sess := &api.Session{ID: "coord"}
	baseline := guard.ProgressClosureBaseline{JobIDs: []string{"job-held"}}
	for _, tc := range []struct {
		tool   string
		args   map[string]any
		exempt bool
	}{
		{"instruct_worker", map[string]any{"worker": "fixer"}, true},
		{"instruct_worker", map[string]any{"worker": "scout"}, false},
		{"instruct_worker", map[string]any{"worker": "nobody"}, false},
		{"instruct_worker", map[string]any{"worker": "broken"}, false},
		{"task", map[string]any{"review_of": "result-held"}, true},
		{"task", map[string]any{"review_of": "result-other"}, false},
		{"task", map[string]any{"worker": "fixer"}, false},
		{"shelve_overlay", map[string]any{"review_of": "result-held"}, false},
	} {
		if got := m.accountsLatchedResult(t.Context(), sess, tc.tool, tc.args, baseline); got != tc.exempt {
			t.Fatalf("%s %v: exempt=%v, want %v", tc.tool, tc.args, got, tc.exempt)
		}
	}
}

type jobNames map[string]workerrecords.JobNames

func (n jobNames) NamesByJob(_ context.Context, jobID string) (workerrecords.JobNames, bool, error) {
	if jobID == "broken" {
		return workerrecords.JobNames{}, false, errors.New("store closed")
	}
	names, ok := n[jobID]
	return names, ok, nil
}

// The closeout refusal names the pending result by worker and handle, never
// the job; a result the coordinator shelved does not hold closeout.
func TestPendingResultIsNamed(t *testing.T) {
	m := &Service{}
	if got := m.pendingResultData(t.Context(), "job-1"); len(got) != 0 {
		t.Fatalf("without names = %v", got)
	}
	m.SetJobNames(jobNames{"job-1": {Worker: "parser-fix", Handle: "result-1"}, "job-2": {Worker: "docs"}, "job-3": {Worker: "kept", Handle: "result-3", Change: workerrecords.ChangeShelved}})
	got := m.pendingResultData(t.Context(), "job-1")
	if got["worker"] != "parser-fix" || got["overlay_id"] != "result-1" || got["job_id"] != nil {
		t.Fatalf("named = %v", got)
	}
	if got := m.pendingResultData(t.Context(), "job-2"); got["worker"] != "docs" || got["overlay_id"] != nil {
		t.Fatalf("unsettled = %v", got)
	}
	if got := m.pendingResultData(t.Context(), "broken"); len(got) != 0 {
		t.Fatalf("failed lookup = %v", got)
	}
	if got := m.openResults(t.Context(), []string{"job-1", "job-3", "broken"}); len(got) != 2 || got[0] != "job-1" || got[1] != "broken" {
		t.Fatalf("open results = %v", got)
	}
}
