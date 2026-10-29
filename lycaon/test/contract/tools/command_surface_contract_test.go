package contract

import (
	"context"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/prompts"
	"github.com/lycaon/lycaon/internal/sandbox"
	"github.com/lycaon/lycaon/internal/toolschema"
	contractcheck "github.com/lycaon/lycaon/test/contract/internal/check"
)

func commandEnabledToolProfiles(t *testing.T) []sandbox.ToolProfile {
	t.Helper()
	profiles, err := sandbox.LoadToolProfiles()
	contractcheck.FailErr(t, "LoadToolProfiles", err)
	out := make([]sandbox.ToolProfile, 0, len(profiles))
	for _, p := range profiles {
		if p.Tools["command"] {
			out = append(out, p)
		}
	}
	if len(out) == 0 {
		t.Fatal("expected at least one tool profile with command enabled")
	}
	return out
}

func TestCommandEnabledToolProfilesRenderCommandSurface(t *testing.T) {
	t.Parallel()
	configRoot := filepath.Join(contractcheck.RepoRoot(t), "lycaon")
	engine := prompts.NewFileTemplateEngineLayers(prompts.PromptLayers{})
	schemas, err := toolschema.LoadSchemaDir(filepath.Join(configRoot, "config", "packs", "painted-wolf", "platform", "tools", "schemas"))
	contractcheck.FailErr(t, "LoadSchemaDir", err)
	hints, err := prompts.LoadHintCodeRows()
	contractcheck.FailErr(t, "LoadHintCodeRows", err)

	for _, profile := range commandEnabledToolProfiles(t) {
		t.Run(profile.ID, func(t *testing.T) {
			t.Parallel()
			data, err := prompts.LoadAgentToolSurface(profile.ID, nil, hints, prompts.SurfaceTurn{Schemas: schemas}, []sandbox.ToolProfile{profile})
			contractcheck.FailErr(t, "LoadAgentToolSurface", err)
			vars := prompts.AgentToolSurfaceTemplateVars(data)
			vars["session_scratch_dir"] = filepath.Join(t.TempDir(), "session-scratch")
			out, err := engine.Render(context.Background(), prompts.AgentToolSurfacePartialRef, vars)
			contractcheck.FailErr(t, "Render agent-tool-surface", err)
			for _, want := range []string{
				"## command",
				"GNU and BSD",
				"--help",
				"@scratch",
			} {
				if !strings.Contains(out, want) {
					t.Fatalf("profile %q surface missing %q", profile.ID, want)
				}
			}
			for _, forbid := range []string{"argv host runner", "not a shell", "COMMAND_NOT_ARGV", "COMMAND_ARGV_REQUIRED", "separate command calls", "separate `command` calls", "pipes never execute"} {
				if strings.Contains(strings.ToLower(out), strings.ToLower(forbid)) {
					t.Fatalf("profile %q surface must not include %q", profile.ID, forbid)
				}
			}
			if data.CommandEffects != profile.CommandEffects || data.CommandDeferred != profile.ToolDeferred("command") {
				t.Fatalf("command surface lost profile facts: %+v", data)
			}
			if !data.CommandDeferred {
				found := false
				for _, tool := range data.Tools {
					if tool.Name == "command" {
						found = slices.Contains(tool.RequiredArgs, "command")
					}
				}
				if !found || !strings.Contains(out, "`command`: `command`") {
					t.Fatal("command schema's required source argument is unreachable")
				}
			}
			mutates, _ := vars["profile_command_mutation"].(bool)
			hidden, _ := vars["command_external_hidden"].(bool)
			if strings.Contains(out, "external -- NAME") != (mutates && !hidden) {
				t.Fatalf("external selector differs from profile facts: %+v", vars)
			}
			commandIdx := strings.Index(out, "## command")
			toolIdx := strings.Index(out, "### Tools")
			if commandIdx < 0 || toolIdx < 0 || commandIdx > toolIdx {
				t.Fatalf("command surface must precede tool inventory: command@%d tools@%d", commandIdx, toolIdx)
			}
		})
	}
}

func TestCommandFreeToolProfilesExcludeCommandSurface(t *testing.T) {
	t.Parallel()
	configRoot := filepath.Join(contractcheck.RepoRoot(t), "lycaon")
	engine := prompts.NewFileTemplateEngineLayers(prompts.PromptLayers{})
	schemas, err := toolschema.LoadSchemaDir(filepath.Join(configRoot, "config", "packs", "painted-wolf", "platform", "tools", "schemas"))
	contractcheck.FailErr(t, "LoadSchemaDir", err)
	hints, err := prompts.LoadHintCodeRows()
	contractcheck.FailErr(t, "LoadHintCodeRows", err)

	profiles, err := sandbox.LoadToolProfiles()
	contractcheck.FailErr(t, "LoadToolProfiles", err)
	for _, profile := range profiles {
		if profile.Tools["command"] {
			continue
		}
		t.Run(profile.ID, func(t *testing.T) {
			t.Parallel()
			data, err := prompts.LoadAgentToolSurface(profile.ID, nil, hints, prompts.SurfaceTurn{Schemas: schemas}, []sandbox.ToolProfile{profile})
			contractcheck.FailErr(t, "LoadAgentToolSurface", err)
			out, err := engine.Render(context.Background(), prompts.AgentToolSurfacePartialRef, prompts.AgentToolSurfaceTemplateVars(data))
			contractcheck.FailErr(t, "Render agent-tool-surface", err)
			for _, forbid := range []string{"## command", "not a shell", "argv host runner", "COMMAND_NOT_ARGV", "COMMAND_ARGV_REQUIRED"} {
				if strings.Contains(out, forbid) {
					t.Fatalf("profile %q must not include command surface fragment %q", profile.ID, forbid)
				}
			}
		})
	}
}

func TestWorkerPersonasWithCommandIncludeCommandSurface(t *testing.T) {
	engine := prompts.NewFileTemplateEngineLayers(prompts.PromptLayers{})
	cfg, err := prompts.LoadPersonaContract()
	contractcheck.FailErr(t, "LoadPersonaContract", err)
	profiles, err := sandbox.LoadToolProfiles()
	contractcheck.FailErr(t, "LoadToolProfiles", err)
	profileByID := make(map[string]sandbox.ToolProfile, len(profiles))
	for _, p := range profiles {
		profileByID[p.ID] = p
	}

	prompts.ResetPersonaContractCache()
	for _, agentID := range cfg.WorkerAgentIDs() {
		if _, ok := cfg.Agents[agentID]; !ok {
			t.Fatalf("missing persona contract for worker %q", agentID)
		}
		profileID, err := prompts.ToolProfileForAgent(agentID)
		contractcheck.FailErr(t, "ToolProfileForAgent", err)
		profile, ok := profileByID[profileID]
		if !ok || !profile.Tools["command"] {
			continue
		}
		t.Run(agentID, func(t *testing.T) {
			got, err := prompts.RenderPersona(context.Background(), engine, agentID, nil)
			contractcheck.FailErr(t, "RenderPersona", err)
			if !profile.ToolDeferred("command") && !strings.Contains(got, "`command`: `command`") {
				t.Fatal("persona lacks the command schema source argument")
			}
			for _, want := range []string{
				"## command",
				"GNU and BSD", "--help",
			} {
				if !strings.Contains(got, want) {
					t.Fatalf("persona missing %q", want)
				}
			}
		})
	}
}
