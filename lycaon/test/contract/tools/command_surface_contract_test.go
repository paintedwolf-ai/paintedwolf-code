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
			if strings.TrimSpace(out) == "" {
				t.Fatalf("profile %q rendered an empty tool surface", profile.ID)
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
				if !found {
					t.Fatal("command schema's required source argument is unreachable")
				}
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
			vars := prompts.AgentToolSurfaceTemplateVars(data)
			if offered, _ := vars["profile_has_command"].(bool); offered {
				t.Fatalf("profile %q offers command without granting it", profile.ID)
			}
			_, err = engine.Render(context.Background(), prompts.AgentToolSurfacePartialRef, vars)
			contractcheck.FailErr(t, "Render agent-tool-surface", err)
		})
	}
}
