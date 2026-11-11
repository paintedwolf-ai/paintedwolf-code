package contract

import (
	"context"
	"testing"

	"github.com/lycaon/lycaon/internal/configlayout"
	"github.com/lycaon/lycaon/internal/orchestration"
	"github.com/lycaon/lycaon/internal/prompts"
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

func TestEachAgentPersonaRenders(t *testing.T) {
	contractcheck.ActivateStockCatalog(t)
	prompts.ResetPersonaContractCache()
	cfg, err := prompts.LoadPersonaContract()
	contractcheck.FailErr(t, "prompts.LoadPersonaContract failed", err)
	engine := contractPersonaEngine(t)
	for id := range cfg.Agents {
		if _, err := prompts.RenderPersona(context.Background(), engine, id, nil); err != nil {
			t.Fatalf("agent %q: %v", id, err)
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
