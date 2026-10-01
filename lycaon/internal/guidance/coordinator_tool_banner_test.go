package guidance

import (
	"context"
	"strconv"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/spawn"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/pkg/api"
)

func TestRenderTaskQueuedBanner(t *testing.T) {
	setTestRenderer(t)
	ctx := context.Background()
	note, err := RenderTaskQueuedBanner(ctx, TaskQueuedBannerOpts{
		AgentType:   "implementer",
		JobID:       "job-1",
		MaxInFlight: spawn.MaxInFlightTaskWorkers,
	})
	testutil.FailErr(t, "RenderTaskQueuedBanner failed", err)
	want := strconv.Itoa(spawn.MaxInFlightTaskWorkers)
	if !strings.Contains(note, want) || !strings.Contains(note, "BANNER_TASK_QUEUED") {
		t.Fatalf("note = %q", note)
	}
	if !strings.Contains(note, "implementer") || !strings.Contains(note, "job-1") {
		t.Fatalf("note = %q", note)
	}
}

func TestRenderWorkerInFlightRoster(t *testing.T) {
	setTestRenderer(t)
	ctx := context.Background()
	note, err := RenderWorkerInFlightRoster(ctx, BuildWorkerRosterLines([]api.WorkerTask{
		{ID: "8a3f12ab-cdef", AgentType: "implementer", Status: api.WorkerStatusPending, Scope: &api.TaskScope{Mode: api.TaskScopeModeWrite, Paths: []string{"internal/auth/**"}}},
		{ID: "9c12dead-beef", AgentType: "path-explorer", Status: api.WorkerStatusRunning},
	}))
	testutil.FailErr(t, "RenderWorkerInFlightRoster failed", err)
	for _, want := range []string{"8a3f", "implementer", "9c12", "path-explorer", "write · focus: internal/auth/**", "batch dispatch", "remaining capacity", "BANNER_WORKER_INFLIGHT_ROSTER"} {
		if !strings.Contains(note, want) {
			t.Fatalf("missing %q in note: %q", want, note)
		}
	}
}

func TestRenderSynthesizedSurveyPreambleRepoResearcher(t *testing.T) {
	setTestRenderer(t)
	out, err := RenderSynthesizedSurveyPreamble(context.Background(), "repo-researcher")
	testutil.FailErr(t, "RenderSynthesizedSurveyPreamble failed", err)
	if strings.Contains(out, "GameFiles") {
		t.Fatalf("repo-researcher preamble must not mention GameFiles: %q", out)
	}
	if !strings.Contains(out, "Lockfile") {
		t.Fatalf("preamble = %q", out)
	}
}

func TestRosterJobIDPrefix(t *testing.T) {
	if got := RosterJobIDPrefix("8a3f12ab-cdef"); got != "8a3f…" {
		t.Fatalf("got %q", got)
	}
}

func TestRenderTakeBranchOverlapBanner(t *testing.T) {
	setTestRenderer(t)
	ctx := context.Background()
	note, err := RenderTakeBranchOverlapBanner(ctx, `{"job_id":"job-a","overlap_job_ids":["job-sibling"],"applied":["shared.go"],"paths":["shared.go"]}`)
	testutil.FailErr(t, "RenderTakeBranchOverlapBanner failed", err)
	for _, want := range []string{"shared.go", "job-sibling", "job-a", "BANNER_TAKE_BRANCH_OVERLAP"} {
		if !strings.Contains(note, want) {
			t.Fatalf("missing %q in note: %q", want, note)
		}
	}
}

