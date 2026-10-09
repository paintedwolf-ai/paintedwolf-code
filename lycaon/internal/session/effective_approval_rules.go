package session

import (
	"context"

	"github.com/lycaon/lycaon/internal/catalogview"
	"github.com/lycaon/lycaon/internal/extpacks"
	"github.com/lycaon/lycaon/internal/settings"
)

// RuleLayers returns trusted device and project approval rules.
func (m *Manager) RuleLayers(ctx context.Context, projectID string) settings.ApprovalRuleLayers {
	// Settings reads use the catalog published to this process.
	deviceView := m.Catalog.PublishedDeviceView(ctx)
	if deviceView == nil {
		return settings.ApprovalRuleLayers{}
	}
	if projectID == "" {
		return ruleLayersFromViews(deviceView, nil)
	}
	return ruleLayersFromViews(deviceView, m.Catalog.ViewForProject(ctx, projectID))
}

func ruleLayersFromViews(deviceView, projectView *catalogview.View) settings.ApprovalRuleLayers {
	if deviceView == nil {
		return settings.ApprovalRuleLayers{}
	}
	device := approvalRulesFromUnits(deviceView.ApprovalRules, settings.ApprovalRuleScopeDevice)
	if projectView == nil || projectView.Catalog == deviceView.Catalog {
		return settings.ApprovalRuleLayers{Device: device}
	}
	deviceIDs := make(map[string]struct{}, len(deviceView.ApprovalRules))
	for _, rule := range deviceView.ApprovalRules {
		deviceIDs[rule.UnitID] = struct{}{}
	}
	projectOnly := make([]extpacks.ApprovalRuleUnit, 0, len(projectView.ApprovalRules))
	for _, rule := range projectView.ApprovalRules {
		if _, exists := deviceIDs[rule.UnitID]; !exists {
			projectOnly = append(projectOnly, rule)
		}
	}
	return settings.ApprovalRuleLayers{
		Device:  device,
		Project: approvalRulesFromUnits(projectOnly, settings.ApprovalRuleScopeProject),
	}
}

func approvalRulesFromUnits(units []extpacks.ApprovalRuleUnit, scope settings.ApprovalRuleScope) []settings.ApprovalRule {
	out := make([]settings.ApprovalRule, 0, len(units))
	for _, unit := range units {
		out = append(out, settings.ApprovalRule{
			Category: settings.ApprovalCategory(unit.Category),
			Pattern:  unit.Pattern,
			Effect:   settings.ApprovalEffect(unit.Effect),
			Source:   settings.ApprovalRuleSource{UnitID: unit.UnitID, PackID: unit.PackID, Scope: scope},
		})
	}
	return out
}
