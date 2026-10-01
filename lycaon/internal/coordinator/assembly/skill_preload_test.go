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
		message, ok := e.skillProcedureInject(t.Context(), &api.Session{ID: "s"}, "coordinator")
		if !ok || !strings.Contains(message.Content, selected.Body) || !strings.Contains(message.Content, `"need":"selected-skill"`) {
			t.Fatalf("procedure=%+v present=%v", message, ok)
		}
		if !message.ContextPinned || message.Authority != api.ContentAuthoritySystem {
			t.Fatalf("procedure envelope=%+v", message)
		}
	}
	selected = nil
	if _, ok := e.skillProcedureInject(t.Context(), &api.Session{ID: "s"}, "coordinator"); ok {
		t.Fatal("injected an unselected skill")
	}
}
