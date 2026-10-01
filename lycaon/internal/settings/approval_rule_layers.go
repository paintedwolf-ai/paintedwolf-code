package settings

import (
	"context"

	"github.com/lycaon/lycaon/internal/llm"
)

// ApprovalRuleCatalogSource supplies trust-gated extension rules.
type ApprovalRuleCatalogSource interface {
	RuleLayers(ctx context.Context, projectID string) ApprovalRuleLayers
}

// EffectiveApprovalRuleLayers composes editable and extension policy by layer.
func EffectiveApprovalRuleLayers(
	ctx context.Context,
	store *ApprovalStore,
	source ApprovalRuleCatalogSource,
	scope llm.SettingsScope,
	ref ProjectRef,
) ApprovalRuleLayers {
	var layers ApprovalRuleLayers
	if store != nil {
		layers = store.RuleLayers(scope, ref)
	}
	if source == nil {
		return layers
	}
	catalog := source.RuleLayers(ctx, ref.ID)
	layers.Device = append(layers.Device, catalog.Device...)
	layers.Project = append(layers.Project, catalog.Project...)
	return layers
}

type inertApprovalRules struct{}

func (inertApprovalRules) RuleLayers(context.Context, string) ApprovalRuleLayers {
	return ApprovalRuleLayers{}
}
