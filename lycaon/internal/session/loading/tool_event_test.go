package loading

import (
	"context"
	"testing"

	"github.com/lycaon/lycaon/internal/coordinator/turnload"
	"github.com/lycaon/lycaon/internal/decide/decidetest"
	"github.com/lycaon/lycaon/internal/extpacks"
	"github.com/lycaon/lycaon/internal/skills"
	"github.com/lycaon/lycaon/internal/tools"
	"github.com/lycaon/lycaon/pkg/api"
)

func TestReadSkillForToolGatesStockSkillsByOfferedToolsAndCopiesResources(t *testing.T) {
	roster := []skills.Skill{
		{
			Name:          "review-accessibility",
			Description:   "Audit a page for accessibility.",
			Body:          "Use `measure_page` and `page_open` to inspect the page, and optionally `capture_page`.",
			OptionalTools: []string{"capture_page"},
			Resources:     []string{"references/wcag.md"},
		},
	}
	sess := &api.Session{ID: "s1"}
	ledger := turnload.NewLedger()
	m := &Service{
		Ledger:  ledger,
		decider: &decidetest.Fake{Scores: []float64{3.8}},
		skills: func(context.Context, *api.Session, string, []string) ([]skills.Skill, []extpacks.Diagnostic) {
			return roster, nil
		},
	}
	m.SetSkillBodyRenderer(func(_ context.Context, _ tools.ToolContext, sk skills.Skill) (string, error) {
		return sk.Body, nil
	})

	// Turn 1: required `measure_page` is not offered on the turn.
	ledger.BeginTurn(sess.ID, turnload.TurnOpening{
		OpeningMessageID: "m1",
		Request:          "Open the web page",
		Loadable:         []string{"page_open"},
	}, turnload.Decision{})
	m.ObserveToolCall(t.Context(), sess, tools.ToolContext{
		Identity: tools.InvocationIdentity{Agent: "coordinator"},
		Turn:     tools.InvocationTurn{TurnOfferedToolNames: []string{"command", "page_open"}},
	}, "page_open")
	if got := m.SkillPreload(sess.ID); got != nil {
		t.Fatalf("preloaded stock skill without required tool: %+v", got)
	}
	if ptr := m.SkillPointer(sess.ID); ptr != nil {
		t.Fatalf("pointed to stock skill without required tool: %+v", ptr)
	}

	// Turn 2: required tools are offered while optional `capture_page` is absent.
	ledger.BeginTurn(sess.ID, turnload.TurnOpening{
		OpeningMessageID: "m2",
		Request:          "Audit the web page accessibility",
		Loadable:         []string{"page_open", "measure_page"},
	}, turnload.Decision{})
	m.ObserveToolCall(t.Context(), sess, tools.ToolContext{
		Identity: tools.InvocationIdentity{Agent: "coordinator"},
		Turn:     tools.InvocationTurn{TurnOfferedToolNames: []string{"command", "page_open", "measure_page"}},
	}, "page_open")
	got := m.SkillPreload(sess.ID)
	if got == nil || got.Name != "review-accessibility" || got.Tool != "page_open" {
		t.Fatalf("expected review-accessibility preload on tool event, got %+v", got)
	}
	if len(got.Resources) != 1 || got.Resources[0] != "references/wcag.md" {
		t.Fatalf("preload resources = %v, want [references/wcag.md]", got.Resources)
	}
}
