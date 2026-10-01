// Package oartest installs production policy in focused host fixtures.
package oartest

import (
	"path/filepath"
	"sync"
	"testing"

	"github.com/lycaon/lycaon/internal/configlayout"
	"github.com/lycaon/lycaon/internal/extpacks"
	"github.com/lycaon/lycaon/internal/guidance"
	"github.com/lycaon/lycaon/internal/guidance/feedback"
	"github.com/lycaon/lycaon/internal/hintregistry"
	"github.com/lycaon/lycaon/internal/oar"
	"github.com/lycaon/lycaon/internal/testutil"
)

type closeoutHost interface {
	SetWorkflowHints(*guidance.HintConfig, *feedback.GateFeedbackCatalog)
	SetRejectFormatter(*guidance.StaticRejectFormatter)
	SetOARPipeline(*oar.GuardPipeline, *oar.Renderer)
}

type closeoutPolicy struct {
	loader *oar.Loader
	rules  *oar.RuleSet
	hints  *guidance.HintConfig
}

var (
	closeoutMu     sync.Mutex
	closeoutKey    string
	closeoutCached closeoutPolicy
)

// effectiveCloseoutPolicy compiles the effective policy once per distinct policy set.
func effectiveCloseoutPolicy(t *testing.T) closeoutPolicy {
	t.Helper()
	catalog, err := extpacks.CatalogForConsumers()
	testutil.FailErr(t, "resolve closeout catalog", err)
	key := hintregistry.EffectivePolicyKey(catalog)
	closeoutMu.Lock()
	defer closeoutMu.Unlock()
	if closeoutKey == key {
		return closeoutCached
	}
	loader, err := oar.NewLoader(filepath.Join(configlayout.FindModuleRoot(), "..", "schemas"))
	testutil.FailErr(t, "create closeout policy loader", err)
	rules, err := loader.LoadEffectivePolicyWithCatalog(catalog)
	testutil.FailErr(t, "load closeout policy", err)
	hints, err := guidance.LoadHintConfigStock()
	testutil.FailErr(t, "load closeout hints", err)
	closeoutKey = key
	closeoutCached = closeoutPolicy{loader: loader, rules: rules, hints: hints}
	return closeoutCached
}

// InstallCloseoutPolicy connects effective closeout rules to prompt fixtures.
func InstallCloseoutPolicy(t *testing.T, manager closeoutHost) {
	t.Helper()
	policy := effectiveCloseoutPolicy(t)
	hints := policy.hints.Clone()
	formatter := guidance.NewStaticRejectFormatter(hints)
	pipeline := oar.NewGuardPipeline(policy.rules, policy.loader, oar.NewCounterStore())
	pipeline.EnableAnchor(oar.AnchorCoordinatorCloseoutCheck)
	manager.SetWorkflowHints(hints, nil)
	manager.SetRejectFormatter(formatter)
	manager.SetOARPipeline(pipeline, oar.NewRenderer(formatter, nil))
}
