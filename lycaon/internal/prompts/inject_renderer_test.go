package prompts_test

import (
	"context"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/packboard"
	"github.com/lycaon/lycaon/internal/prompts"
	"github.com/lycaon/lycaon/internal/testutil"
)

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
	if !strings.Contains(out, "pack-board:v1") {
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
	if !strings.Contains(out, "internal/auth/**") {
		t.Fatalf("missing assignment copy in:\n%s", out)
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
