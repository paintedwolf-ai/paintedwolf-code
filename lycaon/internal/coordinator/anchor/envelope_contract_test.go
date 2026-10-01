package anchor_test

import (
	"context"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/lycaon/lycaon/config"
	"github.com/lycaon/lycaon/internal/configlayout"
	"github.com/lycaon/lycaon/internal/coordinator/anchor"
	"github.com/lycaon/lycaon/internal/coordinator/kick"
	"github.com/lycaon/lycaon/internal/prompts"
	"github.com/lycaon/lycaon/internal/testutil"
	"gopkg.in/yaml.v3"
)

func loadTestRegistry(t *testing.T) *anchor.Registry {
	t.Helper()
	reg, err := anchor.LoadRegistryFromConfigRoot()
	testutil.FailErr(t, "LoadRegistryFromConfigRoot", err)
	anchor.SetDefaultRegistry(reg)
	t.Cleanup(func() { anchor.SetDefaultRegistry(nil) })
	return reg
}

func TestKickTemplateIDRoundTrip(t *testing.T) {
	loadTestRegistry(t)
	for _, id := range []anchor.ID{
		anchor.GateBlocked, anchor.ComposeDone, anchor.LegFinished, anchor.WorkerTaskFinished,
		anchor.PhaseAdvanced, anchor.WaitTimerFired, anchor.ScanFinished, anchor.ProcessFinished,
		anchor.WorkerBudgetRequested, anchor.FeedbackPending, anchor.FeedbackReceived,
		anchor.OverlayPromoteComplete, anchor.EditFollowUpRepeat,
	} {
		if got := anchor.MustAnchor(string(id)); got != id {
			t.Fatalf("MustAnchor(%q)=%q want %s", id, got, id)
		}
		tmpl := anchor.InformRender(id)
		if tmpl == "" {
			t.Fatalf("%s: empty InformRender", id)
		}
		parsed, ok := anchor.ParseID(tmpl)
		if !ok || parsed != id {
			t.Fatalf("ParseID(%q)=%q ok=%v want %s", tmpl, parsed, ok, id)
		}
	}
}

func TestBindingFilenameIsNotAnchorIdentity(t *testing.T) {
	loadTestRegistry(t)
	if _, ok := anchor.ParseID("leg-finished"); ok {
		t.Fatal("binding filenames must not resolve as anchor identities")
	}
}

func TestCatalogContainsKickMappedAnchors(t *testing.T) {
	raw, err := config.Read(config.AnchorCatalog)
	testutil.FailErr(t, "read catalog", err)
	var doc struct {
		Anchors []struct {
			ID string `yaml:"id"`
		} `yaml:"anchors"`
	}
	testutil.FailErr(t, "yaml", yaml.Unmarshal(raw, &doc))
	have := map[string]bool{}
	for _, a := range doc.Anchors {
		have[a.ID] = true
	}
	required := []anchor.ID{
		anchor.GateBlocked, anchor.ComposeDone, anchor.LegFinished, anchor.WorkerTaskFinished,
		anchor.PhaseAdvanced,
		anchor.ReviewLoopContinue, anchor.ReviewLoopDecide,
		anchor.FeedbackPending, anchor.FeedbackReceived, anchor.WorkerTaskStarted, anchor.WorkerLegStarted,
		anchor.WaitTimerFired, anchor.OverlayPromoteComplete, anchor.ScanFinished, anchor.ScanDelta, anchor.ProcessFinished,
		anchor.WorkerBudgetRequested, anchor.WorkerBudgetRaised, anchor.WorkerBudgetDeclined, anchor.EditFollowUpRepeat, anchor.CoordinatorCloseout,
		anchor.CoordinatorCitationGrounding, anchor.CoordinatorReportDocument, anchor.ProjectRootsChanged, anchor.ProjectRootDetachCanceled,
		anchor.WorkerCloseout, anchor.WorkerIterationsLow, anchor.WorkerCancelCloseout,
		anchor.WorkerSummaryTrim, anchor.WorkerCitationGrounding, anchor.LoopWake, anchor.BoardChanged,
		anchor.ProgressMissing, anchor.ProgressStale, anchor.PhaseExitRequired,
		anchor.AuthzSealFailed, anchor.TurnCloseout, anchor.TurnIterationsLow,
		anchor.TurnSpendRunwayLow, anchor.TurnSpendSoftStop, anchor.OutboundSecretWithheld,
		"tool.pre_invoke", "coordinator.post_turn",
	}
	for _, id := range required {
		if !have[string(id)] {
			t.Fatalf("catalog missing %s", id)
		}
	}
}

