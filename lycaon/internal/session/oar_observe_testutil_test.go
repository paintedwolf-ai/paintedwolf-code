package session_test

import (
	"context"
	"fmt"
	"path/filepath"
	"runtime"
	"sync"
	"testing"

	"github.com/lycaon/lycaon/internal/oar"
	"github.com/lycaon/lycaon/internal/testutil"
)

type sessionTestPolicy struct {
	rules  *oar.RuleSet
	loader *oar.Loader
}

var loadSessionTestPolicy = sync.OnceValues(func() (sessionTestPolicy, error) {
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		return sessionTestPolicy{}, fmt.Errorf("locate session test policy")
	}
	root := filepath.Clean(filepath.Join(filepath.Dir(file), "..", ".."))
	loader, err := oar.NewLoader(filepath.Join(filepath.Dir(root), "schemas"))
	if err != nil {
		return sessionTestPolicy{}, err
	}
	rules, err := loader.LoadEffectivePolicy()
	return sessionTestPolicy{rules: rules, loader: loader}, err
})

func sessionTestOARPipeline(t *testing.T) *oar.GuardPipeline {
	t.Helper()
	policy, err := loadSessionTestPolicy()
	testutil.FailErr(t, "load session test policy", err)
	// Each host owns its counters, providers, and delivery sinks.
	p := oar.NewGuardPipeline(policy.rules, policy.loader, oar.NewCounterStore())
	for _, anchor := range []string{
		oar.AnchorCoordinatorPreInvoke,
		oar.AnchorCoordinatorCloseoutCheck,
		oar.AnchorCoordinatorPostTurn,
		oar.AnchorToolPreInvoke,
		oar.AnchorWorkerReportCheck,
		oar.AnchorToolPost,
	} {
		p.EnableAnchor(anchor)
	}
	return p
}

func evaluateHasCode(t *testing.T, gc *oar.GuardContext, code string, anchors ...string) bool {
	t.Helper()
	if gc == nil || code == "" {
		return false
	}
	p := sessionTestOARPipeline(t)
	if len(anchors) == 0 {
		anchors = []string{oar.AnchorCoordinatorPreInvoke, oar.AnchorCoordinatorCloseoutCheck, oar.AnchorWorkerReportCheck, oar.AnchorToolPreInvoke}
	}
	for _, anchor := range anchors {
		res, err := p.EvaluateBlock(context.Background(), anchor, gc)
		testutil.FailErr(t, "EvaluateBlock "+anchor, err)
		if res == nil {
			continue
		}
		if res.Decision == nil {
			continue
		}
		if res.Decision.Code == code {
			return true
		}
		for _, a := range res.Decision.Advisories {
			if a.Code == code {
				return true
			}
		}
	}
	return false
}
