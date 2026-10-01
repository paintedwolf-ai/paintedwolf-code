package overlayplan_test

import (
	"testing"

	"github.com/lycaon/lycaon/internal/overlayplan"
	"github.com/lycaon/lycaon/pkg/api"
)

func TestBuildOverlayMergePlanPromoteSequenceFromBlockedBy(t *testing.T) {
	tasks := []api.WorkerTask{
		{ID: "job-themes", ParentSessionID: "sess-1", Status: api.WorkerStatusComplete, MergeStatus: api.WorkerMergeStatusPending,
			Scope:  &api.TaskScope{Mode: api.TaskScopeModeWrite, Paths: []string{"suggested.py"}},
			Result: &api.WorkerResult{ChangeReport: &api.WorkerChangeReport{ChangedPaths: []string{"themes.py"}}}, WorkspaceRoot: "/tmp/o1"},
		{ID: "job-timing", ParentSessionID: "sess-1", Status: api.WorkerStatusComplete, MergeStatus: api.WorkerMergeStatusPending,
			Result: &api.WorkerResult{ChangeReport: &api.WorkerChangeReport{ChangedPaths: []string{"prompt.py"}}}, WorkspaceRoot: "/tmp/o2"},
		{ID: "job-history", ParentSessionID: "sess-1", Status: api.WorkerStatusComplete, MergeStatus: api.WorkerMergeStatusPending,
			Result: &api.WorkerResult{ChangeReport: &api.WorkerChangeReport{ChangedPaths: []string{"history.py"}}}, WorkspaceRoot: "/tmp/o3"},
		{ID: "job-alias", ParentSessionID: "sess-1", Status: api.WorkerStatusComplete, MergeStatus: api.WorkerMergeStatusPending,
			Result: &api.WorkerResult{ChangeReport: &api.WorkerChangeReport{ChangedPaths: []string{"prompt.py", "history.py", "aliases.py"}}}, WorkspaceRoot: "/tmp/o4"},
	}
	preview := func(_ string, jobID string) (overlayplan.PreviewSnapshot, bool) {
		switch jobID {
		case "job-timing":
			return overlayplan.PreviewSnapshot{PromoteOrder: api.WorkerPromoteOrderCleanIfFirst, BlockedBy: []string{"job-alias"}}, true
		case "job-history":
			return overlayplan.PreviewSnapshot{PromoteOrder: api.WorkerPromoteOrderCleanIfFirst, BlockedBy: []string{"job-alias"}}, true
		case "job-alias":
			return overlayplan.PreviewSnapshot{PromoteOrder: api.WorkerPromoteOrderCleanIfFirst, BlockedBy: []string{"job-timing", "job-history"}}, true
		default:
			return overlayplan.PreviewSnapshot{PromoteOrder: api.WorkerPromoteOrderIndependent}, true
		}
	}
	plan := overlayplan.Build("sess-1", tasks, preview)
	if plan.PendingCount != 4 {
		t.Fatalf("pending_count=%d", plan.PendingCount)
	}
	if len(plan.SharedPaths) != 2 {
		t.Fatalf("shared_paths=%v", plan.SharedPaths)
	}
	seq := plan.PromoteSequence
	if len(seq) != 4 {
		t.Fatalf("sequence=%v", seq)
	}
	if seq[0] != "job-themes" {
		t.Fatalf("themes should promote first: %v", seq)
	}
	if seq[len(seq)-1] != "job-alias" {
		t.Fatalf("alias should promote last: %v", seq)
	}
}
