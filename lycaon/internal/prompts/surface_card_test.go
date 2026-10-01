package prompts_test

import (
	"context"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/prompts"
	"github.com/lycaon/lycaon/internal/testutil"
)

// renderSurfaceCard runs the bundled partial over the derived facts, so these
// tests cover the whole chain the model sees.
func renderSurfaceCard(t *testing.T, label, rule, mode string, sticky, deferred []string) string {
	t.Helper()
	vars := prompts.CoordinatorSurfaceCardVars(label, rule, sticky, deferred).TemplateVars()
	vars["execution_mode"] = mode
	engine := prompts.NewFileTemplateEngineLayers(prompts.PromptLayers{})
	out, err := engine.Render(context.Background(), "partials/coordinator-surface-card.md", vars)
	testutil.FailErr(t, "render surface card partial", err)
	return out
}

func TestSurfaceCardParkAndWrapup(t *testing.T) {
	park := renderSurfaceCard(t, "waiting on workers", "", "orchestrate",
		[]string{"recall", "wait", "worker_cancel", "answer_decision", "update_progress", "ask_user", "surface_note"}, nil)
	for _, want := range []string{"waiting on workers", "ask_user"} {
		if !strings.Contains(park, want) {
			t.Fatalf("park card missing %q: %q", want, park)
		}
	}
	if strings.Contains(park, "You may edit") {
		t.Fatalf("park card must not offer writes: %q", park)
	}

	wrap := renderSurfaceCard(t, "wrapup", "One user report from receipts in this chat.", "wrapup",
		[]string{"recall", "read", "grep", "summarize", "render_view", "update_progress", "request_tools"}, nil)
	for _, want := range []string{"wrapup", "No mutation"} {
		if !strings.Contains(wrap, want) {
			t.Fatalf("wrapup card missing %q: %q", want, wrap)
		}
	}

	inv := renderSurfaceCard(t, "investigate", "Empty assistant text while tools run.", "investigate",
		[]string{"recall", "read", "write", "edit", "replace_lines", "task"},
		[]string{"command", "verify", "delete"})
	for _, want := range []string{"investigate", "Empty assistant text", "edit", "dispatch"} {
		if !strings.Contains(inv, want) {
			t.Fatalf("investigate card missing %q: %q", want, inv)
		}
	}
}

func TestSurfaceCardTracksRoster(t *testing.T) {
	deferredWrite := renderSurfaceCard(t, "investigate", "Empty assistant text while tools run.", "investigate",
		[]string{"recall", "read", "grep"},
		[]string{"write", "edit", "command"})
	if strings.Contains(deferredWrite, "You may edit") {
		t.Fatalf("card claimed inline edit while write/edit are deferred: %q", deferredWrite)
	}
	if !strings.Contains(deferredWrite, "until requested") {
		t.Fatalf("card must say edits arrive on request: %q", deferredWrite)
	}
	// The model describes what it needs; the card never teaches catalog names.
	if strings.Contains(deferredWrite, "request_tools") || strings.Contains(deferredWrite, "`command`") {
		t.Fatalf("card must not enumerate loadable names: %q", deferredWrite)
	}

	stickyWrite := renderSurfaceCard(t, "investigate", "Empty assistant text while tools run.", "investigate",
		[]string{"recall", "read", "write", "edit", "replace_lines"}, []string{"command"})
	if !strings.Contains(stickyWrite, "You may edit inline") {
		t.Fatalf("card must offer inline edit when write is sticky: %q", stickyWrite)
	}

	noDeferred := renderSurfaceCard(t, "investigate", "Empty assistant text while tools run.", "investigate",
		[]string{"recall", "write"}, nil)
	if strings.Contains(noDeferred, "request_tools") {
		t.Fatalf("card must not offer request_tools with an empty deferred roster: %q", noDeferred)
	}
}

// Unnamed surfaces use their execution-mode family; unknown families render no card.
func TestSurfaceCardFallsBackToFamily(t *testing.T) {
	family := renderSurfaceCard(t, "", "", "orchestrate", []string{"recall", "task"}, nil)
	if !strings.Contains(family, "Surface: orchestrate.") {
		t.Fatalf("card must fall back to the execution-mode family: %q", family)
	}
	if !strings.Contains(family, "No product writes this turn.") {
		t.Fatalf("family fallback must carry the family rule: %q", family)
	}

	none := renderSurfaceCard(t, "", "", "", []string{"recall"}, nil)
	if strings.TrimSpace(none) != "" {
		t.Fatalf("no label and no family must render no card, got %q", none)
	}
}
