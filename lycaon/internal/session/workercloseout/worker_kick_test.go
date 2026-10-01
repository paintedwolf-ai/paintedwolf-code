package workercloseout_test

import (
	"context"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/coordinator/anchor"
	"github.com/lycaon/lycaon/internal/prompts"
	"github.com/lycaon/lycaon/internal/session/workercloseout"
)

func TestWorkerKickMarkerFallbackCloseout(t *testing.T) {
	got := workercloseout.RenderWorkerKick(context.Background(), nil, anchor.InformRender(anchor.WorkerCloseout), nil)
	if got != "[host:worker-closeout]" {
		t.Fatalf("got = %q", got)
	}
}

func TestWorkerKickMarkerFallbackTrimIsMarkerOnly(t *testing.T) {
	got := workercloseout.RenderWorkerKick(context.Background(), nil, anchor.InformRender(anchor.WorkerSummaryTrim), map[string]any{
		"max_chars":    12000,
		"actual_chars": 21000,
	})
	if got != "[host:worker-summary-trim]" {
		t.Fatalf("got = %q want marker-only fallback", got)
	}
}

func TestWorkerKickRenderedCloseoutUsesPartial(t *testing.T) {
	engine := prompts.NewFileTemplateEngineLayers(prompts.PromptLayers{})
	render := func(ctx context.Context, kickID string, data map[string]any) (string, error) {
		return engine.RenderKick(ctx, kickID, data)
	}
	got := workercloseout.RenderWorkerKick(context.Background(), render, anchor.InformRender(anchor.WorkerCloseout), nil)
	if !strings.Contains(got, "[host:worker-closeout]") {
		t.Fatalf("got = %q", got)
	}
	if !strings.Contains(got, "complete_leg") {
		t.Fatalf("got = %q want complete_leg closeout partial", got)
	}
}

func TestWorkerKickRenderedCloseoutLLMTimeoutNamesDiscard(t *testing.T) {
	engine := prompts.NewFileTemplateEngineLayers(prompts.PromptLayers{})
	render := func(ctx context.Context, kickID string, data map[string]any) (string, error) {
		return engine.RenderKick(ctx, kickID, data)
	}
	got := workercloseout.RenderWorkerKick(context.Background(), render, anchor.InformRender(anchor.WorkerCloseout), map[string]any{
		"reason_text": "the LLM turn time limit was reached",
		"llm_timeout": true,
	})
	for _, want := range []string{
		"the LLM turn time limit was reached",
		"discarded",
		"append: true",
		"Do not paste file or code content",
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("timeout closeout missing %q: %q", want, got)
		}
	}
}

func TestWorkerKickRenderedCloseoutWithoutTimeoutOmitsDiscard(t *testing.T) {
	engine := prompts.NewFileTemplateEngineLayers(prompts.PromptLayers{})
	render := func(ctx context.Context, kickID string, data map[string]any) (string, error) {
		return engine.RenderKick(ctx, kickID, data)
	}
	got := workercloseout.RenderWorkerKick(context.Background(), render, anchor.InformRender(anchor.WorkerCloseout), map[string]any{
		"reason_text": "the per-turn tool-iteration limit was reached",
		"llm_timeout": false,
	})
	if strings.Contains(got, "discarded — nothing") {
		t.Fatalf("non-timeout closeout must not claim discarded work: %q", got)
	}
	if !strings.Contains(got, "Do not paste file or code content") {
		t.Fatalf("closeout missing content ban: %q", got)
	}
}

func TestWorkerKickRenderedIterationsLowAllowsTools(t *testing.T) {
	engine := prompts.NewFileTemplateEngineLayers(prompts.PromptLayers{})
	render := func(ctx context.Context, kickID string, data map[string]any) (string, error) {
		return engine.RenderKick(ctx, kickID, data)
	}
	got := workercloseout.RenderWorkerKick(context.Background(), render, anchor.InformRender(anchor.WorkerIterationsLow), map[string]any{
		"remaining": 10, "max_tool_loops": 40, "at_host_max": false, "request_open": false,
	})
	if !strings.Contains(got, "[host:worker-iterations-low]") {
		t.Fatalf("got = %q", got)
	}
	if !strings.Contains(got, "10 tool rounds remain") {
		t.Fatalf("got = %q want remaining count", got)
	}
	if strings.Contains(got, "Stop calling tools") {
		t.Fatalf("iterations-low kick must allow tools: %q", got)
	}
}