func TestTakeBranchOverlapUsesStructuredConflictState(t *testing.T) {
	for _, raw := range []string{
		`{"job_id":"job-a","overlap_job_ids":["job-b"],"paths":["clean.go","conflict.go"],"conflicts":[{"path":"conflict.go"}]}`,
		`{"job_id":"job-a","overlap_job_ids":["job-b"],"paths":["clean.go","conflict.go"],"path_status":[{"path":"conflict.go","status":"conflict"}]}`,
	} {
		result, ok := parseWorkerMergeResultOutput(raw)
		if !ok {
			t.Fatal("parse overlap fixture")
		}
		data := takeBranchOverlapContext(result)
		paths := data["paths"].([]string)
		if data["clean_preview"] != false || len(paths) != 1 || paths[0] != "conflict.go" {
			t.Fatalf("conflicting result was presented as clean or claimed extra conflicts: %+v", data)
		}
	}
	result, ok := parseWorkerMergeResultOutput(`{"job_id":"job-a","overlap_job_ids":["job-b"],"clean_paths":["shared.go"]}`)
	if !ok {
		t.Fatal("parse clean fixture")
	}
	data := takeBranchOverlapContext(result)
	if data["clean_preview"] != true || len(data["paths"].([]string)) != 1 {
		t.Fatalf("clean result lost its paths: %+v", data)
	}
}

func TestRenderTakeBranchOverlapBannerSkipsWhenNoOverlap(t *testing.T) {
	setTestRenderer(t)
	note, err := RenderTakeBranchOverlapBanner(context.Background(), `{"overlap_job_ids":[],"applied":["a.go"]}`)
	testutil.FailErr(t, "RenderTakeBranchOverlapBanner failed", err)
	if note != "" {
		t.Fatalf("note = %q want empty", note)
	}
}

func TestRenderTakeBranchOverlapBannerParsesPayloadBeforeReject(t *testing.T) {
	setTestRenderer(t)
	ctx := context.Background()
	payload := `{"job_id":"job-a","overlap_job_ids":["job-b"],"clean_paths":["shared.go"],"paths":["shared.go"]}
Rejected: Overlay job-a cannot integrate
Code: OVERLAY_PROMOTE_CONFLICT`
	note, err := RenderTakeBranchOverlapBanner(ctx, payload)
	testutil.FailErr(t, "RenderTakeBranchOverlapBanner failed", err)
	for _, want := range []string{"shared.go", "job-b", "BANNER_TAKE_BRANCH_OVERLAP"} {
		if !strings.Contains(note, want) {
			t.Fatalf("note = %q want %q", note, want)
		}
	}
}

func TestRenderPromoteConflictDigestBanner(t *testing.T) {
	setTestRenderer(t)
	ctx := context.Background()
	payload := `{"job_id":"job-a","conflict_digest":[{"path":"a.go","summary":["L1-2: primary: p | branch: b"]}]}`
	note, err := RenderPromoteConflictDigestBanner(ctx, payload)
	testutil.FailErr(t, "RenderPromoteConflictDigestBanner failed", err)
	if !strings.Contains(note, "L1-2") || !strings.Contains(note, "BANNER_PROMOTE_CONFLICT_DIGEST") {
		t.Fatalf("note = %q", note)
	}
}

func TestRenderPromotePartialPathBanner(t *testing.T) {
	setTestRenderer(t)
	ctx := context.Background()
	payload := `{"job_id":"job-mix","clean_paths":["util.go"],"path_status":[{"path":"util.go","status":"clean"},{"path":"router.go","status":"conflict","hunk_count":2}],"spill_path":"promote-spills/job-mix.json"}`
	note, err := RenderPromotePartialPathBanner(ctx, payload)
	testutil.FailErr(t, "RenderPromotePartialPathBanner failed", err)
	for _, want := range []string{"job-mix", "util.go", "router.go", "promote-spills/job-mix.json", "BANNER_PROMOTE_PARTIAL_PATH"} {
		if !strings.Contains(note, want) {
			t.Fatalf("missing %q in note: %q", want, note)
		}
	}
}

