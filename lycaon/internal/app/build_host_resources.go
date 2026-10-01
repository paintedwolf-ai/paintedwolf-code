package app

import (
	"context"
	"fmt"

	"github.com/lycaon/lycaon/internal/hostresources"
	"github.com/lycaon/lycaon/internal/llm"
	"github.com/lycaon/lycaon/internal/settings"
)

func (b *serveBuilder) wireHostResources() error {
	service, err := hostresources.NewService(b.dataDir)
	if err != nil {
		return fmt.Errorf("host resources service: %w", err)
	}
	b.hostResources = service
	return nil
}

func newHostResourcePolicyBinder(
	store *settings.ApprovalStore,
	source settings.ApprovalRuleCatalogSource,
) hostresources.PolicyBinder {
	return func(ctx context.Context, project hostresources.ProjectContext) hostresources.PolicyEvaluator {
		scope := llm.SettingsScopeGlobal
		if project.ID != "" || project.Dir != "" {
			scope = llm.SettingsScopeProject
		}
		ref := settings.ProjectRef{ID: project.ID, Dir: project.Dir}
		layers := settings.EffectiveApprovalRuleLayers(ctx, store, source, scope, ref)
		rules := append(append([]settings.ApprovalRule(nil), layers.Device...), layers.Project...)
		editable := store.RuleLayers(scope, ref)
		editableRules := append(append([]settings.ApprovalRule(nil), editable.Device...), editable.Project...)
		return func(resourceID, resourceFamily string) hostresources.PolicyDecision {
			effect, _, matched := settings.EvaluateHostResourceSubjects(
				rules,
				[]string{resourceID, resourceFamily},
			)
			if !matched {
				return hostresources.PolicyDecision{
					Access: hostresources.AccessAllow, Setting: hostresources.AccessSettingInherit,
				}
			}
			setting := hostresources.AccessSettingInherit
			if exactEffect, exact := settings.ExactHostResourceRule(editableRules, resourceID); exact {
				setting = hostresources.AccessSetting(exactEffect)
			}
			return hostresources.PolicyDecision{Access: hostresources.Access(effect), Setting: setting}
		}
	}
}
