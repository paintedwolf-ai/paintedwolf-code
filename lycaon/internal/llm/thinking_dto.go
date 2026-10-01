package llm

import (
	"github.com/lycaon/lycaon/internal/llm/modelcall"
	"github.com/lycaon/lycaon/internal/llm/modelinfo"
	"github.com/lycaon/lycaon/internal/llm/providerprofile"
	"github.com/lycaon/lycaon/pkg/api"
)

func thinkingOverridesToDTO(overrides []modelcall.ThinkingOverride) []api.ThinkingOverride {
	out := make([]api.ThinkingOverride, 0, len(overrides))
	for _, o := range cloneThinkingOverrides(overrides) {
		out = append(out, api.ThinkingOverride{ProviderID: o.ProviderID, Model: o.Model, Mode: o.Mode, Effort: o.Effort, Enabled: o.Enabled, BudgetTokens: o.BudgetTokens})
	}
	return out
}

func thinkingOverridesFromDTO(overrides []api.ThinkingOverride) []modelcall.ThinkingOverride {
	out := make([]modelcall.ThinkingOverride, 0, len(overrides))
	for _, o := range overrides {
		out = append(out, modelcall.ThinkingOverride{ProviderID: o.ProviderID, Model: o.Model, Mode: o.Mode, Effort: o.Effort, Enabled: o.Enabled, BudgetTokens: o.BudgetTokens})
	}
	return cloneThinkingOverrides(out)
}

func thinkingCapabilitiesToDTO(c modelinfo.ThinkingCapabilities) api.ThinkingCapabilities {
	out := api.ThinkingCapabilities{State: c.State, Efforts: c.Efforts, CanEnable: c.CanEnable, CanDisable: c.CanDisable, DefaultEffort: c.DefaultEffort, Source: c.Source}
	if c.Budget != nil {
		out.Budget = &api.ThinkingBudgetRange{Min: c.Budget.Min, Max: c.Budget.Max}
	}
	return out
}

func registryThinkingProfile(snapshot *providerRegistrySnapshot, id string) providerprofile.Profile {
	if snapshot != nil && snapshot.providers[id] != nil {
		return snapshot.providers[id].Profile()
	}
	return providerprofile.Default()
}