func TestRenderPromotePartialPathBannerSkipsCleanOnly(t *testing.T) {
	setTestRenderer(t)
	note, err := RenderPromotePartialPathBanner(context.Background(), `{"job_id":"job-a","clean_paths":["a.go"],"path_status":[{"path":"a.go","status":"clean"}]}`)
	testutil.FailErr(t, "RenderPromotePartialPathBanner failed", err)
	if note != "" {
		t.Fatalf("note = %q want empty", note)
	}
}

func TestRenderPromoteOverlayBodyBanner(t *testing.T) {
	setTestRenderer(t)
	ctx := context.Background()
	payload := `{"job_id":"job-tests","overlap_job_ids":["job-b"],"conflicts":[{"path":"tests/test_core_utils.py","reason":"three_way_unresolved"}],"path_status":[{"path":"tests/test_core_utils.py","status":"conflict","hunk_count":2}],"spill_path":"promote-spills/job-tests.json","overlay_intent":{"summary":"131 pytest tests"}}`
	note, err := RenderPromoteOverlayBodyBanner(ctx, payload)
	testutil.FailErr(t, "RenderPromoteOverlayBodyBanner failed", err)
	for _, want := range []string{
		"tests/test_core_utils.py",
		"promote_overlay",
		"keep_both",
		"drop",
		"reject_overlay",
		"BANNER_PROMOTE_OVERLAY_BODY",
		"primary",
	} {
		if !strings.Contains(note, want) {
			t.Fatalf("missing %q in note: %q", want, note)
		}
	}
}

func TestRenderPromoteOverlayBodyBannerSkipsCleanOnly(t *testing.T) {
	setTestRenderer(t)
	note, err := RenderPromoteOverlayBodyBanner(context.Background(), `{"job_id":"job-a","clean_paths":["a.go"]}`)
	testutil.FailErr(t, "RenderPromoteOverlayBodyBanner failed", err)
	if note != "" {
		t.Fatalf("note = %q want empty", note)
	}
}

func TestRenderPromoteHighConflictBanner(t *testing.T) {
	setTestRenderer(t)
	ctx := context.Background()
	payload := `{"job_id":"job-a","path_status":[{"path":"builtins.py","status":"conflict","hunk_count":84}],"spill_path":"promote-spills/job-a.json"}`
	note, err := RenderPromoteHighConflictBanner(ctx, payload)
	testutil.FailErr(t, "RenderPromoteHighConflictBanner failed", err)
	for _, want := range []string{"builtins.py", "84", "BANNER_PROMOTE_HIGH_CONFLICT", "ready_resolutions", "promote_overlay"} {
		if !strings.Contains(note, want) {
			t.Fatalf("note = %q want %q", note, want)
		}
	}
}

func TestRenderPromoteHunkPickBannerOverlappingEdit(t *testing.T) {
	setTestRenderer(t)
	ctx := context.Background()
	payload := `{"job_id":"job-a","path_status":[{"path":"state.py","status":"conflict","hunk_count":3,"conflict_tier":"overlapping_edit"}]}`
	note, err := RenderPromoteHunkPickBanner(ctx, payload)
	testutil.FailErr(t, "RenderPromoteHunkPickBanner failed", err)
	for _, want := range []string{"back-to-front", "BANNER_PROMOTE_HUNK_PICK", "state.py"} {
		if !strings.Contains(note, want) {
			t.Fatalf("note = %q want %q", note, want)
		}
	}
}

func TestRenderPromoteOrderBannerCleanIfFirst(t *testing.T) {
	setTestRenderer(t)
	ctx := context.Background()
	payload := `{"job_id":"job-a","promote_order":"clean_if_first","blocked_by":["job-b"],"path_status":[{"path":"shared.go","promote_order":"clean_if_first","promote_order_note":"promote before job-b"}]}`
	note, err := RenderPromoteOrderBanner(ctx, payload)
	testutil.FailErr(t, "RenderPromoteOrderBanner failed", err)
	for _, want := range []string{"clean_if_first", "job-b", "BANNER_PROMOTE_ORDER"} {
		if !strings.Contains(note, want) {
			t.Fatalf("note = %q want %q", note, want)
		}
	}
}

