package llm

import (
	"context"
	"fmt"
	"reflect"
	"slices"

	"github.com/lycaon/lycaon/internal/llm/modelcall"
	"github.com/lycaon/lycaon/internal/llm/modelinfo"
	"github.com/lycaon/lycaon/internal/llm/providerprofile"
)

// thinkingPolicyProvider resolves once outside retries and the outbound screen.
type thinkingPolicyProvider struct {
	modelcall.Provider
	registry *Registry
}

func (p *thinkingPolicyProvider) Complete(ctx context.Context, req modelcall.CompletionRequest) (*modelcall.Completion, error) {
	req, err := p.prepare(ctx, req)
	if err != nil {
		return nil, err
	}
	return p.Provider.Complete(ctx, req)
}

func (p *thinkingPolicyProvider) Stream(ctx context.Context, req modelcall.CompletionRequest) (<-chan modelcall.StreamChunk, error) {
	req, err := p.prepare(ctx, req)
	if err != nil {
		return nil, err
	}
	return p.Provider.Stream(ctx, req)
}

func (p *thinkingPolicyProvider) prepare(ctx context.Context, req modelcall.CompletionRequest) (modelcall.CompletionRequest, error) {
	req.ThinkingOverride = nil
	req.ThinkingOverrideStyle = ""
	overrides, ok := thinkingPolicyFromContext(ctx)
	if !ok {
		store := p.registry.thinkingPolicy.Load()
		if store == nil {
			return req, nil
		}
		policy, err := store.Get(SettingsScopeProject, req.Debug.ProjectDir)
		if err != nil {
			return req, err
		}
		overrides = policy.ThinkingOverrides
	}
	model := req.Model
	if model == "" && len(p.Models()) > 0 {
		model = p.Models()[0].ID
	}
	for _, override := range overrides {
		if override.ProviderID != p.ID() || override.Model != model {
			continue
		}
		if override.Mode == "application" {
			return req, nil
		}
		entry, found := p.registry.thinkingModelEntry(ctx, p.ID(), model)
		if !found {
			return req, &ThinkingOverrideError{ProviderID: p.ID(), Model: model, Reason: "the configured model is unavailable"}
		}
		if err := validateThinkingRequest(p.Profile(), entry, override, req); err != nil {
			return req, err
		}
		req.ThinkingOverride = &override
		req.ThinkingOverrideStyle = modelcall.ResolveModelThinking(p.Profile(), entry, model).Style
		return req, nil
	}
	return req, nil
}

func (r *Registry) thinkingModelEntry(ctx context.Context, providerID, model string) (modelinfo.Entry, bool) {
	resolved := r.resolveSelectedModel(ctx, providerID, model)
	for _, candidate := range resolved.Models {
		if candidate.ID == model {
			return candidate, true
		}
	}
	return modelinfo.Entry{}, false
}

func validateThinkingRequest(profile providerprofile.Profile, entry modelinfo.Entry, override modelcall.ThinkingOverride, req modelcall.CompletionRequest) error {
	fail := func(err error) error {
		return &ThinkingOverrideError{ProviderID: override.ProviderID, Model: override.Model, Reason: err.Error()}
	}
	if err := validateThinkingOverride(override); err != nil {
		return fail(err)
	}
	capabilities := modelcall.ResolveThinkingCapabilities(profile, entry, override.Model)
	if err := acceptsThinkingCapabilities(capabilities, override); err != nil {
		return fail(err)
	}
	if override.BudgetTokens != nil {
		limit := entry.MaxTokens
		if req.MaxTokens > 0 && (limit <= 0 || req.MaxTokens < limit) {
			limit = req.MaxTokens
		}
		if limit > 0 && *override.BudgetTokens >= limit {
			return fail(fmt.Errorf("thinking budget %d leaves no answer space within the output limit of %d", *override.BudgetTokens, limit))
		}
	}
	return nil
}

func (s *Service) validateThinkingPolicy(ctx context.Context, overrides, previous []modelcall.ThinkingOverride) error {
	for _, override := range overrides {
		if override.Mode == "application" {
			continue
		}
		// Allow repairing one stale entry without requiring every other saved
		// entry to become available. Each request still validates its override.
		if slices.ContainsFunc(previous, func(saved modelcall.ThinkingOverride) bool { return reflect.DeepEqual(saved, override) }) {
			continue
		}
		if s.Registry == nil {
			return fmt.Errorf("AI provider settings are not configured")
		}
		resolved := s.Registry.resolveModels(ctx, override.ProviderID)
		provider, err := s.Registry.Get(override.ProviderID)
		if err != nil {
			return err
		}
		found := false
		for _, entry := range resolved.Models {
			if entry.ID != override.Model {
				continue
			}
			found = true
			if err := validateThinkingRequest(provider.Profile(), entry, override, modelcall.CompletionRequest{}); err != nil {
				return err
			}
			break
		}
		if !found {
			return fmt.Errorf("thinking override model %q is unavailable from provider %q", override.Model, override.ProviderID)
		}
	}
	return nil
}

func acceptsThinkingCapabilities(c modelinfo.ThinkingCapabilities, o modelcall.ThinkingOverride) error {
	if o.Mode == "application" {
		return nil
	}
	if c.State != "supported" {
		return fmt.Errorf("supported thinking settings are %s", c.State)
	}
	switch {
	case o.Effort != "" && !slices.Contains(c.Efforts, o.Effort):
		return fmt.Errorf("effort %q is not supported", o.Effort)
	case o.Enabled != nil && *o.Enabled && !c.CanEnable:
		return fmt.Errorf("turning thinking on without a level or budget is not supported")
	case o.Enabled != nil && !*o.Enabled && !c.CanDisable:
		return fmt.Errorf("thinking cannot be disabled")
	case o.BudgetTokens != nil:
		if c.Budget == nil || *o.BudgetTokens < c.Budget.Min || c.Budget.Max > 0 && *o.BudgetTokens > c.Budget.Max {
			return fmt.Errorf("thinking budget is outside the supported range")
		}
	}
	return nil
}
