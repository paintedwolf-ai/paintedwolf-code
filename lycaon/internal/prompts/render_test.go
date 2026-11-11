package prompts_test

import (
	"context"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/prompts"
	"github.com/lycaon/lycaon/internal/testutil"
)

func TestFileTemplateEngine_RenderGuidance(t *testing.T) {
	engine := prompts.NewFileTemplateEngineLayers(prompts.PromptLayers{})
	out, err := engine.RenderGuidance(context.Background(), "reject/_reject", map[string]any{
		"code": "TEST", "what": "blocked", "cause": "blocked", "why": "blocked", "fix": "retry", "instead": "retry", "category": "recoverable",
	})
	testutil.FailErr(t, "engine.RenderGuidance failed", err)
	if !strings.Contains(out, "TEST") || !strings.Contains(out, "Rejected:") {
		t.Fatalf("out = %q", out)
	}
}

func TestFileTemplateEngine_RenderKick(t *testing.T) {
	engine := prompts.NewFileTemplateEngineLayers(prompts.PromptLayers{})
	out, err := engine.RenderKick(context.Background(), "coordinator-gate-blocked", nil)
	testutil.FailErr(t, "engine.RenderKick failed", err)
	if !strings.Contains(out, "failed_leaves") {
		t.Fatalf("out = %q", out)
	}
}

func TestFileTemplateEngine_RenderInjectScanEphemeral(t *testing.T) {
	engine := prompts.NewFileTemplateEngineLayers(prompts.PromptLayers{})
	_, err := engine.RenderInject(context.Background(), "scan-guidance-ephemeral", map[string]any{
		"summaries": []map[string]any{{"code": "X", "message": "msg"}},
	})
	testutil.FailErr(t, "engine.RenderInject failed", err)
}

func TestRenderGuidance_RejectsPathEscape(t *testing.T) {
	engine := prompts.NewFileTemplateEngineLayers(prompts.PromptLayers{})
	_, err := engine.RenderGuidance(context.Background(), "../agents/coordinator", nil)
	if err == nil || !strings.Contains(err.Error(), "invalid template ref") {
		t.Fatalf("err = %v", err)
	}
}
