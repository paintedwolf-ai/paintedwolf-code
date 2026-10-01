package contract

import (
	"context"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/lycaon/lycaon/internal/prompts"
	"github.com/lycaon/lycaon/test/contract/internal/catalogfixture"
	contractcheck "github.com/lycaon/lycaon/test/contract/internal/check"
)

// TestKickTemplatesRenderWithKickEngineVars validates required rendered phrases.
func TestKickTemplatesRenderWithKickEngineVars(t *testing.T) {
	t.Parallel()
	catalogfixture.AssertNoKickConstFiles(t)
	reg := catalogfixture.LoadInformBindings(t)

	engine := prompts.NewFileTemplateEngineLayers(prompts.PromptLayers{})
	// Use every variable supplied by KickEngine.
	data := map[string]any{
		"batch_phase":          "integrate",
		"worker_digest":        "ENV report: 2 files changed, build green.",
		"topology_output":      "Recon pack: 3 risks surfaced.",
		"evidence_digest":      "## Evidence digest (curated)\n[grep#1] main.go:1 — package main\nselected/total: 1/1",
		"options_criterion":    "Prefer minimal long-term surface area.",
		"pending_overlay_jobs": []string{"job-a", "job-b"},
		"partial_worker_jobs":  []string{"job-partial"},
		"promoted_paths":       []string{"lycaon/internal/x.go", "lycaon/internal/y.go"},
		"completed_ago":        "2m ago",
		"scan_id":              "scan-1234",
		"scanner_id":           "fixture-scanner",
		"introduced_count":     1,
		"fixed_count":          1,
		"introduced":           []map[string]any{{"level": "high", "rule_id": "fixture-rule", "path": "main.go", "line": 4, "message": "fixture finding"}},
		"fixed":                []map[string]any{{"level": "high", "rule_id": "fixture-rule", "path": "fixed.go", "line": 8}},
		"batches":              6,
		"tools":                []string{"git_diff", "read"},
		"scan_status":          "complete",
		"scan_categories":      "sast, secret",
		"scan_findings_count":  7,
		"failed_leaves":        []string{"hitl_consulted:intake"},
		"gate_obligations": []map[string]any{{
			"id":      "hitl_consulted:intake",
			"purpose": "Plan intake is freeform must-consult.",
			"satisfy": []string{"Call ask_user with task-appropriate questions when no host card is pending."},
		}},
	}
	contractcheck.FailErr(t, "merge coordinator kick policy vars", prompts.MergeCoordinatorKickPolicyVars(data))

	for _, b := range reg.AllInform() {
		if b == nil || !b.IsInform() || !strings.HasPrefix(b.Render, "coordinator-") {
			continue
		}
		id := catalogfixture.BindingShortID(b.Render)
		required := append([]string(nil), b.Invariants.Required...)
		t.Run(id, func(t *testing.T) {
			t.Parallel()
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			out, err := engine.RenderKick(ctx, b.Render, data)
			if err != nil {
				t.Fatalf("RenderKick %s: %v", b.Render, err)
			}
			if strings.TrimSpace(out) == "" {
				t.Fatalf("%s rendered empty under maximal data set", b.Render)
			}
			if id == "scan-delta" && (!strings.Contains(out, "scan-1234") || !strings.Contains(out, "fixture-scanner")) {
				t.Fatalf("scan delta lost its scan or scanner identity: %s", out)
			}
			for _, must := range required {
				if strings.HasPrefix(must, "partials/") {
					continue
				}
				if !strings.Contains(out, must) {
					t.Fatalf("%s rendered output missing required substring %q\n--- rendered ---\n%s", b.Render, must, out)
				}
			}
		})
	}
}

func TestScheduledKickOmitsOverlayWithoutPendingOverlayJobs(t *testing.T) {
	t.Parallel()
	engine := prompts.NewFileTemplateEngineLayers(prompts.PromptLayers{})
	data := map[string]any{"batch_phase": "integrate"}
	contractcheck.FailErr(t, "merge coordinator kick policy vars", prompts.MergeCoordinatorKickPolicyVars(data))

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	out, err := engine.RenderKick(ctx, "coordinator-scheduled", data)
	contractcheck.FailErr(t, "RenderKick coordinator-scheduled", err)
	if strings.Contains(out, "promote_overlay") || strings.Contains(out, "preview_overlay") {
		t.Fatalf("scheduled kick must not mention overlay promote when pending_overlay_jobs unset\n--- rendered ---\n%s", out)
	}
	if strings.Contains(out, "Pending overlays") {
		t.Fatalf("scheduled kick must not list pending overlays when ledger empty\n--- rendered ---\n%s", out)
	}
	if !strings.Contains(out, "prose only") || !strings.Contains(out, "do **not** re-arm a timer wait") {
		t.Fatalf("scheduled kick must teach prose-finish instead of timer-only poll\n--- rendered ---\n%s", out)
	}
}

