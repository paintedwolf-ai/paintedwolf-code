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
	latest := map[string]overlayplan.LatestResult{
		"job-themes": {Worker: "themes", Handle: "r-themes"}, "job-timing": {Worker: "timing", Handle: "r-timing"},
		"job-history": {Worker: "history", Handle: "r-history"}, "job-alias": {Worker: "alias", Handle: "r-alias"},
	}
	plan := overlayplan.Build("sess-1", tasks, preview, latest)
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
	if seq[0] != "r-themes" {
		t.Fatalf("themes should promote first: %v", seq)
	}
	if seq[len(seq)-1] != "r-alias" {
		t.Fatalf("alias should promote last: %v", seq)
	}
}

func TestBuildOverlayMergePlanExcludesUnfinishedLegsFromPromoteSequence(t *testing.T) {
	tasks := []api.WorkerTask{
		{
			ID: "job-partial", ParentSessionID: "sess-1", Status: api.WorkerStatusComplete, MergeStatus: api.WorkerMergeStatusPending,
			Result: &api.WorkerResult{
				Status:           "partial",
				CompletionReport: &api.WorkerCompletionReport{LegStatus: "partial"},
				ChangeReport:     &api.WorkerChangeReport{ChangedPaths: []string{"status.py"}},
			},
			WorkspaceRoot: "/tmp/o-partial",
		},
		{
			ID: "job-blocked", ParentSessionID: "sess-1", Status: api.WorkerStatusComplete, MergeStatus: api.WorkerMergeStatusPending,
			Result: &api.WorkerResult{
				Status:       "blocked",
				ChangeReport: &api.WorkerChangeReport{ChangedPaths: []string{"status.py"}},
			},
			WorkspaceRoot: "/tmp/o-blocked",
		},
	}
	plan := overlayplan.Build("sess-1", tasks, func(string, string) (overlayplan.PreviewSnapshot, bool) {
		return overlayplan.PreviewSnapshot{PromoteOrder: api.WorkerPromoteOrderIndependent}, true
	}, map[string]overlayplan.LatestResult{"job-partial": {Worker: "a", Handle: "r-a"}, "job-blocked": {Worker: "b", Handle: "r-b"}})
	if plan.PendingCount != 2 {
		t.Fatalf("pending_count = %d, want 2", plan.PendingCount)
	}
	if len(plan.PromoteSequence) != 0 {
		t.Fatalf("promote_sequence = %v, want empty for unfinished legs", plan.PromoteSequence)
	}
	if len(plan.SharedPaths) != 1 || plan.SharedPaths[0].Path != "status.py" {
		t.Fatalf("shared_paths = %v, want status.py", plan.SharedPaths)
	}
}

// A worker's earlier round takes no place of its own: landing its latest
// result disposes it. Every reference is a result handle and a worker name.
func TestMergePlanCoversEachWorkersLatestResult(t *testing.T) {
	pending := func(id string, paths ...string) api.WorkerTask {
		return api.WorkerTask{ID: id, ParentSessionID: "s", Status: api.WorkerStatusComplete, MergeStatus: api.WorkerMergeStatusPending,
			Result: &api.WorkerResult{ChangeReport: &api.WorkerChangeReport{ChangedPaths: paths}}, WorkspaceRoot: "/tmp/" + id}
	}
	tasks := []api.WorkerTask{pending("fixer-1", "total.go"), pending("fixer-2", "total.go"), pending("docs-1", "total.go")}
	plan := overlayplan.Build("s", tasks, func(_, jobID string) (overlayplan.PreviewSnapshot, bool) {
		if jobID == "fixer-2" {
			return overlayplan.PreviewSnapshot{PromoteOrder: api.WorkerPromoteOrderCleanAfter, PromoteAfter: []string{"docs-1", "fixer-1"}}, true
		}
		return overlayplan.PreviewSnapshot{}, false
	}, map[string]overlayplan.LatestResult{"fixer-2": {Worker: "fixer", Handle: "r-fixer"}, "docs-1": {Worker: "docs", Handle: "r-docs"}})
	if plan.PendingCount != 2 || len(plan.Entries) != 2 {
		t.Fatalf("plan = %+v", plan)
	}
	for _, e := range plan.Entries {
		if (e.Worker == "fixer") != (e.OverlayID == "r-fixer") {
			t.Fatalf("entry = %+v", e)
		}
		if e.Worker == "fixer" && (len(e.PromoteAfter) != 1 || e.PromoteAfter[0] != "r-docs") {
			t.Fatalf("promote_after = %v", e.PromoteAfter)
		}
	}
	if len(plan.SharedPaths) != 1 || len(plan.SharedPaths[0].OverlayIDs) != 2 {
		t.Fatalf("shared = %+v", plan.SharedPaths)
	}
	if len(plan.PromoteSequence) != 2 || plan.PromoteSequence[0] != "r-docs" || plan.PromoteSequence[1] != "r-fixer" {
		t.Fatalf("sequence = %v", plan.PromoteSequence)
	}
}
