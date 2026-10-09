package boot_test

import (
	"github.com/lycaon/lycaon/internal/boot"
	"github.com/lycaon/lycaon/internal/conditions"
	"github.com/lycaon/lycaon/internal/rules"
	"github.com/lycaon/lycaon/internal/session"
	"github.com/lycaon/lycaon/internal/session/store"
	"github.com/lycaon/lycaon/internal/settings"
	"github.com/lycaon/lycaon/internal/testdbfixture"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/workflow"
	workflowdef "github.com/lycaon/lycaon/internal/workflow/definition"
	workflowpersistence "github.com/lycaon/lycaon/internal/workflow/persistence"
	"testing"
)

func TestValidateServeWiringAcceptsProductionShape(t *testing.T) {
	postures, err := session.LoadPostureRegistry()
	testutil.FailErr(t, "session.LoadPostureRegistry failed", err)
	packs, err := rules.LoadBundledRules()
	testutil.FailErr(t, "rules.LoadBundledRules failed", err)
	reg, err := conditions.NewDefaultRegistry(conditions.RegistryDeps{})
	testutil.FailErr(t, "conditions.NewDefaultRegistry failed", err)
	if err := rules.RegisterRuleConditions(reg); err != nil {
		testutil.FailErr(t, "register rule conditions", err)
	}
	engine, err := rules.NewPostureRuleEngine(postures, packs, reg)
	testutil.FailErr(t, "rules.NewPostureRuleEngine failed", err)
	mgr := session.NewManager(store.NewMemory(), nil, nil, settings.DefaultSessionLimits())
	mgr.SetPostureRegistry(postures)
	manifests, err := workflowdef.RegistryFromDirs("")
	testutil.FailErr(t, "workflow.RegistryFromDirs failed", err)
	sqlDB := testdbfixture.Open(t, "wiring.db")
	sessStore := store.NewSQL(sqlDB)
	workflowMgr := workflow.NewManager(workflowpersistence.New(sqlDB), sessStore, manifests, nil)
	workflowMgr.SetConditionRegistry(reg)
	if err := boot.ValidateServeWiring(boot.ServeWiring{
		PostureRegistry: postures,
		BundledRules:    packs,
		RuleEngine:      engine,
		SessionManager:  mgr,
		WorkflowManager: workflowMgr,
	}); err != nil {
		t.Fatal(err)
	}
}