func TestGoldenKickRenderByteIdenticalViaEmit(t *testing.T) {
	reg := loadTestRegistry(t)
	engine := prompts.NewFileTemplateEngineLayers(prompts.PromptLayers{ModuleRoot: configlayout.FindModuleRoot()})
	kicks := &kick.KickEngine{}
	kicks.SetPromptEngine(engine)
	bus := anchor.NewBus(kicks)
	bus.SetRegistry(reg)

	goldenDir := filepath.Join("testdata", "golden", "kicks")
	update := os.Getenv("UPDATE_ANCHOR_KICK_GOLDEN") == "1"

	done := time.Now().UTC().Add(-2 * time.Minute)
	cases := []struct {
		name string
		id   anchor.ID
		env  anchor.Envelope
		live kick.CoordinatorKickRenderContext
	}{
		{name: "compose-done", id: anchor.ComposeDone},
		{
			name: "gate-blocked",
			id:   anchor.GateBlocked,
			live: kick.CoordinatorKickRenderContext{
				FailedLeaves: []string{"gate.evidence"},
				GateObligations: []kick.GateObligation{{
					ID: "gate.evidence", Purpose: "prove", Satisfy: []string{"x"}, Missing: []string{"x"},
				}},
			},
		},
		{
			name: "leg-finished",
			id:   anchor.LegFinished,
			env:  anchor.Envelope{CompletedAt: &done, WorkerDigest: "Host worker digest — agent=implementer status=complete"},
		},
		{
			name: "scan-finished",
			id:   anchor.ScanFinished,
			env: anchor.Envelope{
				ScanID: "scan-1", ScanStatus: "completed", ScanCategories: "secrets", ScanFindingsCount: 2,
			},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			bus.Emit(context.Background(), "sess-golden", tc.id, tc.env)
			id := kicks.TakePendingKickID("sess-golden")
			if id != anchor.InformRender(tc.id) {
				t.Fatalf("pending id = %q want %q", id, anchor.InformRender(tc.id))
			}
			text, lease, ok, _ := kicks.RenderPendingNudge(t.Context(), "sess-golden", tc.live)
			if !ok || strings.TrimSpace(text) == "" {
				t.Fatal("expected rendered nudge text")
			}
			kicks.AckPendingNudge("sess-golden", lease)
			bus.Emit(context.Background(), "sess-golden2", tc.id, tc.env)
			_ = kicks.TakePendingKickID("sess-golden2")
			text2, lease2, ok, _ := kicks.RenderPendingNudge(t.Context(), "sess-golden2", tc.live)
			if !ok {
				t.Fatal("second render missing")
			}
			kicks.AckPendingNudge("sess-golden2", lease2)
			if text != text2 {
				t.Fatalf("render drift:\n---\n%s\n---\n%s", text, text2)
			}
			goldenPath := filepath.Join(goldenDir, tc.name+".golden")
			if update {
				testutil.FailErr(t, "mkdir golden", os.MkdirAll(goldenDir, 0o755))
				testutil.FailErr(t, "write golden", os.WriteFile(goldenPath, []byte(text), 0o644))
				return
			}
			want, err := os.ReadFile(goldenPath)
			testutil.FailErr(t, "read golden "+tc.name, err)
			if string(want) != text {
				t.Fatalf("golden mismatch %s (UPDATE_ANCHOR_KICK_GOLDEN=1 to refresh):\n--- want ---\n%s\n--- got ---\n%s",
					tc.name, want, text)
			}
		})
	}
}

