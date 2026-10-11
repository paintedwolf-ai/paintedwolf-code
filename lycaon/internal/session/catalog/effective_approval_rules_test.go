package catalog

import (
	"runtime"
	"testing"
	"weak"

	"github.com/lycaon/lycaon/internal/catalogview"
	"github.com/lycaon/lycaon/internal/extpacks"
	"github.com/lycaon/lycaon/internal/settings"
	"github.com/lycaon/lycaon/internal/testutil"
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

// The service is embedded in its host, so a bound method retains the entire allocation.
type catalogLifetimeHost struct {
	Catalog Service
}

func stoppedCatalogHost(t *testing.T) weak.Pointer[catalogLifetimeHost] {
	t.Helper()
	host := &catalogLifetimeHost{Catalog: New(nil)}
	host.Catalog.Configure("", nil, nil)
	host.Catalog.Configure("", nil, nil)
	host.Catalog.Stop()
	host.Catalog.Stop()
	host.Catalog.Configure("", nil, nil)
	testutil.FailErr(t, "drain stopped catalog", host.Catalog.Wait(t.Context()))
	if _, _, err := host.Catalog.work.Begin(t.Context()); err == nil {
		t.Fatal("stopped catalog admitted work")
	}
	return weak.Make(host)
}

func TestStoppedCatalogReleasesItsHost(t *testing.T) {
	host := stoppedCatalogHost(t)
	runtime.GC()
	if host.Value() != nil {
		t.Fatal("active refresher retained a stopped host")
	}
}
