package llm

import (
	"context"
	"fmt"

	"github.com/lycaon/lycaon/internal/llm/modelcall"
	"github.com/lycaon/lycaon/internal/llm/modelinfo"
	"github.com/lycaon/lycaon/internal/llm/providerprofile"
	"github.com/lycaon/lycaon/internal/llm/transcript"
)

const ToolCompatibilityRevision = 1

type ToolVerification struct {
	ReasoningPolicy *ToolReasoningPolicy `json:"reasoning_policy,omitempty"`
	ToolCompatibilityResult
	DriverSHA256        string `json:"driver_sha256"`
	RequestPolicySHA256 string `json:"request_policy_sha256"`
}

type toolVerificationTarget struct {
	model    modelinfo.Entry
	provider modelcall.Provider
	profile  providerprofile.Profile
}

func (r *Registry) resolveToolVerification(ctx context.Context, providerID, model string) (toolVerificationTarget, error) {
	var target toolVerificationTarget
	models := r.EffectiveModels(ctx, providerID)
	snapshot := r.snapshot.Load()
	if snapshot == nil {
		return target, fmt.Errorf("provider settings are not ready")
	}
	entry, ok := snapshot.entries[providerID]
	if !ok || snapshot.providers[providerID] == nil {
		return target, fmt.Errorf("provider is unavailable")
	}
	for _, candidate := range models {
		if !modelinfo.EquivalentID(entry.Kind, candidate.ID, model) {
			continue
		}
		eligibility := ModelRoleEligibility(entry.Kind, candidate, PolicySlotCoordinator, r.roleExclusions)
		if !eligibility.Selectable {
			return target, fmt.Errorf("%s", eligibility.Reason)
		}
		provider := snapshot.providers[providerID]
		profile := provider.Profile()
		return toolVerificationTarget{model: candidate, provider: provider, profile: profile}, nil
	}
	return target, fmt.Errorf("model is not available from this provider")
}

// CheckToolCompatibility is an explicit paid diagnostic, never a side effect of listing.
func (r *Registry) CheckToolCompatibility(ctx context.Context, providerID, model string) (ToolVerification, error) {
	target, err := r.resolveToolVerification(ctx, providerID, model)
	if err != nil {
		return ToolVerification{}, err
	}
	return target.check(ctx)
}

func (target toolVerificationTarget) check(ctx context.Context) (ToolVerification, error) {
	reasoning := toolReasoningPolicy(target.profile, target.model)
	result := ToolVerification{ReasoningPolicy: &reasoning}
	var err error
	result.DriverSHA256, err = toolDriverIdentity(target.profile)
	if err != nil {
		return result, err
	}
	result.RequestPolicySHA256, err = toolRequestIdentity(target.profile, target.model)
	if err != nil {
		return result, err
	}
	observation, err := ToolCompatibility(ctx, func(ctx context.Context, request modelcall.CompletionRequest) (<-chan modelcall.StreamChunk, error) {
		request.Model = target.model.ID
		request.Messages = transcript.Project(request.Messages)
		chunks, streamErr := target.provider.Stream(ctx, request)
		if streamErr != nil {
			return nil, streamErr
		}
		return streamWithSelection(ctx, chunks, &ModelSelection{ProviderID: target.provider.ID(), Model: target.model.ID}), nil
	})
	result.ToolCompatibilityResult = observation
	if err != nil {
		return result, err
	}
	currentIdentity, err := toolRequestIdentity(target.profile, target.model)
	if err != nil {
		return result, err
	}
	if currentIdentity != result.RequestPolicySHA256 {
		return result, fmt.Errorf("request policy changed during the compatibility check")
	}
	return result, nil
}
