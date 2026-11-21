package promptsource

import (
	"slices"
	"testing"

	"github.com/lycaon/lycaon/internal/configlayout"
	"github.com/lycaon/lycaon/internal/coordinator/turnload"
	"github.com/lycaon/lycaon/internal/hostctx"
	"github.com/lycaon/lycaon/internal/prompts"
	"github.com/lycaon/lycaon/internal/session/loading"
	"github.com/lycaon/lycaon/internal/session/profiles"
	"github.com/lycaon/lycaon/internal/session/store"
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

func TestProcedureReaderNarrowsAskModeAcrossPosturesIncludingEmptyPosture(t *testing.T) {
	postures, err := profiles.LoadPostureRegistry()
	testutil.FailErr(t, "load posture registry", err)

	for _, posture := range []api.SessionPosture{
		"",
		api.SessionPostureSpec,
		api.SessionPostureVet,
		api.SessionPostureBuild,
		api.SessionPostureOrchestrate,
	} {
		mem := store.NewMemory()
		ask := api.SessionModeAsk
		sess, err := mem.Create(t.Context(), api.CreateSessionRequest{
			Mode:    &ask,
			Posture: posture,
		}, "proj-1")
		testutil.FailErr(t, "create session", err)

		prof := profiles.New(mem, nil, nil)
		prof.SetPostureRegistry(postures)
		m := &Context{
			Profiles: prof,
			Sessions: mem,
		}
		reader, err := m.procedureReader(t.Context(), &api.Session{
			ID:      sess.ID,
			Mode:    api.SessionModeAsk,
			Posture: posture,
		}, "implement_investigate", []string{"command", "command_output", "command_stop"})
		testutil.FailErr(t, "procedureReader", err)
		vars := reader.Facts()
		for _, flag := range []string{
			"profile_command_mutation",
			"profile_command_external_exec",
			"profile_command_network_egress",
			"profile_has_terminal_capture",
		} {
			if vars[flag] != false {
				t.Fatalf("posture %q %s = %v, want false", posture, flag, vars[flag])
			}
		}

		// Even if the store upgrades to Agent, a round bound to Ask keeps procedureReader read-only.
		_, err = mem.SetMode(t.Context(), sess.ID, api.SessionModeAgent)
		testutil.FailErr(t, "upgrade store to agent", err)
		roundCtx := hostctx.WithRoundMode(t.Context(), sess.ID, api.SessionModeAsk)
		roundReader, err := m.procedureReader(roundCtx, &api.Session{
			ID:      sess.ID,
			Mode:    api.SessionModeAsk,
			Posture: posture,
		}, "implement_investigate", []string{"command", "command_output", "command_stop"})
		testutil.FailErr(t, "procedureReader bound round", err)
		roundVars := roundReader.Facts()
		for _, flag := range []string{
			"profile_command_mutation",
			"profile_command_external_exec",
			"profile_command_network_egress",
		} {
			if roundVars[flag] != false {
				t.Fatalf("posture %q round-bound %s = %v, want false", posture, flag, roundVars[flag])
			}
		}
	}
}
