package prompts_test

import (
	"context"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/prompts"
	"github.com/lycaon/lycaon/internal/testutil"
)

func surfaceCardVarsMap(card prompts.SurfaceCardVars, mode string) map[string]any {
	return map[string]any{
		"surface_card_label":     card.Label,
		"surface_card_rule":      card.Rule,
		"surface_publishes_text": card.PublishesText,
		"card_may_edit":          card.MayEdit,
		"card_may_run":           card.MayRun,
		"card_may_dispatch":      card.MayDispatch,
		"card_edit_deferred":     card.EditDeferred,
		"card_only_tools":        card.OnlyTools,
		"execution_mode":         mode,
	}
}

// renderSurfaceCard runs the bundled partial over the derived facts, so these
// tests cover the whole chain the model sees.
func renderSurfaceCard(t *testing.T, label, rule, mode string, sticky, deferred []string) string {
	t.Helper()
	vars := surfaceCardVarsMap(prompts.CoordinatorSurfaceCardVars(prompts.SurfaceCard{Label: label, Rule: rule}, sticky, deferred, true), mode)
	engine := prompts.NewFileTemplateEngineLayers(prompts.PromptLayers{})
	out, err := engine.Render(context.Background(), "partials/coordinator-surface-card.md", vars)
	testutil.FailErr(t, "render surface card partial", err)
	return out
}

func TestSurfaceCardParkAndWrapup(t *testing.T) {
	park := renderSurfaceCard(t, "waiting on workers", "", "orchestrate",
		[]string{"recall", "wait", "worker_cancel", "answer_decision", "update_progress", "ask_user"}, nil)
	for _, want := range []string{"waiting on workers", "ask_user"} {
		if !strings.Contains(park, want) {
			t.Fatalf("park card missing %q: %q", want, park)
		}
	}
	if strings.Contains(park, "You may edit") {
		t.Fatalf("park card must not offer writes: %q", park)
	}

	_ = renderSurfaceCard(t, "wrapup", "One user report from receipts in this chat.", "wrapup",
		[]string{"recall", "read", "grep", "summarize", "render_view", "update_progress", "request_tools"}, nil)

	inv := renderSurfaceCard(t, "investigate", "Empty assistant text while tools run.", "investigate",
		[]string{"recall", "restore_version", "task"},
		[]string{"command", "verify"})
	for _, want := range []string{"Empty assistant text", "edit", "dispatch"} {
		if !strings.Contains(inv, want) {
			t.Fatalf("investigate card missing %q: %q", want, inv)
		}
	}
}

func TestSurfaceCardTracksRoster(t *testing.T) {
	deferredWrite := renderSurfaceCard(t, "investigate", "Empty assistant text while tools run.", "investigate",
		[]string{"recall"},
		[]string{"restore_version", "command"})
	if strings.Contains(deferredWrite, "You may edit") {
		t.Fatalf("card claimed inline edit while mutations are deferred: %q", deferredWrite)
	}
	// The model describes what it needs; the card never teaches catalog names.
	if strings.Contains(deferredWrite, "request_tools") || strings.Contains(deferredWrite, "`command`") {
		t.Fatalf("card must not enumerate loadable names: %q", deferredWrite)
	}

	stickyWrite := renderSurfaceCard(t, "investigate", "Empty assistant text while tools run.", "investigate",
		[]string{"recall", "restore_version"}, []string{"command"})
	if !strings.Contains(stickyWrite, "You may edit inline") {
		t.Fatalf("card must offer inline edit when restoration is sticky: %q", stickyWrite)
	}

	noDeferred := renderSurfaceCard(t, "investigate", "Empty assistant text while tools run.", "investigate",
		[]string{"recall", "restore_version"}, nil)
	if strings.Contains(noDeferred, "request_tools") {
		t.Fatalf("card must not offer request_tools with an empty deferred roster: %q", noDeferred)
	}
}

// A surface without a card label renders no banner, whatever its
// execution-mode family.
func TestSurfaceCardWithoutLabelRendersNothing(t *testing.T) {
	for _, family := range []string{"", "investigate", "orchestrate", "wrapup"} {
		if out := renderSurfaceCard(t, "", "", family, []string{"recall", "task"}, nil); strings.TrimSpace(out) != "" {
			t.Fatalf("family %q without a label rendered %q", family, out)
		}
	}
}

func TestSurfaceCardCommandEffects(t *testing.T) {
	for _, mutates := range []bool{false, true} {
		immediate := prompts.CoordinatorSurfaceCardVars(prompts.SurfaceCard{}, []string{"command"}, nil, mutates)
		if immediate.MayEdit != mutates || immediate.MayRun != mutates || immediate.EditDeferred {
			t.Fatalf("immediate command mutation=%t: %+v", mutates, immediate)
		}
		deferred := prompts.CoordinatorSurfaceCardVars(prompts.SurfaceCard{}, nil, []string{"command"}, mutates)
		if deferred.MayEdit || deferred.MayRun || deferred.EditDeferred != mutates {
			t.Fatalf("deferred command mutation=%t: %+v", mutates, deferred)
		}
	}
}