func TestScheduledKickClosedBatchOmitsResubscribe(t *testing.T) {
	t.Parallel()
	engine := prompts.NewFileTemplateEngineLayers(prompts.PromptLayers{})
	data := map[string]any{"batch_phase": "closed"}
	contractcheck.FailErr(t, "merge coordinator kick policy vars", prompts.MergeCoordinatorKickPolicyVars(data))

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	out, err := engine.RenderKick(ctx, "coordinator-scheduled", data)
	contractcheck.FailErr(t, "RenderKick coordinator-scheduled", err)
	if strings.Contains(out, "Re-subscribe") || strings.Contains(out, "Dispatch `task()`") {
		t.Fatalf("closed-batch scheduled kick must not re-subscribe or dispatch\n--- rendered ---\n%s", out)
	}
	if !strings.Contains(out, "Batch closed") {
		t.Fatalf("closed-batch scheduled kick must say batch closed\n--- rendered ---\n%s", out)
	}
}

func TestWorkerTaskFinishedKickOmitsOverlayWithoutPendingOverlayJobs(t *testing.T) {
	t.Parallel()
	engine := prompts.NewFileTemplateEngineLayers(prompts.PromptLayers{})
	data := map[string]any{}
	contractcheck.FailErr(t, "merge coordinator kick policy vars", prompts.MergeCoordinatorKickPolicyVars(data))

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	out, err := engine.RenderKick(ctx, "coordinator-worker-task-finished", data)
	contractcheck.FailErr(t, "RenderKick coordinator-worker-task-finished", err)
	if strings.Contains(out, "Write overlays") {
		t.Fatalf("worker-task-finished must not mention overlays when pending_overlay_jobs unset\n--- rendered ---\n%s", out)
	}
	if strings.Contains(out, "promote pending overlays") || strings.Contains(out, "promote_overlay") {
		t.Fatalf("worker-task-finished must not instruct promote when pending_overlay_jobs unset\n--- rendered ---\n%s", out)
	}
}

// Armed kicks require checklist reconciliation before dispatch.
func TestLegFinishKicksLeadWithReconcileWhenLatched(t *testing.T) {
	t.Parallel()
	engine := prompts.NewFileTemplateEngineLayers(prompts.PromptLayers{})

	render := func(t *testing.T, kickID string, latched bool) string {
		t.Helper()
		data := map[string]any{}
		if latched {
			data["progress_closure_armed"] = true
			data["progress_open_items"] = 3
		}
		contractcheck.FailErr(t, "merge coordinator kick policy vars", prompts.MergeCoordinatorKickPolicyVars(data))
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		out, err := engine.RenderKick(ctx, kickID, data)
		contractcheck.FailErr(t, "RenderKick "+kickID, err)
		return out
	}

	for _, kickID := range []string{"coordinator-worker-task-finished", "coordinator-leg-finished"} {
		t.Run(kickID, func(t *testing.T) {
			t.Parallel()
			latched := render(t, kickID, true)
			if !strings.Contains(latched, "update_progress") {
				t.Errorf("%s names a dispatch move with the checklist latched but never names "+
					"update_progress:\n--- rendered ---\n%s", kickID, latched)
			}
			if !strings.Contains(latched, "PROGRESS_ITEM_NOT_CLOSED") {
				t.Errorf("%s does not name the Code the coordinator is about to hit:"+
					"\n--- rendered ---\n%s", kickID, latched)
			}

			clear := render(t, kickID, false)
			if strings.Contains(clear, "PROGRESS_ITEM_NOT_CLOSED") {
				t.Errorf("%s warns about the closure latch when it is not armed:"+
					"\n--- rendered ---\n%s", kickID, clear)
			}
		})
	}
}

