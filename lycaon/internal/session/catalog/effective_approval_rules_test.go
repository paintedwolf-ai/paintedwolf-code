package catalog

import (
	"testing"

	"github.com/lycaon/lycaon/internal/catalogview"
	"github.com/lycaon/lycaon/internal/extpacks"
	"github.com/lycaon/lycaon/internal/settings"
	wire "github.com/lycaon/lycaon/pkg/api"
)

func TestRuleLayersFromViewsSeparatesPublishedDeviceAndProjectRules(t *testing.T) {
	deviceCatalog := &extpacks.EffectiveCatalog{}
	projectCatalog := &extpacks.EffectiveCatalog{}
	deviceRule := extpacks.ApprovalRuleUnit{
		UnitID: "approvals/rules/device.yaml", PackID: "device-pack",
		Category: wire.ApprovalCategoryHost, Pattern: "device.example", Effect: wire.ApprovalEffectDeny,
	}
	projectRule := extpacks.ApprovalRuleUnit{
		UnitID: "approvals/rules/project.yaml", PackID: "project-pack",
		Category: wire.ApprovalCategoryHost, Pattern: "project.example", Effect: wire.ApprovalEffectAsk,
	}

	layers := ruleLayersFromViews(
		&catalogview.View{Catalog: deviceCatalog, ApprovalRules: []extpacks.ApprovalRuleUnit{deviceRule}},
		&catalogview.View{Catalog: projectCatalog, ApprovalRules: []extpacks.ApprovalRuleUnit{deviceRule, projectRule}},
	)

	if len(layers.Device) != 1 || layers.Device[0].Source.Scope != settings.ApprovalRuleScopeDevice {
		t.Fatalf("device layers = %#v, want one device-scoped rule", layers.Device)
	}
	if len(layers.Project) != 1 || layers.Project[0].Source.Scope != settings.ApprovalRuleScopeProject {
		t.Fatalf("project layers = %#v, want one project-scoped rule", layers.Project)
	}
	if got := layers.Project[0].Source.UnitID; got != projectRule.UnitID {
		t.Fatalf("project unit = %q, want %q", got, projectRule.UnitID)
	}
}

func TestRuleLayersFromViewsDoesNotDuplicateOnePublishedCatalog(t *testing.T) {
	catalog := &extpacks.EffectiveCatalog{}
	rule := extpacks.ApprovalRuleUnit{
		UnitID: "approvals/rules/device.yaml", PackID: "device-pack",
		Category: wire.ApprovalCategoryHost, Pattern: "device.example", Effect: wire.ApprovalEffectDeny,
	}
	device := &catalogview.View{Catalog: catalog, ApprovalRules: []extpacks.ApprovalRuleUnit{rule}}

	layers := ruleLayersFromViews(device, &catalogview.View{Catalog: catalog, ApprovalRules: device.ApprovalRules})
	if len(layers.Device) != 1 || len(layers.Project) != 0 {
		t.Fatalf("layers = %#v, want device only", layers)
	}
}
