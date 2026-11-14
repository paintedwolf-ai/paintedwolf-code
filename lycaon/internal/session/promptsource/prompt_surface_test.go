package promptsource

import (
	"slices"
	"testing"

	"github.com/lycaon/lycaon/internal/configlayout"
	"github.com/lycaon/lycaon/internal/coordinator/turnload"
	"github.com/lycaon/lycaon/internal/prompts"
	"github.com/lycaon/lycaon/internal/session/loading"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/trainingcapture"
	"github.com/lycaon/lycaon/pkg/api"
)

func TestPromptSurfaceNamesEachToolsSourceAndTheUnitsItAttached(t *testing.T) {
	ledger := turnload.NewLedger()
	ledger.Restore("session", turnload.Standing{
		Tools: []turnload.Load{{Tool: "edit", Source: turnload.SourcePredicted}, {Tool: "update_progress", Source: turnload.SourceCompanion, With: "edit"}},
		Skill: &turnload.SkillPreload{Name: "review", Tool: "edit"},
	}, nil)
	m := &Context{
		Prompts: prompts.NewFileTemplateEngineLayers(prompts.PromptLayers{ModuleRoot: configlayout.FindModuleRoot()}),
		Loading: &loading.Service{Ledger: ledger},
	}
	offered := []string{"read", "edit", "update_progress"}
	surface, err := m.PromptSurface(t.Context(), &api.Session{ID: "session"}, prompts.CoordinatorProfileID, offered)
	testutil.FailErr(t, "build prompt surface", err)
	want := []trainingcapture.OfferedTool{{Tool: "read", Source: "standing"}, {Tool: "edit", Source: "predicted"}, {Tool: "update_progress", Source: "companion", With: "edit"}}
	if !slices.Equal(surface.Tools, want) {
		t.Fatalf("tools = %+v", surface.Tools)
	}
	if surface.Skill == nil || *surface.Skill != (trainingcapture.SkillRead{Name: "review", Tool: "edit"}) {
		t.Fatalf("skill = %+v", surface.Skill)
	}
	if surface.TurnLoadReceiptID != 0 || len(surface.Units) == 0 {
		t.Fatalf("surface = %+v", surface)
	}
	attached := false
	for _, unit := range surface.Units {
		for _, tool := range unit.AttachedBy {
			if !slices.Contains(offered, tool) {
				t.Fatalf("unit %q attached by a tool that was not offered: %q", unit.ID, tool)
			}
			attached = true
		}
	}
	if !attached {
		t.Fatal("no unit names the offered tool that attached it")
	}
}
