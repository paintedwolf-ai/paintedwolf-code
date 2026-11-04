package assembly

import (
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/coordinator/turnload"
	"github.com/lycaon/lycaon/internal/prompts/promptstest"
	"github.com/lycaon/lycaon/pkg/api"
)

func TestSkillPreloadInjectUsesRenderedProcedureWithoutRoster(t *testing.T) {
	e := &AssemblyEngine{}
	selected := &turnload.SkillPreload{Name: "selected-skill", Score: 3.6, Body: "Run the targeted check."}
	e.SetDeps(AssemblyDeps{Injects: promptstest.InjectRenderer(t), SkillPreload: func(string) *turnload.SkillPreload { return selected }})
	for i := 0; i < 2; i++ {
		message, ok := testTurnContext(e).skillProcedureInject(t.Context(), &api.Session{ID: "s"}, "coordinator")
		if !ok || !strings.Contains(message.Content, selected.Body) || !strings.Contains(message.Content, "### Procedure: selected-skill") || strings.Contains(message.Content, "skills_read") {
			t.Fatalf("procedure=%+v present=%v", message, ok)
		}
		if !message.ContextPinned || message.Authority != api.ContentAuthoritySystem {
			t.Fatalf("procedure envelope=%+v", message)
		}
	}
	selected.Resources = []string{"references/guide.md"}
	if message, ok := testTurnContext(e).skillProcedureInject(t.Context(), &api.Session{ID: "s"}, "coordinator"); !ok || !strings.Contains(message.Content, `"need":"selected-skill"`) || !strings.Contains(message.Content, "references/guide.md") {
		t.Fatalf("procedure with resources=%+v present=%v", message, ok)
	}
	selected = nil
	if _, ok := testTurnContext(e).skillProcedureInject(t.Context(), &api.Session{ID: "s"}, "coordinator"); ok {
		t.Fatal("injected an unselected skill")
	}
}