func TestKickTemplateReceivesStructuredContext(t *testing.T) {
	engine := prompts.NewFileTemplateEngineLayers(prompts.PromptLayers{ModuleRoot: configlayout.FindModuleRoot()})
	out, err := engine.RenderKick(t.Context(), "coordinator-gate-blocked", map[string]any{
		"gate_obligations": []map[string]any{{
			"id": "gate.evidence", "purpose": "prove", "missing": []string{"x"}, "satisfy": []string{"x"},
		}},
	})
	testutil.FailErr(t, "render structured kick", err)
	for _, want := range []string{"gate.evidence", "missing: x", "1. x"} {
		if !strings.Contains(out, want) {
			t.Fatalf("structured kick %q missing %q", out, want)
		}
	}
}

var pongoVarRe = regexp.MustCompile(`\{\{\s*([a-zA-Z_][a-zA-Z0-9_]*)`)

func TestKickTemplateVarsSubsetOfEnvelopeKeys(t *testing.T) {
	allowed := map[string]bool{
		"completed_ago": true, "worker_digest": true, "last_worker_decision_request": true,
		"evidence_digest": true, "command_completion": true, "command_refusal": true, "topology_output": true, "options_criterion": true,
		"fanout_plan": true, "max_fanout_legs": true, "batch_phase": true,
		"pending_overlay_jobs": true, "partial_worker_jobs": true, "promoted_paths": true,
		"advance_when_gate_met": true, "failed_leaves": true, "gate_obligations": true,
		"progress_open_items": true, "progress_closure_armed": true,
		"batches": true, "tools": true,
		"scan_id": true, "scan_status": true, "scan_categories": true, "scan_findings_count": true,
		"job_id": true, "tool_loops_used": true, "max_tool_loops": true, "remaining": true,
		"host_max_tool_loops": true, "budget_request_open": true, "requested_max": true,
		"requested_rounds": true, "remaining_work": true, "at_host_max": true, "request_open": true,
		"worker_budget_exhausted": true, "child_session_id": true, "suggested_resume_max": true,
		"workspace_path": true, "project_id": true, "config_root": true,
		"coordinator_brief": true, "pending_feedback": true, "current_phase": true,
		"folder": true, "roots": true, "active_root": true, "canceled": true, "root_id": true, "root_label": true,
		"path": true, "paths": true, "sessions": true, "overlays": true, "workers": true,
		"before_count": true, "after_count": true, "after_omitted_count": true, "added_count": true, "removed_count": true,
		"attempt": true, "max_attempts": true, "drafted_synthesis": true, "reason_text": true,
		"repair_observations": true, "retained_citations": true,
		"offenders_sample": true, "observed_handles_sample": true, "observed_paths_sample": true,
		// Report-document repair supplies the refusal's reason, the current document fence,
		// and whether the draft delivers its run's report.
		"rejection_reason": true, "offenders_omitted": true, "retained_document": true, "run_report": true,
		// QueueWorkerKick supplies these worker fields.
		"agent_type": true, "leg_id": true, "phase_id": true, "cancel_reason": true,
		"actual_chars": true, "max_chars": true,
		// Envelope.Vars keys declared on the post-turn / landing-advisory anchors.
		"tool": true, "pending": true, "workflow_id": true, "guidance": true, "llm_timeout": true,
		"held_call_handle": true, "held_call_tool": true, "held_call_outcome": true,
	}
	entries, err := config.List(config.PlatformGuidance)
	testutil.FailErr(t, "readdir kicks", err)
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".md") {
			continue
		}
		name := e.Name()
		if !strings.HasPrefix(name, "coordinator-") && !strings.HasPrefix(name, "worker-") && !strings.HasPrefix(name, "survey-") {
			continue
		}
		raw, err := config.Read(config.PlatformGuidance.Join(name))
		testutil.FailErr(t, "read "+name, err)
		for _, m := range pongoVarRe.FindAllStringSubmatch(string(raw), -1) {
			v := m[1]
			// Skip syntax names and local loop bindings.
			if len(v) <= 2 || v == "forloop" || v == "after" || v == "new_primary" ||
				v == "attempt" || v == "max_attempts" || v == "drafted_synthesis" || v == "reason_text" ||
				v == "id" || v == "include" || v == "endif" || v == "endfor" || v == "elif" || v == "else" {
				continue
			}
			if !allowed[v] {
				t.Errorf("%s references {{ %s }} not in Envelope/live allowlist", name, v)
			}
		}
	}
}
