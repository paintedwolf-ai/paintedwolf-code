package contract

import (
	"context"
	"io/fs"
	"path/filepath"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/config"
	"github.com/lycaon/lycaon/internal/configlayout"
	"github.com/lycaon/lycaon/internal/orchestration"
	"github.com/lycaon/lycaon/internal/prompts"
	"github.com/lycaon/lycaon/internal/sandbox"
	contractcheck "github.com/lycaon/lycaon/test/contract/internal/check"
)

func contractPromptLayers(t *testing.T) prompts.PromptLayers {
	t.Helper()
	mod := configlayout.FindModuleRoot()
	return prompts.PromptLayers{
		ModuleRoot: mod,
	}
}

func contractPersonaEngine(t *testing.T) *prompts.FileTemplateEngine {
	t.Helper()
	return prompts.NewFileTemplateEngineLayers(contractPromptLayers(t))
}

// bundledAgentPrompts reads agent prompts across all embedded stock packs.
func bundledAgentPrompts(t *testing.T) map[config.Rel]string {
	t.Helper()
	out := map[config.Rel]string{}
	err := config.Walk(config.StockPacks, func(rel config.Rel, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() || !strings.HasSuffix(rel.String(), ".md") {
			return nil
		}
		if !strings.Contains(rel.String(), "/agents/prompts/") {
			return nil
		}
		raw, err := config.Read(rel)
		if err != nil {
			return err
		}
		out[rel] = string(raw)
		return nil
	})
	contractcheck.FailErr(t, "walk bundled agent prompts", err)
	if len(out) == 0 {
		t.Fatal("no bundled agent prompts found")
	}
	return out
}

func TestBundledPromptsNoHtmlInclude(t *testing.T) {
	t.Parallel()
	var hits []string
	for rel, body := range bundledAgentPrompts(t) {
		if strings.Contains(body, "<!-- include") {
			hits = append(hits, rel.String())
		}
	}
	if len(hits) > 0 {
		t.Fatalf("HTML include comments in bundled prompts: %v", hits)
	}
}

func TestAgentMarkdownNoLegacyIncludeDirective(t *testing.T) {
	t.Parallel()
	cfg, err := prompts.LoadPersonaContract()
	contractcheck.FailErr(t, "prompts.LoadPersonaContract failed", err)
	layout := prompts.DefaultBundledLayout()
	for id := range cfg.Agents {
		raw, _, err := layout.ReadBundled(templateRefFor(t, id))
		contractcheck.FailErr(t, "read template for "+id, err)
		if strings.Contains(string(raw), "<!-- include:") {
			t.Fatalf("agent %q contains legacy <!-- include: directive", id)
		}
	}
}

func TestPersonaContractYAMLValid(t *testing.T) {
	t.Parallel()
	cfg, err := prompts.LoadPersonaContract()
	contractcheck.FailErr(t, "prompts.LoadPersonaContract failed", err)
	if cfg.Version != 2 {
		t.Fatalf("version = %d want 2", cfg.Version)
	}
	// Coverage is asserted against the agent registry, not a count pinned here.
	if len(cfg.Agents) == 0 {
		t.Fatal("persona contract defines no agents")
	}
	if len(cfg.Archetypes) < 5 {
		t.Fatalf("archetypes = %d want at least 5", len(cfg.Archetypes))
	}
}

func TestArchetypeRequiredPartialsExist(t *testing.T) {
	t.Parallel()
	cfg, err := prompts.LoadPersonaContract()
	contractcheck.FailErr(t, "prompts.LoadPersonaContract failed", err)
	layers := contractPromptLayers(t)
	if err := prompts.ValidateArchetypePartials(layers, cfg); err != nil {
		contractcheck.FailErr(t, "prompts.ValidateArchetypePartials failed", err)
	}
}