func TestRenderPromoteSpillFirstBanner(t *testing.T) {
	setTestRenderer(t)
	ctx := context.Background()
	payload := `{"job_id":"job-a","spill_path":"promote-spills/job-a.json","path_status":[{"path":"prompt.py","status":"conflict","hunk_count":23,"conflict_tier":"line_shift"}]}`
	note, err := RenderPromoteSpillFirstBanner(ctx, payload)
	testutil.FailErr(t, "RenderPromoteSpillFirstBanner failed", err)
	for _, want := range []string{"spill_path", "read()", "BANNER_PROMOTE_SPILL_FIRST", "primary"} {
		if want == "spill_path" {
			if !strings.Contains(note, "promote-spills") && !strings.Contains(note, "spill_path") {
				t.Fatalf("note = %q want spill path hint", note)
			}
			continue
		}
		if !strings.Contains(note, want) {
			t.Fatalf("note = %q want %q", note, want)
		}
	}
}

func TestRenderOverlayMergePlanBanner(t *testing.T) {
	setTestRenderer(t)
	ctx := context.Background()
	payload := `{"overlay_merge_plan":{"pending_count":2,"promote_sequence":["job-a","job-b"],"shared_paths":[{"path":"shared.go","job_ids":["job-a","job-b"]}]}}`
	note, err := RenderOverlayMergePlanBanner(ctx, payload)
	testutil.FailErr(t, "RenderOverlayMergePlanBanner failed", err)
	for _, want := range []string{"job-a", "job-b", "shared.go", "BANNER_OVERLAY_MERGE_PLAN"} {
		if !strings.Contains(note, want) {
			t.Fatalf("note = %q want %q", note, want)
		}
	}
}

func TestRenderPromoteScopedHunksBanner(t *testing.T) {
	setTestRenderer(t)
	ctx := context.Background()
	payload := `{"job_id":"job-a","paths":["a.go"],"conflicts":[{"path":"a.go","hunks":[{"start_line":1}]}],"path_status":[{"path":"a.go","status":"conflict","hunk_count":3}]}`
	note, err := RenderPromoteScopedHunksBanner(ctx, payload)
	testutil.FailErr(t, "RenderPromoteScopedHunksBanner failed", err)
	for _, want := range []string{"a.go", "BANNER_PROMOTE_SCOPED_HUNKS", "back-to-front"} {
		if !strings.Contains(note, want) {
			t.Fatalf("note = %q want %q", note, want)
		}
	}
}

func TestPromoteHunkAdviceUsesEachPathsMeasuredTierAndCount(t *testing.T) {
	setTestRenderer(t)
	payload := `{"job_id":"job-a","path_status":[{"path":"small-overlap","status":"conflict","hunk_count":3,"conflict_tier":"overlapping_edit"},{"path":"large-shift","status":"conflict","hunk_count":84,"conflict_tier":"line_shift"},{"path":"small-shift","status":"conflict","hunk_count":2,"conflict_tier":"line_shift"}]}`
	pick, err := RenderPromoteHunkPickBanner(t.Context(), payload)
	testutil.FailErr(t, "render mixed-path hunk advice", err)
	if !strings.Contains(pick, "small-overlap") || strings.Contains(pick, "large-shift") || strings.Contains(pick, "small-shift") || !strings.Contains(pick, "end_line") {
		t.Fatalf("incorrect per-path hunk advice: %s", pick)
	}
	high, err := RenderPromoteHighConflictBanner(t.Context(), payload)
	testutil.FailErr(t, "render mixed-path high-count advice", err)
	if !strings.Contains(high, "large-shift") || strings.Contains(high, "small-overlap") || strings.Contains(high, "small-shift") {
		t.Fatalf("incorrect high-count paths: %s", high)
	}
}