// The runway notice offers request_budget only when the worker can still ask.
func TestWorkerKickRenderedIterationsLowOffersTheAskWhenItCanRise(t *testing.T) {
	engine := prompts.NewFileTemplateEngineLayers(prompts.PromptLayers{})
	render := func(ctx context.Context, kickID string, data map[string]any) (string, error) {
		return engine.RenderKick(ctx, kickID, data)
	}
	renderWith := func(atHostMax, requestOpen bool) string {
		return workercloseout.RenderWorkerKick(context.Background(), render, anchor.InformRender(anchor.WorkerIterationsLow), map[string]any{
			"remaining": 5, "max_tool_loops": 20, "at_host_max": atHostMax, "request_open": requestOpen,
		})
	}
	if got := renderWith(false, false); !strings.Contains(got, "call `request_budget`") {
		t.Fatalf("a worker below the host maximum must be offered the ask: %q", got)
	}
	if got := renderWith(true, false); strings.Contains(got, "call `request_budget`") || !strings.Contains(got, "cannot rise") {
		t.Fatalf("a worker at the host maximum must not be offered the ask: %q", got)
	}
	if got := renderWith(false, true); strings.Contains(got, "call `request_budget`") || !strings.Contains(got, "with the coordinator") {
		t.Fatalf("a worker with an open request must not be told to ask again: %q", got)
	}
}

func TestWorkerKickRenderedGroundingAllowsTools(t *testing.T) {
	engine := prompts.NewFileTemplateEngineLayers(prompts.PromptLayers{})
	render := func(ctx context.Context, kickID string, data map[string]any) (string, error) {
		return engine.RenderKick(ctx, kickID, data)
	}
	got := workercloseout.RenderWorkerKick(context.Background(), render, anchor.InformRender(anchor.WorkerCitationGrounding), map[string]any{
		"attempt":      1,
		"max_attempts": 2,
	})
	if !strings.Contains(got, "[host:worker-citation-grounding]") {
		t.Fatalf("got = %q", got)
	}
	if strings.Contains(got, "Stop calling tools") {
		t.Fatalf("grounding kick must allow tools: %q", got)
	}
	if !strings.Contains(got, "findings") || !strings.Contains(got, "kind#n") {
		t.Fatalf("got = %q want typed findings/handle guidance", got)
	}
}

func TestWorkerKickRenderedCoordinatorCitationGrounding(t *testing.T) {
	engine := prompts.NewFileTemplateEngineLayers(prompts.PromptLayers{})
	render := func(ctx context.Context, kickID string, data map[string]any) (string, error) {
		return engine.RenderKick(ctx, strings.TrimSpace(kickID), data)
	}
	const draft = "Architecture uses two binaries over HTTP + SSE."
	got := workercloseout.RenderWorkerKick(context.Background(), render, anchor.InformRender(anchor.CoordinatorCitationGrounding), map[string]any{
		"attempt":           2,
		"max_attempts":      3,
		"drafted_synthesis": draft,
	})
	if got == "[host:coordinator-citation-grounding]" {
		t.Fatal("RenderHostKick fell back to marker-only — TemplateRef did not resolve coordinator-citation-grounding.md")
	}
	for _, want := range []string{
		"[host:coordinator-citation-grounding]",
		"Emit only the citations fence",
		"Attempt 2/3",
		draft,
		"Pinned body:",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("missing %q in:\n%s", want, got)
		}
	}
}

func TestWorkerKickRenderedTrimUsesPartial(t *testing.T) {
	engine := prompts.NewFileTemplateEngineLayers(prompts.PromptLayers{})
	render := func(ctx context.Context, kickID string, data map[string]any) (string, error) {
		return engine.RenderKick(ctx, kickID, data)
	}
	got := workercloseout.RenderWorkerKick(context.Background(), render, anchor.InformRender(anchor.WorkerSummaryTrim), map[string]any{
		"max_chars":    12000,
		"actual_chars": 21000,
	})
	if !strings.Contains(got, "21000") || !strings.Contains(got, "12000") {
		t.Fatalf("got = %q", got)
	}
	if !strings.Contains(got, "complete_leg") {
		t.Fatalf("got = %q want complete_leg closeout partial", got)
	}
}
