package worker

import (
	"context"
	"errors"
	"slices"
	"testing"

	"github.com/lycaon/lycaon/internal/workerrecords"
	"github.com/lycaon/lycaon/pkg/api"
)

type namer map[string]workerrecords.JobNames

func (n namer) NamesByJob(_ context.Context, jobID string) (workerrecords.JobNames, bool, error) {
	if jobID == "broken" {
		return workerrecords.JobNames{}, false, errors.New("store closed")
	}
	names, ok := n[jobID]
	return names, ok, nil
}

var names = namer{
	"job-1": {Worker: "parser-fix", Handle: "result-1"},
	"job-2": {Worker: "docs"},
}

// A refusal about a worker's job carries the worker and its result handle in
// place of the job; a job with no worker round or a failed lookup carries
// neither, and data without a job is left as it is.
func TestRefusalsNameTheJobsWorkerAndResult(t *testing.T) {
	for _, tc := range []struct {
		job  string
		want map[string]any
	}{
		{"job-1", map[string]any{"worker": "parser-fix", "overlay_id": "result-1"}},
		{"job-2", map[string]any{"worker": "docs"}},
		{"job-none", map[string]any{}},
		{"broken", map[string]any{}},
	} {
		got := withResultRef(t.Context(), names, map[string]any{"job_id": tc.job})
		if len(got) != len(tc.want) {
			t.Fatalf("%s: data = %v want %v", tc.job, got, tc.want)
		}
		for k, v := range tc.want {
			if got[k] != v {
				t.Fatalf("%s: data = %v want %v", tc.job, got, tc.want)
			}
		}
	}
	if got := withResultRef(t.Context(), names, map[string]any{"overlay_id": "x"}); len(got) != 1 {
		t.Fatalf("data without a job changed: %v", got)
	}
}

// Overlapping siblings are named by result handle; a sibling with no settled
// result has no handle and is left out.
func TestMergeResultNamesOverlapsByHandle(t *testing.T) {
	s := &MergeService{Names: names}
	out := api.WorkerMergeResult{OverlapJobIDs: []string{"job-1", "job-2", "broken"}}
	s.nameOverlaps(t.Context(), &out)
	if !slices.Equal(out.OverlapOverlayIDs, []string{"result-1"}) {
		t.Fatalf("overlaps = %v", out.OverlapOverlayIDs)
	}
}
