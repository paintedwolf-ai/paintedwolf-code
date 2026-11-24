package promptsource

import (
	"context"
	"slices"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/configlayout"
	"github.com/lycaon/lycaon/internal/coordinator/anchor"
	"github.com/lycaon/lycaon/internal/coordinator/turnload"
	"github.com/lycaon/lycaon/internal/hostctx"
	"github.com/lycaon/lycaon/internal/prompts"
	"github.com/lycaon/lycaon/internal/session/loading"
	"github.com/lycaon/lycaon/internal/session/profiles"
	"github.com/lycaon/lycaon/internal/session/store"
	"github.com/lycaon/lycaon/internal/testdbfixture"
	"github.com/lycaon/lycaon/internal/testdbseed"
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

// verify carries a narrowed schema (socks_proxy only) while command is offered with nil schema.
func TestToolProceduresPartialSchemasKeepExactPerToolCapabilities(t *testing.T) {
	reg, err := anchor.LoadRegistryFromConfigRoot()
	testutil.FailErr(t, "load anchor registry", err)
	t.Cleanup(anchor.SetAnchorsFor(func(context.Context, string) *anchor.Registry { return reg }))
	narrowedVerify := map[string]any{
		"type":       "object",
		"properties": map[string]any{"socks_proxy": map[string]any{"type": "boolean"}},
	}
	m := &Context{
		Prompts: prompts.NewFileTemplateEngineLayers(prompts.PromptLayers{ModuleRoot: configlayout.FindModuleRoot()}),
	}
	sess := &api.Session{ID: "proc-partial", AgentType: "implementer", WorkspacePath: t.TempDir(), Posture: api.SessionPostureBuild}

	partialCtx := prompts.WithProjectedSchemas(t.Context(), map[string]map[string]any{
		"verify": narrowedVerify,
	})
	reader, err := m.procedureReader(partialCtx, sess, "implementer", []string{"command", "verify"})
	testutil.FailErr(t, "procedureReader partial", err)
	facts := reader.Facts()
	if got, _ := facts["runner_socks_proxy_tools"].([]string); !slices.Equal(got, []string{"verify"}) {
		t.Fatalf("runner_socks_proxy_tools = %v, want [verify]", got)
	}
	if got, _ := facts["runner_capability_request_tools"].([]string); len(got) != 0 {
		t.Fatalf("runner_capability_request_tools = %v, want empty", got)
	}
	if facts["profile_has_command"] != true || facts["can_command_verify"] != true {
		t.Fatalf("command presence/effect facts lost: has=%v verify=%v", facts["profile_has_command"], facts["can_command_verify"])
	}

	block, err := m.ToolProcedures(partialCtx, sess, "implementer", []string{"command", "verify"})
	testutil.FailErr(t, "ToolProcedures partial", err)
	if !strings.Contains(block, "`socks_proxy: true` on `verify`") || strings.Contains(block, "`command` or `verify`") {
		t.Fatalf("procedure block did not restrict socks_proxy to verify:\n%s", block)
	}
	if strings.Contains(block, "Declare access needs in `capability_request`") {
		t.Fatalf("procedure block leaked pruned capability_request:\n%s", block)
	}
	if !strings.Contains(block, "Use `command(verification: true)`") {
		t.Fatalf("procedure block lost command effect capability:\n%s", block)
	}

	emptyCtx := prompts.WithProjectedSchemas(t.Context(), map[string]map[string]any{})
	emptyBlock, err := m.ToolProcedures(emptyCtx, sess, "implementer", []string{"command", "verify"})
	testutil.FailErr(t, "ToolProcedures empty", err)
	if strings.Contains(emptyBlock, "socks_proxy: true") || strings.Contains(emptyBlock, "Declare access needs in `capability_request`") {
		t.Fatalf("empty projected map fell back to raw catalog schemas:\n%s", emptyBlock)
	}
}

func TestProcedureReaderNarrowsAskModeAcrossPosturesAndZeroesEmptyStoredPosture(t *testing.T) {
	postures, err := profiles.LoadPostureRegistry()
	testutil.FailErr(t, "load posture registry", err)

	for _, posture := range []api.SessionPosture{
		api.SessionPostureSpec,
		api.SessionPostureVet,
		api.SessionPostureBuild,
		api.SessionPostureOrchestrate,
	} {
		sqlDB := testdbfixture.Open(t, string(posture)+".db")
		testdbseed.InsertProjectRoot(t, sqlDB, testdbseed.DefaultProjectID, t.TempDir())
		ctx := testdbseed.OwnerCaller(t, t.Context(), sqlDB)
		sqlStore := store.NewSQL(sqlDB).Sessions
		ask := api.SessionModeAsk
		sess, err := sqlStore.Create(ctx, api.CreateSessionRequest{
			Mode:    &ask,
			Posture: posture,
		}, testdbseed.DefaultProjectID)
		testutil.FailErr(t, "create session", err)

		prof := profiles.New(sqlStore, nil, nil)
		prof.SetPostureRegistry(postures)
		m := &Context{
			Profiles: prof,
			Sessions: sqlStore,
		}
		reader, err := m.procedureReader(ctx, &api.Session{
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
		_, err = sqlStore.SetMode(ctx, sess.ID, api.SessionModeAgent)
		testutil.FailErr(t, "upgrade store to agent", err)
		roundCtx := hostctx.WithRoundMode(ctx, sess.ID, api.SessionModeAsk)
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

	// An empty stored posture on the session record yields zero command effects in both Agent and Ask modes.
	for _, mode := range []api.SessionMode{api.SessionModeAgent, api.SessionModeAsk} {
		prof := profiles.New(emptyPostureSessionStore{mode: mode}, nil, nil)
		prof.SetPostureRegistry(postures)
		m := &Context{
			Profiles: prof,
		}
		for _, profileID := range []string{"implement_investigate", "implement"} {
			reader, err := m.procedureReader(t.Context(), &api.Session{
				ID:      "empty-posture",
				Mode:    mode,
				Posture: "",
			}, profileID, []string{"command", "command_output", "command_stop"})
			testutil.FailErr(t, "procedureReader empty posture", err)
			vars := reader.Facts()
			for _, flag := range []string{
				"profile_command_mutation",
				"profile_command_external_exec",
				"profile_command_network_egress",
				"profile_has_terminal_capture",
			} {
				if vars[flag] != false {
					t.Fatalf("empty posture mode=%q profile=%q %s = %v, want false", mode, profileID, flag, vars[flag])
				}
			}
		}
	}
}

type emptyPostureSessionStore struct {
	mode api.SessionMode
}

func (s emptyPostureSessionStore) Get(_ context.Context, id string) (*api.Session, error) {
	return &api.Session{ID: id, Mode: s.mode, Posture: ""}, nil
}

func (s emptyPostureSessionStore) ResolveEffectiveMode(_ context.Context, id string) (store.RootAuthority, error) {
	return store.RootAuthority{RootSessionID: id, Mode: s.mode}, nil
}
