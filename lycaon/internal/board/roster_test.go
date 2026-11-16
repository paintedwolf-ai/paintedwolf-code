package board

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/lycaon/lycaon/internal/testbaseline"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/pkg/api"
)

func TestPackBoardEnvelopeOmitsHeavyWorkerFields(t *testing.T) {
	baseline := map[string]testbaseline.File{
		"src/main.go": {Content: strings.Repeat("x", 8000)},
	}
	rawBaseline := testbaseline.FromFiles(t, baseline)

	snap := fixtureSnapshot()
	snap.Workers = &api.BoardWorkersSlice{
		"tasks": []api.WorkerTask{{
			ID:                    "job-heavy",
			AgentType:             "implementer",
			Status:                api.WorkerStatusComplete,
			MergeStatus:           api.WorkerMergeStatusMerged,
			Prompt:                strings.Repeat("investigate ", 200),
			Brief:                 "fixture",
			WorkspaceBaselinePath: rawBaseline,
			WorkspaceRoot:         "/tmp/ws",
			Result: &api.WorkerResult{
				ChangeReport: &api.WorkerChangeReport{
					ChangedPaths: []string{"src/main.go"},
				},
			},
		}},
	}
	snap.PackContentHash = PackContentHash(snap, time.Now().UTC())

	out := marshalBoardView(t, &snap, api.BoardDetailLevelCompact, time.Date(2026, 6, 9, 12, 0, 0, 0, time.UTC))
	if len(out) > 4000 {
		t.Fatalf("envelope too large (%d bytes); heavy fields should be stripped", len(out))
	}
	body := string(out)
	for _, forbidden := range []string{"workspace_baseline_path", strings.Repeat("x", 100)} {
		if strings.Contains(body, forbidden) {
			t.Fatalf("envelope must not contain %q", forbidden)
		}
	}

	var env map[string]any
	if err := json.Unmarshal(out, &env); err != nil {
		testutil.FailErr(t, "unmarshal envelope", err)
	}
	summary, _ := env["summary"].(string)
	if summary == "" {
		t.Fatal("summary missing")
	}
	if strings.Contains(body, `"prompt"`) {
		t.Fatal("compact envelope must not include prompt")
	}
	if strings.Contains(body, "workspace_root") {
		t.Fatal("pack_board envelope must not name workspace_root")
	}
}

func TestPackBoardEnvelopeStatusLevelOmitsWorkers(t *testing.T) {
	snap := fixtureSnapshot()
	snap.Workers = &api.BoardWorkersSlice{"workers": []api.BoardWorker{{Name: "parser", AgentType: "implementer", State: api.WorkerStateWorking}}}
	snap.PackContentHash = PackContentHash(snap, time.Now().UTC())
	out := marshalBoardView(t, &snap, api.BoardDetailLevelStatus, time.Now().UTC())
	var env map[string]any
	if err := json.Unmarshal(out, &env); err != nil {
		testutil.FailErr(t, "unmarshal envelope", err)
	}
	if _, ok := env["workers"]; ok {
		t.Fatal("status level must omit workers")
	}
	if env["summary"] == "" {
		t.Fatal("summary missing")
	}
}

func TestPackBoardEnvelopeFullOmitsPromptAndBaselines(t *testing.T) {
	baseline := map[string]testbaseline.File{
		"src/main.go": {Content: "baseline body"},
	}
	rawBaseline := testbaseline.FromFiles(t, baseline)

	snap := fixtureSnapshot()
	snap.Workers = &api.BoardWorkersSlice{
		"tasks": []api.WorkerTask{{
			ID:                    "job-1",
			Status:                api.WorkerStatusComplete,
			Prompt:                strings.Repeat("assignment ", 200),
			Brief:                 "fix the bug",
			WorkspaceBaselinePath: rawBaseline,
		}},
	}
	snap.PackContentHash = PackContentHash(snap, time.Now().UTC())

	out := marshalBoardView(t, &snap, api.BoardDetailLevelFull, time.Now().UTC())
	body := string(out)
	if strings.Contains(body, `"prompt"`) || strings.Contains(body, strings.Repeat("assignment ", 20)) {
		t.Fatalf("full level must omit prompt: %s", body)
	}
	if strings.Contains(body, "baseline body") || strings.Contains(body, "workspace_baseline_path") {
		t.Fatal("full level must never include workspace baselines")
	}
}

