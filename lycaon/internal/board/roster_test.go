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
	roster, ok := env["roster"].([]any)
	if !ok || len(roster) != 1 {
		t.Fatalf("roster = %#v", env["roster"])
	}
	row, _ := roster[0].(map[string]any)
	if row["changed_paths"] == nil {
		t.Fatal("roster row missing changed_paths")
	}
	if _, hasPrompt := row["prompt"]; hasPrompt {
		t.Fatal("compact roster must not include prompt")
	}
	if _, hasRoot := row["workspace_root"]; hasRoot {
		t.Fatal("roster must not include workspace_root")
	}
	if strings.Contains(body, "workspace_root") {
		t.Fatal("pack_board envelope must not name workspace_root")
	}
}

func TestPackBoardEnvelopeStatusLevelOmitsRoster(t *testing.T) {
	snap := fixtureSnapshot()
	snap.PackContentHash = PackContentHash(snap, time.Now().UTC())
	out := marshalBoardView(t, &snap, api.BoardDetailLevelStatus, time.Now().UTC())
	var env map[string]any
	if err := json.Unmarshal(out, &env); err != nil {
		testutil.FailErr(t, "unmarshal envelope", err)
	}
	if _, ok := env["roster"]; ok {
		t.Fatal("status level must omit roster")
	}
	if env["summary"] == "" {
		t.Fatal("summary missing")
	}
}

func TestPackBoardEnvelopeFullIncludesBriefNotPrompt(t *testing.T) {
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
	if !strings.Contains(body, `"brief":"fix the bug"`) {
		t.Fatalf("full level should include brief: %s", body)
	}
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

func TestAttachRosterReservationsMapsPathsByJobID(t *testing.T) {
	roster := []api.BoardWorkerRosterEntry{
		{WorkerID: "job-a", AgentType: "implementer", Status: api.WorkerStatusRunning},
		{WorkerID: "job-b", AgentType: "implementer", Status: api.WorkerStatusRunning},
	}
	got := AttachRosterReservations(roster, []api.BoardReservationEntry{
		{Path: "pkg/foo.go", JobID: "job-a"},
		{Path: "pkg/bar.go", JobID: "job-a"},
	})
	if len(got[0].Reservations) != 2 || got[0].Reservations[0] != "pkg/foo.go" {
		t.Fatalf("job-a reservations = %#v", got[0].Reservations)
	}
	if len(got[1].Reservations) != 0 {
		t.Fatalf("job-b reservations = %#v", got[1].Reservations)
	}
}

func TestBuildBoardViewRosterIncludesReservations(t *testing.T) {
	snap := fixtureSnapshot()
	snap.Workers = &api.BoardWorkersSlice{
		"tasks": []api.WorkerTask{{
			ID: "job-a", AgentType: "implementer", Status: api.WorkerStatusRunning,
		}},
		"active_reservations": []api.BoardReservationEntry{{
			Path: "shellsim/builtins.py", JobID: "job-a", LegLabel: "timer-leg",
		}},
	}
	snap.PackContentHash = PackContentHash(snap, time.Now().UTC())
	out := marshalBoardView(t, &snap, api.BoardDetailLevelCompact, time.Now().UTC())
	var env map[string]any
	if err := json.Unmarshal(out, &env); err != nil {
		testutil.FailErr(t, "unmarshal envelope", err)
	}
	roster, ok := env["roster"].([]any)
	if !ok || len(roster) != 1 {
		t.Fatalf("roster = %#v", env["roster"])
	}
	row, _ := roster[0].(map[string]any)
	res, ok := row["reservations"].([]any)
	if !ok || len(res) != 1 || res[0] != "shellsim/builtins.py" {
		t.Fatalf("reservations = %#v", row["reservations"])
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
				{ID: "a", Status: api.WorkerStatusComplete, MergeStatus: api.WorkerMergeStatusPending},
				{ID: "b", Status: api.WorkerStatusComplete, MergeStatus: api.WorkerMergeStatusPending},
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