func TestLegFinishedKickSplitByAdvancePolicy(t *testing.T) {
	t.Parallel()
	engine := prompts.NewFileTemplateEngineLayers(prompts.PromptLayers{})

	render := func(advance string) string {
		data := map[string]any{"advance_when_gate_met": advance}
		contractcheck.FailErr(t, "merge coordinator kick policy vars", prompts.MergeCoordinatorKickPolicyVars(data))
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		out, err := engine.RenderKick(ctx, "coordinator-leg-finished", data)
		contractcheck.FailErr(t, "RenderKick coordinator-leg-finished", err)
		return out
	}

	coord := render("coordinator")
	if !strings.Contains(coord, "workflow_advance") {
		t.Fatalf("coordinator leg-finished missing workflow_advance:\n%s", coord)
	}
	if strings.Contains(coord, "No `workflow_advance` in build chat") {
		t.Fatalf("coordinator leg-finished must not include build-only copy:\n%s", coord)
	}

	auto := render("auto")
	if strings.Contains(auto, "Call `workflow_advance`") {
		t.Fatalf("build leg-finished must not instruct workflow_advance:\n%s", auto)
	}
	if !strings.Contains(auto, "No `workflow_advance` in build chat") {
		t.Fatalf("build leg-finished missing build guard copy:\n%s", auto)
	}
}

func TestCoordinatorOverlayPromptConditioning(t *testing.T) {
	t.Parallel()
	engine := prompts.NewFileTemplateEngineLayers(prompts.PromptLayers{})

	base := map[string]any{
		"max_in_flight":                32,
		"max_tool_loops":               40,
		"max_concurrent_tool_calls":    8,
		"pending_overlay_promote":      false,
		"execution_mode":               "orchestrate",
		"has_file_tools":               true,
		"can_spawn_implementer":        true,
		"can_spawn_web_research":       true,
		"root_count":                   1,
		"defer_until_user_reply_tools": []string{"pack_board"},
	}
	contractcheck.FailErr(t, "merge coordinator kick policy vars", prompts.MergeCoordinatorKickPolicyVars(base))

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	dispatch, err := engine.Render(ctx, "agents/coordinator-mode-implement-dispatch.md", base)
	contractcheck.FailErr(t, "render dispatch mode", err)
	if strings.Contains(dispatch, "per-overlay promote order") {
		t.Fatalf("dispatch must omit overlay promote order when pending_overlay_promote false\n---\n%s", dispatch)
	}
	if strings.Contains(dispatch, "without pending overlay") {
		t.Fatalf("dispatch must omit pending-overlay partial clause when pending_overlay_promote false\n---\n%s", dispatch)
	}

	pacing, err := engine.Render(ctx, "partials/coordinator-mode-orchestrate-pacing.md", base)
	contractcheck.FailErr(t, "render orchestrate pacing", err)
	if strings.Contains(pacing, "promote pending overlays") {
		t.Fatalf("orchestrate pacing must omit promote when pending_overlay_promote false\n---\n%s", pacing)
	}

	withOverlay := map[string]any{}
	for k, v := range base {
		withOverlay[k] = v
	}
	withOverlay["pending_overlay_promote"] = true
	withOverlay["partial_worker_jobs"] = []string{"job-partial"}

	dispatchPending, err := engine.Render(ctx, "agents/coordinator-mode-implement-dispatch.md", withOverlay)
	contractcheck.FailErr(t, "render dispatch with overlays", err)
	if !strings.Contains(dispatchPending, "per-overlay promote order") {
		t.Fatalf("dispatch must include overlay promote order when pending_overlay_promote true\n---\n%s", dispatchPending)
	}
	if !strings.Contains(dispatchPending, "job-partial") {
		t.Fatalf("dispatch must list partial_worker_jobs when set\n---\n%s", dispatchPending)
	}
}

// Kicks remain informative when event metadata is absent.
func TestKickTemplatesRenderWithMinimalDataAreNonEmpty(t *testing.T) {
	t.Parallel()
	ids, err := kickTemplateIDs()
	contractcheck.FailErr(t, "kickTemplateIDs", err)

	engine := prompts.NewFileTemplateEngineLayers(prompts.PromptLayers{})
	data := map[string]any{}
	contractcheck.FailErr(t, "merge coordinator kick policy vars", prompts.MergeCoordinatorKickPolicyVars(data))

	var empty []string
	for _, id := range ids {
		if !strings.HasPrefix(id, "coordinator-") {
			continue
		}
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		out, err := engine.RenderKick(ctx, id, data)
		cancel()
		if err != nil {
			t.Fatalf("RenderKick %s: %v", id, err)
		}
		if strings.TrimSpace(out) == "" {
			empty = append(empty, id)
		}
	}
	sort.Strings(empty)
	if len(empty) > 0 {
		t.Fatalf("kick templates that render empty under minimal data — every kick must produce baseline prose: %v", empty)
	}
}
