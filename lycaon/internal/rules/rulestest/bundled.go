// Package rulestest assembles the bundled posture rule engine for tests.
package rulestest

import (
	"testing"

	"github.com/lycaon/lycaon/internal/conditions"
	"github.com/lycaon/lycaon/internal/rules"
	sessionposture "github.com/lycaon/lycaon/internal/session/posture"
	"github.com/lycaon/lycaon/internal/session/profiles"
	"github.com/lycaon/lycaon/internal/testutil"
)

// BundledEngine builds a posture engine from the shipped rules and a populated registry.
func BundledEngine(t testing.TB, reg *conditions.ConditionRegistry) *rules.PostureRuleEngine {
	t.Helper()
	postures, err := profiles.LoadPostureRegistry()
	testutil.FailErr(t, "load posture registry", err)
	packs, err := rules.LoadBundledRules()
	testutil.FailErr(t, "load bundled rules", err)
	testutil.FailErr(t, "validate posture rules", rules.ValidatePostureRules(postures, sessionposture.AllSessionPostures(), packs))
	engine, err := rules.NewPostureRuleEngine(postures, packs, reg)
	testutil.FailErr(t, "construct posture rule engine", err)
	return engine
}