func TestBuildBoardViewForensicIncludesNoticeAndTasks(t *testing.T) {
	baseline := map[string]testbaseline.File{
		"src/main.go": {Content: "baseline body"},
	}
	rawBaseline := testbaseline.FromFiles(t, baseline)

	snap := fixtureSnapshot()
	snap.Workers = &api.BoardWorkersSlice{
		"tasks": []api.WorkerTask{{
			ID:                    "job-1",
			Status:                api.WorkerStatusComplete,
			WorkspaceBaselinePath: rawBaseline,
			WorkspaceRoot:         "/Users/me/.config/paintedwolf/worker-branches/ab12cd34/job-1",
			Prompt:                "fix it",
			Brief:                 "fixture",
		}},
	}
	snap.PackContentHash = PackContentHash(snap, time.Now().UTC())

	view := BuildBoardView(&snap, "sess-1", api.BoardDetailLevelForensic, time.Now().UTC())
	if view.ForensicWorkers == nil {
		t.Fatal("forensic_workers missing")
	}
	if view.ForensicWorkers.Notice != api.BoardForensicNotice {
		t.Fatalf("notice = %q", view.ForensicWorkers.Notice)
	}
	if len(view.ForensicWorkers.Tasks) != 1 {
		t.Fatalf("tasks = %d", len(view.ForensicWorkers.Tasks))
	}
	if view.ForensicWorkers.Tasks[0].WorkspaceRoot != "" {
		t.Fatalf("forensic workspace_root = %q want empty", view.ForensicWorkers.Tasks[0].WorkspaceRoot)
	}
}

func TestBoardWorkersCarryTheirLiveRoundReservations(t *testing.T) {
	snap := fixtureSnapshot()
	snap.Workers = &api.BoardWorkersSlice{
		"workers": []api.BoardWorker{
			{Name: "timer", AgentType: "implementer", State: api.WorkerStateWorking, LatestJobID: "job-a"},
			{Name: "parser", AgentType: "implementer", State: api.WorkerStateWorking, LatestJobID: "job-b"},
		},
		"active_reservations": []api.BoardReservationEntry{
			{Path: "shellsim/builtins.py", JobID: "job-a"},
			{Path: "shellsim/timer.py", JobID: "job-a"},
			{Path: "orphan.py", JobID: "job-gone"},
		},
	}
	view := BuildBoardView(&snap, "sess-1", api.BoardDetailLevelCompact, time.Now().UTC())
	if got := view.Workers[0].Reservations; len(got) != 2 || got[0] != "shellsim/builtins.py" || got[1] != "shellsim/timer.py" {
		t.Fatalf("timer reservations = %#v", got)
	}
	if got := view.Workers[1].Reservations; len(got) != 0 {
		t.Fatalf("parser reservations = %#v", got)
	}
}

func TestBuildBoardSummaryPendingMergeNeedsAction(t *testing.T) {
	snap := api.BoardSnapshot{
		Workers: &api.BoardWorkersSlice{
			"tasks": []api.WorkerTask{
				{ID: "a", Status: api.WorkerStatusComplete, MergeStatus: api.WorkerMergeStatusPending},
			},
		},
	}
	got := BuildBoardSummary(snap)
	if !strings.Contains(got, "promote or reject pending overlays") {
		t.Fatalf("summary = %q", got)
	}
}

func TestBuildBoardSummary(t *testing.T) {
	snap := api.BoardSnapshot{
		Workers: &api.BoardWorkersSlice{
			"tasks": []api.WorkerTask{
				{ID: "a", Status: api.WorkerStatusComplete, Result: &api.WorkerResult{CompletionReport: &api.WorkerCompletionReport{LegStatus: "complete"}}, MergeStatus: api.WorkerMergeStatusPending},
				{ID: "b", Status: api.WorkerStatusComplete, Result: &api.WorkerResult{CompletionReport: &api.WorkerCompletionReport{LegStatus: "complete"}}, MergeStatus: api.WorkerMergeStatusPending},
			},
			"promote_paths": []api.WorkerPromoteJobPathStatus{{
				WorkerID: "a",
				Paths:    []api.WorkerPromotePathStatus{{Path: "x.go", Status: api.WorkerPromotePathOutcomeApplied}},
			}},
		},
	}
	got := BuildBoardSummary(snap)
	if !strings.Contains(got, "All workers complete") {
		t.Fatalf("summary = %q", got)
	}
	if !strings.Contains(got, "2 overlay(s) pending merge") {
		t.Fatalf("summary = %q", got)
	}
	if !strings.Contains(got, "no preview conflicts cached") {
		t.Fatalf("summary = %q", got)
	}
}

func TestBuildBoardSummaryConflictPreview(t *testing.T) {
	snap := api.BoardSnapshot{
		Workers: &api.BoardWorkersSlice{
			"tasks": []api.WorkerTask{
				{ID: "a", Status: api.WorkerStatusComplete, MergeStatus: api.WorkerMergeStatusPending},
			},
			"promote_paths": []api.WorkerPromoteJobPathStatus{{
				WorkerID: "a",
				Paths: []api.WorkerPromotePathStatus{{
					Path:      "shellsim/builtins.py",
					Status:    api.WorkerPromotePathOutcomeConflict,
					HunkCount: 84,
				}},
			}},
		},
	}
	got := BuildBoardSummary(snap)
	if !strings.Contains(got, "1 path(s) in conflict (preview)") {
		t.Fatalf("summary = %q", got)
	}
}
