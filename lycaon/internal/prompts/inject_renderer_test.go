package prompts_test

import (
	"context"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/packboard"
	"github.com/lycaon/lycaon/internal/prompts"
	"github.com/lycaon/lycaon/internal/testutil"
)

func TestInjectRenderer_RenderActiveWorkflow(t *testing.T) {
	engine := prompts.NewFileTemplateEngineLayers(prompts.PromptLayers{})
	renderer := prompts.NewInjectRenderer(engine)
	out, err := renderer.Render(context.Background(), "active-workflow", map[string]any{
		"workflow_id":       "default-pipeline",
		"current_phase":     "research",
		"failed_leaves":     []string{"topology_stage_complete"},
		"complete_when":     "gates_satisfied",
		"topology":          "default-pipeline",
		"topology_phase_id": "research",
		"phase_index":       1,
		"phase_total":       2,
		"advance_authority": "coordinator",
		"next_phase":        "expand",
		"current_phase_gates": []map[string]any{
			{"id": "research_satisfied", "satisfied": false},
		},
		"phase_exit": map[string]any{
			"kind":              "proof",
			"advance_authority": "coordinator",
			"steps": []string{
				"Satisfy gate `research_satisfied` — it is not met yet.",
				"When gates pass, call `workflow_advance`.",
			},
		},
		"phases": []map[string]any{
			{"id": "research", "label": "research", "is_current": true},
			{"id": "expand", "label": "expand"},
		},
	})
	testutil.FailErr(t, "renderer.Render failed", err)
	for _, want := range []string{
		"<!-- lycaon-workflow-runtime:v1 -->",
		"default-pipeline",
		"You are at phase 1/2 — `research`",
		"failed_leaves",
		"topology_stage_complete",
		"[ ] `research_satisfied`",
		"### Phase exit",
		"call `workflow_advance`",
		"Next: `expand`",
	} {
		if !strings.Contains(out, want) {
			t.Fatalf("missing %q in:\n%s", want, out)
		}
	}
	if strings.Contains(out, "Advance: call") {
		t.Fatalf("must not use standalone Advance one-liner:\n%s", out)
	}
}

func TestInjectRenderer_RejectsPathEscape(t *testing.T) {
	engine := prompts.NewFileTemplateEngineLayers(prompts.PromptLayers{})
	renderer := prompts.NewInjectRenderer(engine)
	_, err := renderer.Render(context.Background(), "../agents/coordinator", nil)
	if err == nil || !strings.Contains(err.Error(), "invalid inject ref") {
		t.Fatalf("err = %v", err)
	}
}

func TestInjectRenderer_RenderBoardOrientationStub(t *testing.T) {
	engine := prompts.NewFileTemplateEngineLayers(prompts.PromptLayers{})
	renderer := prompts.NewInjectRenderer(engine)
	out, err := renderer.Render(context.Background(), "board-orientation", map[string]any{
		"pack_sentinel":       packboard.PackBoardSentinel,
		"now_line":            "Now: 2026-05-31 12:00 UTC",
		"include_scan_legend": true,
		"lines": []map[string]any{
			{"text": "Recon: go"},
		},
	})
	testutil.FailErr(t, "renderer.Render failed", err)
	if !strings.Contains(out, "<!-- lycaon-board-orientation:v1 -->") {
		t.Fatalf("missing board sentinel in:\n%s", out)
	}
	if !strings.Contains(out, "Pack board") || !strings.Contains(out, "pack-board:v1") {
		t.Fatalf("missing heading or pack-board envelope:\n%s", out)
	}
	if !strings.Contains(out, "per_scan") || !strings.Contains(out, "scan_pack") {
		t.Fatalf("board-orientation must include pack-board-legend partial:\n%s", out)
	}
}

func TestInjectRenderer_RenderWorkerTaskAssignmentStub(t *testing.T) {
	engine := prompts.NewFileTemplateEngineLayers(prompts.PromptLayers{})
	renderer := prompts.NewInjectRenderer(engine)
	out, err := renderer.Render(context.Background(), "worker-task-assignment", map[string]any{
		"user_task":   "Fix auth",
		"agent_type":  "implementer",
		"leg_brief":   "Implement login fix",
		"scope_mode":  "write",
		"scope_paths": []string{"internal/auth/**"},
	})
	testutil.FailErr(t, "renderer.Render failed", err)
	if !strings.Contains(out, "<!-- lycaon-worker-task-assignment:v1 -->") {
		t.Fatalf("missing worker-task-assignment sentinel in:\n%s", out)
	}
	if !strings.Contains(out, "Leg assignment") || !strings.Contains(out, "internal/auth/**") {
		t.Fatalf("missing assignment copy in:\n%s", out)
	}
}

func TestInjectRenderer_RenderWorkerTaskPreambleStub(t *testing.T) {
	engine := prompts.NewFileTemplateEngineLayers(prompts.PromptLayers{})
	renderer := prompts.NewInjectRenderer(engine)
	out, err := renderer.Render(context.Background(), "worker-task-preamble", map[string]any{
		"touch_paths": []string{"internal/**"},
		"scope_mode":  "write",
		"scope_paths": []string{"internal/auth/**"},
	})
	testutil.FailErr(t, "renderer.Render failed", err)
	if !strings.Contains(out, "<!-- lycaon-worker-task-preamble:v1 -->") {
		t.Fatalf("missing worker-task-preamble sentinel in:\n%s", out)
	}
	if !strings.Contains(out, "orientation for the project") || !strings.Contains(out, "obligation:") {
		t.Fatalf("missing preamble copy in:\n%s", out)
	}
	if strings.Contains(strings.ToLower(out), "shim") {
		t.Fatalf("worker scope copy must not authorize shims:\n%s", out)
	}
	if !strings.Contains(out, "suggested paths guide focus only") {
		t.Fatalf("worker task copy must keep suggested paths advisory:\n%s", out)
	}
}

func TestInjectRenderer_RenderWorkerLegStub(t *testing.T) {
	engine := prompts.NewFileTemplateEngineLayers(prompts.PromptLayers{})
	renderer := prompts.NewInjectRenderer(engine)
	out, err := renderer.Render(context.Background(), "worker-leg", map[string]any{
		"leg_id":      "leg-1",
		"workflow_id": "wf-1",
		"phase":       "implement",
		"checklist": []map[string]any{
			{"index": 1, "text": "run tests"},
		},
	})
	testutil.FailErr(t, "renderer.Render failed", err)
	if !strings.Contains(out, "<!-- lycaon-worker-leg:v1 -->") {
		t.Fatalf("missing worker sentinel in:\n%s", out)
	}
	if !strings.Contains(out, "leg-1") {
		t.Fatal("missing leg_id")
	}
}

func TestInjectRenderer_MissingTemplateFailClosed(t *testing.T) {
	engine := prompts.NewFileTemplateEngineLayers(prompts.PromptLayers{})
	renderer := prompts.NewInjectRenderer(engine)
	_, err := renderer.Render(context.Background(), "missing-inject", nil)
	if err == nil {
		t.Fatal("expected error for missing template")
	}
}