func TestEachAgentMeetsPersonaContract(t *testing.T) {
	contractcheck.ActivateStockCatalog(t)
	prompts.ResetPersonaContractCache()
	cfg, err := prompts.LoadPersonaContract()
	contractcheck.FailErr(t, "prompts.LoadPersonaContract failed", err)
	engine := contractPersonaEngine(t)
	for id, def := range cfg.Agents {
		got, err := prompts.RenderPersona(context.Background(), engine, id, nil)
		if err != nil {
			t.Fatalf("agent %q: %v", id, err)
		}
		for _, heading := range cfg.RequiredHeadingsRendered {
			if !strings.Contains(got, heading) {
				t.Fatalf("agent %q missing %s in rendered output", id, heading)
			}
		}
		for _, sub := range def.MustSubstringsRendered {
			if !strings.Contains(got, sub) {
				t.Fatalf("agent %q missing substring %q in rendered output", id, sub)
			}
		}
		if def.Advisory && !strings.Contains(got, "advisory only") {
			t.Fatalf("agent %q missing advisory partial body", id)
		}
	}
}

func TestAdvisoryAgentsIncludePartialBody(t *testing.T) {
	contractcheck.ActivateStockCatalog(t)
	prompts.ResetPersonaContractCache()
	cfg, err := prompts.LoadPersonaContract()
	contractcheck.FailErr(t, "prompts.LoadPersonaContract failed", err)
	engine := contractPersonaEngine(t)
	for id, def := range cfg.Agents {
		if !def.Advisory {
			continue
		}
		got, err := prompts.RenderPersona(context.Background(), engine, id, nil)
		if err != nil {
			t.Fatalf("agent %q: %v", id, err)
		}
		if !strings.Contains(got, "advisory only") || !strings.Contains(got, "read-only") {
			t.Fatalf("agent %q missing advisory-vs-gates body in %q", id, got)
		}
	}
}

func TestArchetypePartialsInRenderedOutput(t *testing.T) {
	contractcheck.ActivateStockCatalog(t)
	profiles, err := sandbox.LoadToolProfiles()
	contractcheck.FailErr(t, "load tool profiles", err)
	prompts.ResetPersonaContractCache()
	cfg, err := prompts.LoadPersonaContract()
	contractcheck.FailErr(t, "prompts.LoadPersonaContract failed", err)
	engine := contractPersonaEngine(t)
	for id, def := range cfg.Agents {
		got, err := prompts.RenderPersona(context.Background(), engine, id, nil)
		if err != nil {
			t.Fatalf("agent %q: %v", id, err)
		}
		arch, ok := cfg.Archetypes[def.Archetype]
		if !ok {
			t.Fatalf("agent %q unknown archetype %q", id, def.Archetype)
		}
		for _, partial := range arch.RequiredPartials {
			switch filepath.Base(partial) {
			case "advisory-vs-gates.md":
				if !strings.Contains(got, "advisory only") {
					t.Fatalf("agent %q missing advisory partial content", id)
				}
			case "agent-tool-surface.md":
				if !strings.Contains(got, "Tool schema") {
					t.Fatalf("agent %q missing agent-tool-surface partial content", id)
				}
				if !strings.Contains(got, "DOOM_LOOP") {
					t.Fatalf("agent %q missing doom-loop guidance in tool surface", id)
				}
				if !strings.Contains(got, "Branch on tool") {
					t.Fatalf("agent %q missing branch-on-Code meta-rule in tool surface", id)
				}
				// Secure floors ride agent-tool-surface (self-gated by profile_has_*_tools)
				// so new archetypes inherit them without a per-agent include.
				profileID, err := prompts.ToolProfileForAgent(id)
				contractcheck.FailErr(t, "resolve agent profile", err)
				toolSurface, err := prompts.LoadAgentToolSurface(profileID, nil, nil, prompts.SurfaceTurn{}, profiles)
				contractcheck.FailErr(t, "load agent tool surface", err)
				vars := prompts.AgentToolSurfaceTemplateVars(toolSurface)
				hasWriteTools, _ := vars["profile_has_write_tools"].(bool)
				hasReadTools, _ := vars["profile_has_read_tools"].(bool)
				hasWriteFloor := strings.Contains(got, "Security floor.")
				hasReadFloor := strings.Contains(got, "Secret reads.")
				if hasWriteTools != hasWriteFloor {
					t.Fatalf("agent %q write-tools/floor mismatch (tools=%v floor=%v)", id, hasWriteTools, hasWriteFloor)
				}
				if hasReadTools != hasReadFloor {
					t.Fatalf("agent %q read-tools/floor mismatch (tools=%v floor=%v)", id, hasReadTools, hasReadFloor)
				}
			case "finish-handoff.md":
				if !strings.Contains(got, "leg_status") {
					t.Fatalf("agent %q missing finish-handoff partial content", id)
				}
			}
		}
	}
}

