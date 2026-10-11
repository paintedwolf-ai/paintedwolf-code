package confine

import (
	"context"
	"sync"
)

// EgressRuleEffect is the authored policy result for one host. Reusable authority
// is evaluated separately by the approval gate.
type EgressRuleEffect string

const (
	EgressRuleDeny EgressRuleEffect = "deny"
	EgressRuleAsk  EgressRuleEffect = "ask"
)

// EgressRuleResult retains the authored pattern for citations.
type EgressRuleResult struct {
	Effect  EgressRuleEffect
	Pattern string
	UnitID  string
	PackID  string
	Scope   string
}

type egressRuleRegistration struct {
	fn func(context.Context, EgressCommand, string) EgressRuleResult
}

var ruleEvalMu sync.RWMutex
var activeRuleEval *egressRuleRegistration

// SetEgressRuleEvaluator installs the host policy until its owner releases it.
func SetEgressRuleEvaluator(fn func(context.Context, EgressCommand, string) EgressRuleResult) func() {
	registration := &egressRuleRegistration{fn: fn}
	ruleEvalMu.Lock()
	activeRuleEval = registration
	ruleEvalMu.Unlock()
	return func() {
		ruleEvalMu.Lock()
		defer ruleEvalMu.Unlock()
		if activeRuleEval == registration {
			activeRuleEval = nil
		}
		registration.fn = nil
	}
}

func currentRuleEval() func(context.Context, EgressCommand, string) EgressRuleResult {
	ruleEvalMu.RLock()
	defer ruleEvalMu.RUnlock()
	if activeRuleEval == nil {
		return nil
	}
	return activeRuleEval.fn
}