func TestAgentMarkdownNoLegContextLiterals(t *testing.T) {
	t.Parallel()
	cfg, err := prompts.LoadPersonaContract()
	contractcheck.FailErr(t, "prompts.LoadPersonaContract failed", err)
	layout := prompts.DefaultBundledLayout()
	forbidden := []string{"leg_id:", "phase_id:", "workflow_id:", "- read\n", "- write\n", "- grep\n"}
	for id := range cfg.Agents {
		raw, _, err := layout.ReadBundled(templateRefFor(t, id))
		if err != nil {
			t.Fatalf("agent %q: %v", id, err)
		}
		content := string(raw)
		if strings.Count(content, "\n") < 2 {
			t.Fatalf("agent %q md is stub-only (%d lines)", id, strings.Count(content, "\n")+1)
		}
		for _, lit := range forbidden {
			if strings.Contains(content, lit) {
				t.Fatalf("agent %q md contains forbidden literal %q", id, lit)
			}
		}
	}
}

func TestScopedWriteProfilesRenderWriteGlobsSSOT(t *testing.T) {
	contractcheck.ActivateStockCatalog(t)
	prompts.ResetPersonaContractCache()
	engine := contractPersonaEngine(t)
	for _, id := range []string{"plan-writer"} {
		got, err := prompts.RenderPersona(context.Background(), engine, id, nil)
		contractcheck.FailErr(t, "RenderPersona "+id, err)
		for _, want := range []string{
			"Allowed write paths",
			"WRITE_SCOPE_DENIED",
		} {
			if !strings.Contains(got, want) {
				t.Fatalf("agent %q missing %q in rendered persona", id, want)
			}
		}
	}
}

// Every registered agent that renders a persona needs a contract row, and the
// contract carries none for agents that do not exist. An agent the contract
// cannot resolve reads as an unknown archetype, and the host facts derived from
// one go unset, which makes their gates no-ops.
func TestPersonaContractCoversEveryRegisteredAgent(t *testing.T) {
	t.Parallel()
	reg := orchestration.NewMemoryAgentRegistry()
	contractcheck.FailErr(t, "LoadRequiredAgentRegistry", orchestration.LoadRequiredAgentRegistry(context.Background(), reg))
	var ids []string
	for _, agent := range reg.List() {
		ids = append(ids, agent.ID)
	}
	cfg, err := prompts.LoadPersonaContract()
	contractcheck.FailErr(t, "prompts.LoadPersonaContract failed", err)
	var worker []string
	for _, id := range ids {
		if id != orchestration.ProfileCoordinator {
			worker = append(worker, id)
		}
	}
	contractcheck.FailErr(t, "ValidatePersonaContractCoverage", prompts.ValidatePersonaContractCoverage(cfg, worker))
	// The reverse direction is a stock-authoring property: with nothing disabled
	// every row names an agent this suite registers.
	registered := make(map[string]bool, len(ids))
	for _, id := range ids {
		registered[id] = true
	}
	for id := range cfg.Agents {
		if !registered[id] {
			t.Fatalf("persona contract row %q names no registered agent", id)
		}
	}
}

// templateRefFor resolves the agent's template the way the renderer does.
func templateRefFor(t *testing.T, agentID string) string {
	t.Helper()
	ref, err := prompts.TemplateRefForAgent(agentID)
	contractcheck.FailErr(t, "template ref for "+agentID, err)
	return ref
}
